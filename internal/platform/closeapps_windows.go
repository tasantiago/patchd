package platform

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Fechar o programa com aviso (job de terceiros com close): as operações do Windows. O
// serviço roda como SYSTEM na sessão 0 e não alcança as janelas das sessões dos usuários;
// o aviso vai pelo WTSSendMessage, e o pedido de fechar, por um taskkill (sem /F) iniciado
// dentro da sessão com o token do próprio usuário: é o WM_CLOSE, como o X da janela.

var (
	wtsapi32           = windows.NewLazySystemDLL("wtsapi32.dll")
	procWTSSendMessage = wtsapi32.NewProc("WTSSendMessageW")
)

const (
	wtsActive = 0 // WTS_CONNECTSTATE_CLASS.WTSActive

	mbOK          = 0x00000000
	mbIconWarning = 0x00000030
	mbTopMost     = 0x00040000
	mbSetFore     = 0x00010000
)

// Sessions: as sessões com usuário (menos a 0, dos serviços) e se cada uma está ativa.
func Sessions() (map[uint32]bool, error) {
	var list *windows.WTS_SESSION_INFO
	var n uint32
	if err := windows.WTSEnumerateSessions(0, 0, 1, &list, &n); err != nil {
		return nil, fmt.Errorf("WTSEnumerateSessions: %w", err)
	}
	defer windows.WTSFreeMemory(uintptr(unsafe.Pointer(list)))
	out := map[uint32]bool{}
	for _, s := range unsafe.Slice(list, n) {
		if s.SessionID != 0 {
			out[s.SessionID] = s.State == wtsActive
		}
	}
	return out, nil
}

// NotifySession mostra o aviso na sessão, sem esperar a resposta (some depois de timeout).
func NotifySession(session uint32, title, text string, timeout time.Duration) error {
	t, err := windows.UTF16FromString(title)
	if err != nil {
		return err
	}
	m, err := windows.UTF16FromString(text)
	if err != nil {
		return err
	}
	var resp uint32
	r, _, callErr := procWTSSendMessage.Call(0, uintptr(session),
		uintptr(unsafe.Pointer(&t[0])), uintptr((len(t)-1)*2),
		uintptr(unsafe.Pointer(&m[0])), uintptr((len(m)-1)*2),
		mbOK|mbIconWarning|mbTopMost|mbSetFore, uintptr(timeout/time.Second), uintptr(unsafe.Pointer(&resp)), 0)
	if r == 0 {
		return fmt.Errorf("WTSSendMessage na sessão %d: %w", session, callErr)
	}
	return nil
}

func system32(name string) string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32", name)
}

// AskClose pede para os processos fecharem: um taskkill sem /F, dentro da sessão, como o
// usuário dela (só números na linha de comando). Espera o taskkill terminar (até 30 s).
func AskClose(session uint32, pids []uint32) error {
	if len(pids) == 0 {
		return nil
	}
	var tok windows.Token
	if err := windows.WTSQueryUserToken(session, &tok); err != nil {
		return fmt.Errorf("WTSQueryUserToken da sessão %d: %w", session, err)
	}
	defer tok.Close()
	exe := system32("taskkill.exe")
	parts := []string{`"` + exe + `"`}
	for _, pid := range pids {
		parts = append(parts, "/PID", strconv.FormatUint(uint64(pid), 10))
	}
	app, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return err
	}
	cmd, err := windows.UTF16PtrFromString(strings.Join(parts, " "))
	if err != nil {
		return err
	}
	desk, err := windows.UTF16PtrFromString(`winsta0\default`)
	if err != nil {
		return err
	}
	si := windows.StartupInfo{Desktop: desk, Flags: windows.STARTF_USESHOWWINDOW, ShowWindow: windows.SW_HIDE}
	si.Cb = uint32(unsafe.Sizeof(si))
	var pi windows.ProcessInformation
	if err := windows.CreateProcessAsUser(tok, app, cmd, nil, nil, false, windows.CREATE_NO_WINDOW, nil, nil, &si, &pi); err != nil {
		return fmt.Errorf("taskkill na sessão %d: %w", session, err)
	}
	defer windows.CloseHandle(pi.Process)
	defer windows.CloseHandle(pi.Thread)
	if ev, err := windows.WaitForSingleObject(pi.Process, 30_000); err != nil || ev != windows.WAIT_OBJECT_0 {
		return fmt.Errorf("taskkill na sessão %d não terminou", session)
	}
	return nil
}

// KillProcess fecha o processo à força, se ele ainda for o executável name (o PID pode ter
// sido reaproveitado entre a lista e aqui).
func KillProcess(pid uint32, name string) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, 1024)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return err
	}
	if !strings.EqualFold(filepath.Base(windows.UTF16ToString(buf[:n])), name) {
		return errors.New("o PID agora é de outro programa")
	}
	return windows.TerminateProcess(h, 1)
}
