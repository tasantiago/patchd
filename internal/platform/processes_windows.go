package platform

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Processes lista os processos em execução, de todas as sessões, por um instantâneo do
// Toolhelp. A sessão vem de ProcessIdToSessionId; se o processo já saiu, fica a 0.
func Processes() ([]Process, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	var out []Process
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		p := Process{Name: windows.UTF16ToString(e.ExeFile[:]), PID: e.ProcessID}
		_ = windows.ProcessIdToSessionId(p.PID, &p.Session)
		out = append(out, p)
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return nil, err
	}
	return out, nil
}
