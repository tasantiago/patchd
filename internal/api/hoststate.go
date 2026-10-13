package api

import (
	"fmt"
	"net/http"
	"strings"
)

// Rede e discos no detalhe da máquina (Aula 12.1).

// hostView é o cartão "Rede e discos".
type hostView struct {
	Has      bool   // o agente já mandou o estado
	Age      string // "há 12 min (13/10/2026 02:01 UTC)"
	Addr     string // de onde veio o último check-in
	AddrNote string
	Primary  string // o IPv4 que representa a máquina
	Ifs      []ifRow
	Disks    []diskRow
	Errors   []string
}

type ifRow struct {
	Name, MAC, IPv4, IPv6 string
	Up, LocalMAC          bool
}

type diskRow struct {
	Mount, FS, Device, Total, Free string
	Model, Bus                     string
	Pct                            int    // ocupado, de 0 a 100
	Level                          string // "", "alto" (≥ 80 %), "cheio" (≥ 90 %)
}

// Níveis de ocupação do disco: os mesmos dos alertas comuns de monitoração.
const (
	diskHigh = 80
	diskFull = 90
)

func (a *api) hostView(r *http.Request, id string, v *machineView) error {
	h, found, err := a.store.HostState(r.Context(), id)
	if err != nil || !found {
		return err
	}
	hv := &v.Host
	hv.Addr = dash(h.CheckinAddr)
	if h.State == nil {
		return nil
	}
	s := *h.State
	hv.Has = true
	hv.Age = ago(a.now().Sub(h.ReceivedAt)) + " (" + h.ReceivedAt.Format("02/01/2006 15:04") + " UTC)"
	hv.Primary = dash(s.PrimaryIPv4(h.CheckinAddr))
	switch {
	case h.CheckinAddr == "":
	case s.HasAddr(h.CheckinAddr):
		hv.AddrNote = "endereço de uma interface da máquina"
	default:
		// O servidor viu outro endereço: NAT, proxy ou VPN entre a máquina e ele.
		hv.AddrNote = "não é de nenhuma interface da máquina: há NAT, proxy ou VPN no caminho"
	}
	for _, n := range s.Interfaces {
		hv.Ifs = append(hv.Ifs, ifRow{Name: n.Name, MAC: dash(n.MAC), IPv4: dash(strings.Join(n.IPv4, ", ")),
			IPv6: dash(strings.Join(n.IPv6, ", ")), Up: n.Up, LocalMAC: n.LocalMAC})
	}
	for _, d := range s.Disks {
		row := diskRow{Mount: d.Mount, FS: dash(d.FS), Device: d.Device, Model: d.Model, Bus: d.Bus, Total: bytesLabel(d.TotalBytes), Free: bytesLabel(d.FreeBytes)}
		if d.TotalBytes > 0 {
			row.Pct = int((d.TotalBytes - d.FreeBytes) * 100 / d.TotalBytes)
		}
		switch {
		case row.Pct >= diskFull:
			row.Level = "cheio"
		case row.Pct >= diskHigh:
			row.Level = "alto"
		}
		hv.Disks = append(hv.Disks, row)
	}
	hv.Errors = s.Errors
	return nil
}

// bytesLabel: 1536 → "1,5 KiB", em potências de 1024, como o df -h e o Explorador do
// Windows (que escreve "GB", mas conta em GiB).
func bytesLabel(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit && exp < 4; m /= unit {
		div *= unit
		exp++
	}
	s := fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTP"[exp])
	return strings.Replace(s, ".", ",", 1)
}
