package hoststate

import (
	"context"

	"golang.org/x/sys/unix"

	"github.com/tasantiago/patchd/internal/protocol"
)

// disks lista os volumes locais com getfsstat. MNT_NOWAIT devolve o que o kernel já sabe,
// sem perguntar a cada sistema de arquivos (um volume de rede travado não trava a coleta).
//
// No APFS, o sistema e os dados são volumes do mesmo contêiner: "/" mostra o total e o livre
// do contêiner. Os volumes internos (Data, Preboot, VM, Update) vêm marcados MNT_DONTBROWSE
// e ficam de fora; os discos externos aparecem.
func disks(ctx context.Context) ([]protocol.Disk, []error) {
	n, err := unix.Getfsstat(nil, unix.MNT_NOWAIT)
	if err != nil {
		return nil, []error{err}
	}
	buf := make([]unix.Statfs_t, n+4) // folga: um volume pode montar entre as duas chamadas
	n, err = unix.Getfsstat(buf, unix.MNT_NOWAIT)
	if err != nil {
		return nil, []error{err}
	}
	var out []protocol.Disk
	for _, st := range buf[:n] {
		fs := unix.ByteSliceToString(st.Fstypename[:])
		if st.Flags&unix.MNT_LOCAL == 0 || st.Flags&unix.MNT_DONTBROWSE != 0 || fs == "devfs" || fs == "autofs" {
			continue
		}
		d := protocol.Disk{
			Mount:      unix.ByteSliceToString(st.Mntonname[:]),
			FS:         fs,
			Device:     unix.ByteSliceToString(st.Mntfromname[:]),
			TotalBytes: st.Blocks * uint64(st.Bsize),
			FreeBytes:  st.Bavail * uint64(st.Bsize),
		}
		if d.TotalBytes > 0 {
			out = append(out, d)
		}
	}
	return dedupe(out), nil
}
