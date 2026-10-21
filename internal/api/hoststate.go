package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/tasantiago/patchd/internal/machinebox"
	"github.com/tasantiago/patchd/internal/protocol"
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
	// Usuários conectados (Aula 15.6). UsersKnown: o agente coleta (mesmo que ninguém esteja).
	UsersKnown bool
	Users      []userRow
	Active     int // sessões ativas (alguém na tela): o reinício "agora" pede confirmação (Aula 15.12)
	// Aula 20.5b: a lista de organizações que o agente aceita nos instaladores de terceiros.
	// SignersKnown: o agente informa (Windows, v0.17.8 ou mais nova).
	SignersKnown bool
	Signers      string
	// BoxKeyID (Aula 21.3): a impressão da chave da caixa da máquina; vazio = o agente não
	// informa (anterior à 21.3) e não pode receber segredos (a senha da BIOS, 21.4).
	BoxKeyID string
}

// userRow: uma sessão de usuário na máquina.
type userRow struct {
	Name, Account, Session string
	Active                 bool
}

// sessionLabels: o tipo de sessão como o suporte fala.
var sessionLabels = map[string]string{
	protocol.SessionConsole:  "console",
	protocol.SessionRDP:      "Área de Trabalho Remota",
	protocol.SessionSSH:      "SSH",
	protocol.SessionTerminal: "terminal",
}

// usersSummary: todos os usuários numa linha, para a dica da lista e para o CSV:
// "Fulano de Tal (EXEMPLO\fulano, console); suporte (SSH, desconectada)".
func usersSummary(us []protocol.LoggedUser) string {
	parts := make([]string, 0, len(us))
	for _, u := range us {
		detail := []string{}
		if u.DisplayName != "" {
			detail = append(detail, u.Account)
		}
		detail = append(detail, sessionLabels[u.Session])
		if !u.Active {
			detail = append(detail, "desconectada")
		}
		parts = append(parts, userLabel(u)+" ("+strings.Join(detail, ", ")+")")
	}
	return strings.Join(parts, "; ")
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
		row.Pct = usedPct(d.TotalBytes, d.FreeBytes)
		switch {
		case row.Pct >= diskFull:
			row.Level = "cheio"
		case row.Pct >= diskHigh:
			row.Level = "alto"
		}
		hv.Disks = append(hv.Disks, row)
	}
	hv.Errors = s.Errors
	hv.UsersKnown = s.Users != nil
	for _, u := range s.Users {
		hv.Users = append(hv.Users, userRow{Name: u.DisplayName, Account: u.Account, Session: sessionLabels[u.Session], Active: u.Active})
	}
	hv.Active = activeSessions(s.Users)
	hv.SignersKnown, hv.Signers = s.AllowedSigners != nil, strings.Join(s.AllowedSigners, ", ")
	if pub, err := machinebox.ParsePublic(s.BoxKey); err == nil {
		hv.BoxKeyID = machinebox.KeyID(pub)
	}
	return nil
}

// usedPct: a ocupação de 0 a 100. Os números vêm do agente: livre maior que o total dava
// um número negativo enorme, e (total-livre)*100 estoura o uint64 acima de 184 PB (Aula 20.2,
// gosec G115). Em float64 a conta fica entre 0 e 100.
func usedPct(total, free uint64) int {
	if total == 0 || free >= total {
		return 0
	}
	return int(float64(total-free) * 100 / float64(total))
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

// activeSessions: quantas sessões têm alguém na tela agora. A desconectada não conta: quem
// está nela não vê o aviso de reinício, mas também não está trabalhando nela agora.
func activeSessions(us []protocol.LoggedUser) int {
	n := 0
	for _, u := range us {
		if u.Active {
			n++
		}
	}
	return n
}
