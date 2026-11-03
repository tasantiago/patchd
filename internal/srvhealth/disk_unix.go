//go:build linux || darwin

package srvhealth

import (
	"golang.org/x/sys/unix"
)

func statDisk(p Path) Disk {
	d := Disk{Name: p.Name, Dir: p.Dir, FreePct: Unknown}
	var st unix.Statfs_t
	if err := unix.Statfs(p.Dir, &st); err != nil {
		d.Err = err.Error()
		return d
	}
	// #nosec G115 -- tamanho de bloco do statfs: nunca negativo
	unit := uint64(st.Bsize)
	d.Total, d.Free = st.Blocks*unit, st.Bavail*unit
	if d.Total == 0 {
		d.Err = "volume sem tamanho"
		return d
	}
	d.FreePct = 100 * float64(d.Free) / float64(d.Total)
	d.statOK = true
	var fi unix.Stat_t
	if err := unix.Stat(p.Dir, &fi); err == nil {
		d.device, d.deviceKnown = uint64(fi.Dev), true // #nosec G115 -- número do dispositivo
	}
	return d
}
