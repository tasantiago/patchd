package hoststate

import (
	"context"
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/tasantiago/patchd/internal/protocol"
)

// disks percorre as letras de unidade e fica com os volumes locais de verdade:
//   - DRIVE_FIXED: sem pendrive, CD e unidade de rede mapeada (uma unidade de rede lenta
//     travaria a chamada, e a letra mapeada é da sessão do usuário, não da máquina: o
//     serviço roda como SYSTEM);
//   - com partição de disco por baixo (o critério do Get-Volume): o G: do Google Drive
//     diz DRIVE_FIXED, tem volume no gerenciador de montagem (aparece no mountvol e no
//     Win32_Volume), mas nenhuma partição. Medido no lab da Aula 12.1c.
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
		if windows.GetDriveType(root) != windows.DRIVE_FIXED || !onDisk(letter) {
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

// IOCTL_VOLUME_GET_VOLUME_DISK_EXTENTS: CTL_CODE(IOCTL_VOLUME_BASE 0x56, 0, METHOD_BUFFERED,
// FILE_ANY_ACCESS). Por ser FILE_ANY_ACCESS, vale num handle aberto sem direito nenhum.
const ioctlVolumeDiskExtents = 0x00560000

// onDisk diz se o volume da letra fica em partição de disco: pergunta ao volume em que
// discos ele está (as "extensões"). Um volume de verdade responde com uma ou mais; um
// drive virtual não sabe responder. Acesso negado conta como disco: na dúvida, o volume
// fica (sumir com o C: de uma máquina seria pior que mostrar um drive a mais).
func onDisk(letter string) bool {
	path, _ := windows.UTF16PtrFromString(`\\.\` + letter)
	h, err := windows.CreateFile(path, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return errors.Is(err, windows.ERROR_ACCESS_DENIED)
	}
	defer windows.CloseHandle(h)
	// VOLUME_DISK_EXTENTS: o número de extensões (uint32, com alinhamento de 8) e, para
	// cada uma, DiskNumber, StartingOffset e ExtentLength (24 bytes). Cabem 8 extensões.
	var buf [1 + 3*8]uint64
	var n uint32
	err = windows.DeviceIoControl(h, ioctlVolumeDiskExtents, nil, 0, (*byte)(unsafe.Pointer(&buf[0])), uint32(len(buf)*8), &n, nil)
	switch {
	case err == nil:
		return uint32(buf[0]) > 0
	case errors.Is(err, windows.ERROR_MORE_DATA): // mais de 8 discos (volume estendido)
		return true
	case errors.Is(err, windows.ERROR_ACCESS_DENIED):
		return true
	}
	return false
}
