package hoststate

import (
	"context"
	"fmt"

	"golang.org/x/sys/windows"

	"github.com/tasantiago/patchd/internal/protocol"
)

// disks percorre as letras de unidade e fica com as fixas (DRIVE_FIXED): sem pendrive, CD
// e unidade de rede mapeada. Uma unidade de rede lenta travaria a chamada; e a letra
// mapeada é da sessão do usuário, não da máquina (o serviço roda como SYSTEM).
func disks(ctx context.Context) ([]protocol.Disk, []error) {
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return nil, []error{err}
	}
	var out []protocol.Disk
	var errs []error
	for i := range 26 {
		if mask&(1<<i) == 0 {
			continue
		}
		if ctx.Err() != nil {
			return out, append(errs, ctx.Err())
		}
		letter := string(rune('A'+i)) + ":"
		root, _ := windows.UTF16PtrFromString(letter + `\`)
		if windows.GetDriveType(root) != windows.DRIVE_FIXED {
			continue
		}
		var avail, total, totalFree uint64
		if err := windows.GetDiskFreeSpaceEx(root, &avail, &total, &totalFree); err != nil {
			// Ex.: volume BitLocker bloqueado.
			errs = append(errs, fmt.Errorf("%s %w", letter, err))
			continue
		}
		d := protocol.Disk{Mount: letter, TotalBytes: total, FreeBytes: avail}
		var fsName [windows.MAX_PATH + 1]uint16
		if err := windows.GetVolumeInformation(root, nil, 0, nil, nil, nil, &fsName[0], uint32(len(fsName))); err == nil {
			d.FS = windows.UTF16ToString(fsName[:])
		}
		out = append(out, d)
	}
	return out, errs
}
