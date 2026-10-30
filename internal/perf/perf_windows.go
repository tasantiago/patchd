package perf

import (
	"context"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	procGetSystemTimes       = kernel32.NewProc("GetSystemTimes")
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
)

// memoryStatusEx é o MEMORYSTATUSEX do Windows.
type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

// readCPU: GetSystemTimes (o tempo de kernel já inclui o ocioso).
func readCPU(context.Context) (cpuTimes, error) {
	var idle, kernel, user windows.Filetime
	r, _, err := procGetSystemTimes.Call(uintptr(unsafe.Pointer(&idle)), uintptr(unsafe.Pointer(&kernel)), uintptr(unsafe.Pointer(&user)))
	if r == 0 {
		return cpuTimes{}, err
	}
	ft := func(f windows.Filetime) uint64 { return uint64(f.HighDateTime)<<32 | uint64(f.LowDateTime) }
	total := ft(kernel) + ft(user)
	i := ft(idle)
	if i > total {
		return cpuTimes{}, nil
	}
	return cpuTimes{busy: total - i, total: total}, nil
}

// readMem: GlobalMemoryStatusEx, pela memória física disponível.
func readMem(context.Context) (float64, error) {
	var m memoryStatusEx
	m.Length = uint32(unsafe.Sizeof(m))
	if r, _, err := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&m))); r == 0 {
		return 0, err
	}
	if m.TotalPhys == 0 || m.AvailPhys > m.TotalPhys {
		return 0, nil
	}
	return clamp(float64(m.TotalPhys-m.AvailPhys) * 100 / float64(m.TotalPhys)), nil
}
