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
//   - que dizem qual é o dispositivo por baixo (probeVolume). O G: do Google Drive diz
//     DRIVE_FIXED, tem volume no gerenciador de montagem, responde com uma extensão e uma
//     partição, mas não sabe dizer o dispositivo. Medido nos labs das Aulas 12.1c e 12.1d.
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
		local, bus, model := probeVolume(letter)
		if !local {
			continue
		}
		var avail, total, totalFree uint64
		if err := windows.GetDiskFreeSpaceEx(root, &avail, &total, &totalFree); err != nil {
			// Ex.: volume BitLocker bloqueado.
			errs = append(errs, fmt.Errorf("%s %w", letter, err))
			continue
		}
		d := protocol.Disk{Mount: letter, TotalBytes: total, FreeBytes: avail, Bus: bus, Model: model}
		var fsName [windows.MAX_PATH + 1]uint16
		if err := windows.GetVolumeInformation(root, nil, 0, nil, nil, nil, &fsName[0], uint32(len(fsName))); err == nil {
			d.FS = windows.UTF16ToString(fsName[:])
		}
		out = append(out, d)
	}
	return out, errs
}

// Códigos de controle (CTL_CODE com FILE_ANY_ACCESS: valem num handle aberto sem direito
// nenhum, então nem o agente nem o -state precisam de administrador).
const (
	ioctlVolumeDiskExtents = 0x00560000 // IOCTL_VOLUME_GET_VOLUME_DISK_EXTENTS
	ioctlStorageQuery      = 0x002D1400 // IOCTL_STORAGE_QUERY_PROPERTY
)

// probeVolume faz duas perguntas ao volume da letra:
//  1. em que discos você está (as extensões): um volume de verdade tem pelo menos uma;
//  2. qual é o dispositivo (StorageDeviceProperty): barramento e modelo do disco.
//
// Um volume que responde às duas é local, e o barramento e o modelo vão para o painel. O
// drive virtual do Google Drive responde à primeira (repassando o disco onde fica o cache)
// e falha na segunda. Um volume estendido por vários discos também não responde à segunda,
// mas tem mais de uma extensão: fica.
//
// Acesso negado em qualquer pergunta: o volume sai (Aula 12.1f). O serviço roda como SYSTEM,
// que pergunta a qualquer volume da máquina; no lab, o único que negou foi o G: do Google
// Drive (que é da sessão do usuário). Um volume BitLocker bloqueado responde às duas
// perguntas e só falha na leitura do espaço, que vira erro em "errors".
func probeVolume(letter string) (local bool, bus, model string) {
	path, _ := windows.UTF16PtrFromString(`\\.\` + letter)
	h, err := windows.CreateFile(path, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return false, "", ""
	}
	defer windows.CloseHandle(h)

	// VOLUME_DISK_EXTENTS: o número de extensões (uint32, com alinhamento de 8) e, para
	// cada uma, DiskNumber, StartingOffset e ExtentLength (24 bytes). Cabem 8 extensões.
	var ext [1 + 3*8]uint64
	var n uint32
	spanned := false
	err = windows.DeviceIoControl(h, ioctlVolumeDiskExtents, nil, 0, (*byte)(unsafe.Pointer(&ext[0])), uint32(len(ext)*8), &n, nil)
	switch {
	case err == nil:
		// #nosec G115 -- NumberOfDiskExtents é um DWORD; os 4 bytes altos são alinhamento
		if c := uint32(ext[0]); c == 0 {
			return false, "", ""
		} else if c > 1 {
			spanned = true
		}
	case errors.Is(err, windows.ERROR_MORE_DATA): // mais de 8 discos
		spanned = true
	default:
		return false, "", ""
	}

	// STORAGE_PROPERTY_QUERY{PropertyId: StorageDeviceProperty (0), QueryType:
	// PropertyStandardQuery (0)}: 12 bytes zerados. 1 KiB de resposta, o mesmo tamanho
	// da sonda do lab (um NVMe e um SATA responderam com ele).
	var query [3]uint32
	var desc [128]uint64
	err = windows.DeviceIoControl(h, ioctlStorageQuery, (*byte)(unsafe.Pointer(&query[0])), uint32(len(query)*4),
		(*byte)(unsafe.Pointer(&desc[0])), uint32(len(desc)*8), &n, nil)
	if err != nil {
		return spanned, "", ""
	}
	b := unsafe.Slice((*byte)(unsafe.Pointer(&desc[0])), len(desc)*8)[:min(int(n), len(desc)*8)]
	return true, busName(le32(b, 28)), descString(b, int(le32(b, 16)))
}
