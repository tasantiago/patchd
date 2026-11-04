//go:build !linux && !darwin

package srvhealth

func statDisk(p Path) Disk {
	return Disk{Name: p.Name, Dir: p.Dir, FreePct: Unknown, Err: "não medido neste sistema"}
}
