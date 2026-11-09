package cleanup

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/tasantiago/patchd/internal/jobs"
)

// errSkip: o arquivo ficou (em uso, link, fora da pasta).
var errSkip = errors.New("pulado")

func systemRoot() string {
	if r := os.Getenv("SystemRoot"); r != "" {
		return r
	}
	return `C:\Windows`
}

func systemDrive() string {
	if d := os.Getenv("SystemDrive"); d != "" {
		return d + `\`
	}
	return `C:\`
}

func systemFree() int64 {
	p, err := windows.UTF16PtrFromString(systemDrive())
	if err != nil {
		return -1
	}
	var free, total, totalFree uint64
	if windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree) != nil {
		return -1
	}
	return int64(totalFree)
}

// isLink: link simbólico, junção ou qualquer ponto de nova análise (não entra, não apaga).
func isLink(path string, d fs.DirEntry) bool {
	if d.Type()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
		return true
	}
	info, err := os.Lstat(path)
	if err != nil {
		return true
	}
	if a, ok := info.Sys().(*syscall.Win32FileAttributeData); ok && a.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return true
	}
	return false
}

// finalPath: o caminho real do handle, sem o \\?\ do começo.
func finalPath(h windows.Handle) (string, error) {
	buf := make([]uint16, 1024)
	n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), 0)
	if err != nil || int(n) >= len(buf) {
		return "", errors.New("caminho real ilegível")
	}
	return strings.TrimPrefix(windows.UTF16ToString(buf[:n]), `\\?\`), nil
}

// realRoot: o caminho real da pasta do item; ela não pode ser link, e o caminho real tem de ser
// o esperado (um link no meio do caminho, como AppData redirecionado, faz pular a pasta).
func realRoot(path string) (string, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	h, err := windows.CreateFile(p, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return "", err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return "", errors.New("a pasta é um link")
	}
	real, err := finalPath(h)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(filepath.Clean(real), filepath.Clean(path)) {
		return "", errors.New("a pasta está redirecionada para " + real)
	}
	return real, nil
}

// safeDelete apaga pelo handle: abre sem seguir pontos de nova análise, confere que não é link
// e que o caminho real está dentro da pasta do item, e marca para exclusão. Arquivo em uso ou
// somente leitura fica.
func safeDelete(root string) func(string) error {
	prefix := strings.ToLower(filepath.Clean(root)) + `\`
	return func(path string) error {
		p, err := windows.UTF16PtrFromString(path)
		if err != nil {
			return err
		}
		h, err := windows.CreateFile(p, windows.DELETE|windows.FILE_READ_ATTRIBUTES,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
			windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
		if err != nil {
			return err
		}
		defer windows.CloseHandle(h)
		var info windows.ByHandleFileInformation
		if err := windows.GetFileInformationByHandle(h, &info); err != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return errSkip
		}
		real, err := finalPath(h)
		if err != nil || !strings.HasPrefix(strings.ToLower(filepath.Clean(real)), prefix) {
			return errSkip
		}
		del := byte(1) // FILE_DISPOSITION_INFO.DeleteFile
		return windows.SetFileInformationByHandle(h, windows.FileDispositionInfo, &del, uint32(unsafe.Sizeof(del)))
	}
}

func (c *Cleaner) sweepRoot(rep *itemReport, root string) {
	real, err := realRoot(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			return
		}
		rep.skipped++
		if rep.note == "" {
			rep.note = root + ": " + err.Error()
		}
		return
	}
	c.sweep(rep, real, safeDelete(real))
}

func (c *Cleaner) item(ctx context.Context, item string) itemReport {
	rep := itemReport{item: item}
	switch item {
	case jobs.CleanupSystemTemp:
		c.sweepRoot(&rep, filepath.Join(systemRoot(), "Temp"))
	case jobs.CleanupUserTemp:
		users := filepath.Join(systemDrive(), "Users")
		entries, err := os.ReadDir(users)
		if err != nil {
			rep.note = err.Error()
			break
		}
		for _, e := range entries {
			p := filepath.Join(users, e.Name())
			if !e.IsDir() || isLink(p, e) { // "All Users", "Default User": junções
				continue
			}
			c.sweepRoot(&rep, filepath.Join(p, "AppData", "Local", "Temp"))
		}
	case jobs.CleanupPatchd:
		for _, d := range c.patchdDirs() {
			c.sweepRoot(&rep, d)
		}
	case jobs.CleanupUpdateCache:
		c.updateCache(&rep)
	case jobs.CleanupComponents:
		before := c.free()
		dism := filepath.Join(systemRoot(), "System32", "Dism.exe")
		rctx, cancel := context.WithTimeout(ctx, 2*time.Hour)
		defer cancel()
		if _, err := c.Run.Run(rctx, dism, "/Online", "/Cleanup-Image", "/StartComponentCleanup", "/NoRestart"); err != nil {
			rep.note = "o DISM falhou: " + err.Error()
		}
		if after := c.free(); before >= 0 && after > before {
			rep.freed = after - before
		}
	}
	return rep
}

// updateCache: para o Windows Update, limpa o cache de download e religa o serviço.
func (c *Cleaner) updateCache(rep *itemReport) {
	m, err := mgr.Connect()
	if err != nil {
		rep.note = "sem acesso aos serviços: " + err.Error()
		return
	}
	defer m.Disconnect()
	s, err := m.OpenService("wuauserv")
	if err != nil {
		rep.note = "serviço do Windows Update: " + err.Error()
		return
	}
	defer s.Close()
	st, err := s.Query()
	if err != nil {
		rep.note = "serviço do Windows Update: " + err.Error()
		return
	}
	wasRunning := st.State != svc.Stopped
	if wasRunning {
		if _, err := s.Control(svc.Stop); err != nil {
			rep.note = "o Windows Update não parou (atualizando agora?): " + err.Error()
			return
		}
		for i := 0; i < 60; i++ {
			if st, err = s.Query(); err == nil && st.State == svc.Stopped {
				break
			}
			time.Sleep(time.Second)
		}
		if st.State != svc.Stopped {
			rep.note = "o Windows Update não parou em 60 s: o cache ficou"
			_ = s.Start()
			return
		}
	}
	c.sweepRoot(rep, filepath.Join(systemRoot(), "SoftwareDistribution", "Download"))
	if wasRunning {
		if err := s.Start(); err != nil {
			rep.note = "o cache foi limpo, mas o Windows Update não religou: " + err.Error()
		}
	}
}
