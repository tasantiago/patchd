//go:build windows

package shortcuts

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
	"github.com/tasantiago/patchd/internal/protocol"
	"golang.org/x/sys/windows"
)

func UserManager() (*Manager, error) {
	desktop, err := windows.KnownFolderPath(windows.FOLDERID_Desktop, 0)
	if err != nil {
		return nil, err
	}
	local, err := windows.KnownFolderPath(windows.FOLDERID_LocalAppData, 0)
	if err != nil {
		return nil, err
	}
	return &Manager{Desktop: desktop, StatePath: filepath.Join(local, "patchd", "standardization", "shortcuts.json"), Write: writeWindows}, nil
}

var envPath = regexp.MustCompile(`%([^%]+)%`)

func expandPath(path string) (string, error) {
	missing := false
	path = envPath.ReplaceAllStringFunc(path, func(s string) string {
		v := os.Getenv(strings.Trim(s, "%"))
		if v == "" {
			missing = true
		}
		return v
	})
	if missing || strings.Contains(path, "%") || !filepath.IsAbs(path) {
		return "", errors.New("caminho do perfil ou aplicativo indisponível")
	}
	return path, nil
}

func writeWindows(path string, s protocol.StandardShortcut) error {
	if err := s.Validate(); err != nil {
		return err
	}
	icon := s.Icon
	if icon == "" {
		icon = s.Kind
	}
	index := 0
	switch icon {
	case "site":
		index = 14
	case "aplicativo":
		index = 2
	case "pasta":
		index = 3
	case "documento":
		index = 0
	}
	iconFile := filepath.Join(os.Getenv("SystemRoot"), "System32", "shell32.dll")
	if s.Kind == "site" {
		data := fmt.Sprintf("[InternetShortcut]\r\nURL=%s\r\nIconFile=%s\r\nIconIndex=%d\r\n", s.Target, iconFile, index)
		return os.WriteFile(path, []byte(data), 0600)
	}
	target, err := expandPath(s.Target)
	if err != nil {
		return err
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED); err != nil {
		var oe *ole.OleError
		if !errors.As(err, &oe) || oe.Code() != 1 {
			return err
		}
	}
	defer ole.CoUninitialize()
	object, err := oleutil.CreateObject("WScript.Shell")
	if err != nil {
		return err
	}
	defer object.Release()
	dispatch, err := object.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return err
	}
	defer dispatch.Release()
	value, err := oleutil.CallMethod(dispatch, "CreateShortcut", path)
	if err != nil {
		return err
	}
	link := value.ToIDispatch()
	defer link.Release()
	if _, err := oleutil.PutProperty(link, "TargetPath", target); err != nil {
		return err
	}
	if _, err := oleutil.PutProperty(link, "Description", s.Name); err != nil {
		return err
	}
	location := fmt.Sprintf("%s,%d", iconFile, index)
	if s.Icon == "" && s.Kind == "aplicativo" {
		location = target + ",0"
	}
	if _, err := oleutil.PutProperty(link, "IconLocation", location); err != nil {
		return err
	}
	_, err = oleutil.CallMethod(link, "Save")
	return err
}
