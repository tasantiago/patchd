package api

import (
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

func TestCategoria(t *testing.T) {
	casos := []struct{ fab, modelo, chassi, quer string }{
		{"Dell Inc.", "OptiPlex 7010", "3", "Desktop"},
		{"LENOVO", "20XW", "10", "Notebook"},
		{"HP", "EliteBook", "31", "Notebook"}, // conversível
		{"Dell Inc.", "PowerEdge R650", "23", "Servidor"},
		{"Microsoft Corporation", "Surface Go", "30", "Tablet"},
		{"VMware, Inc.", "VMware20,1", "1", "Virtual"}, // chassi "outro": o fabricante decide
		{"Microsoft Corporation", "Virtual Machine", "3", "Virtual"},
		{"QEMU", "Standard PC (Q35 + ICH9, 2009)", "1", "Virtual"},
		{"innotek GmbH", "VirtualBox", "1", "Virtual"},
		{"Apple Inc.", "MacBookPro18,1", "", "Notebook"},
		{"Apple Inc.", "Macmini9,1", "", "Desktop"},
		{"Apple Inc.", "Mac14,2", "", ""}, // pode ser os dois: sem palpite
		{"ACME", "X", "2", ""},
		{"ACME", "X", "", ""},
		{"ACME", "X", "12", "Outro"}, // docking station
	}
	for _, c := range casos {
		if got := category(c.fab, c.modelo, c.chassi); got != c.quer {
			t.Errorf("%s / %s / %q: %q, quer %q", c.fab, c.modelo, c.chassi, got, c.quer)
		}
	}
}

// frotaDeTeste: 7 máquinas com nomes, IPs, contatos e versões variados. Endereços de
// documentação (RFC 5737).
func frotaDeTeste(agora time.Time) []equip {
	var rows []store.InventoryRow
	for i := range 7 {
		r := store.InventoryRow{ID: fmt.Sprintf("m-%d", i), Hostname: fmt.Sprintf("pc-%02d", 7-i), OSName: "Ubuntu", OSVersion: "26.04",
			AgentVersion: fmt.Sprintf("v0.%d.0", 9+i), LastSeenAt: agora.Add(-time.Duration(i) * time.Hour), Ring: i % 2,
			Serial: "SN" + fmt.Sprint(1000+i)}
		if i%3 != 0 {
			r.State = &protocol.MachineState{Interfaces: []protocol.NetInterface{{Name: "eth0", Up: true, IPv4: []string{fmt.Sprintf("192.0.2.%d/24", 100-i*10)}}}}
		}
		rows = append(rows, r)
	}
	rows[0].Serial = "To be filled by O.E.M."
	rows[3].CheckinAddr = "198.51.100.3" // sem estado: o IP é o do check-in
	return buildEquip(rows, agora)
}

func TestFiltroOrdemEPaginas(t *testing.T) {
	agora := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	frota := frotaDeTeste(agora)
	if frota[0].Serial != "" || frota[1].Serial != "SN1001" {
		t.Errorf("serial genérico vira vazio: %q %q", frota[0].Serial, frota[1].Serial)
	}
	if !frota[3].IPFromCheckin || frota[3].IP != "198.51.100.3" || frota[1].IPFromCheckin || frota[1].IP != "192.0.2.90" {
		t.Errorf("IP: %+v %+v", frota[3], frota[1])
	}
	// Online: até 2 h sem check-in (máquinas 0, 1 e 2).
	if n := len(filterEquip(frota, equipQuery{Status: statusOnline})); n != 3 {
		t.Errorf("online: %d", n)
	}
	if n := len(filterEquip(frota, equipQuery{Status: statusNoState})); n != 3 {
		t.Errorf("sem estado (0, 3 e 6): %d", n)
	}
	// Busca sem diferença de maiúsculas, no campo pedido ou em todos.
	if l := filterEquip(frota, equipQuery{Field: "nome", Q: "PC-07"}); len(l) != 1 || l[0].ID != "m-0" {
		t.Errorf("nome: %+v", l)
	}
	if l := filterEquip(frota, equipQuery{Field: "ip", Q: "192.0.2.9"}); len(l) != 1 {
		t.Errorf("ip: %+v", l)
	}
	if l := filterEquip(frota, equipQuery{Field: "serial", Q: "pc-"}); len(l) != 0 {
		t.Error("o campo restringe a busca")
	}
	if l := filterEquip(frota, equipQuery{Field: "todos", Q: "sn1005"}); len(l) != 1 || l[0].ID != "m-5" {
		t.Errorf("todos: %+v", l)
	}

	ids := func(l []equip) string {
		s := ""
		for _, e := range l {
			s += e.ID[2:]
		}
		return s
	}
	l := append([]equip(nil), frota...)
	sortEquip(l, "nome", false)
	if ids(l) != "6543210" {
		t.Errorf("nome: %s", ids(l))
	}
	// IP em ordem numérica; quem não tem IP vai para o fim nas duas direções.
	sortEquip(l, "ip", false)
	if ids(l) != "5421360" && ids(l) != "5421306" {
		t.Errorf("ip crescente: %s", ids(l))
	}
	sortEquip(l, "ip", true)
	if got := ids(l); got[:5] != "31245" {
		t.Errorf("ip decrescente: %s", got)
	}
	// Versão do agente como versão (v0.15 depois de v0.9), não como texto.
	sortEquip(l, "agente", false)
	if ids(l) != "0123456" {
		t.Errorf("agente: %s", ids(l))
	}

	// Parâmetros fora da lista voltam ao padrão; a URL canônica omite os padrões.
	q := parseEquipQuery(url.Values{"campo": {"<x>"}, "ordem": {"senha"}, "por": {"999"}, "pagina": {"-3"}, "status": {"x"}})
	if q.Field != "todos" || q.Order != "nome" || q.Per != defaultPerPage || q.Page != 1 || q.Status != "" {
		t.Errorf("padrões: %+v", q)
	}
	if h := q.href("/painel/inventario"); h != "/painel/inventario" {
		t.Errorf("sem parâmetros: %s", h)
	}
	q = parseEquipQuery(url.Values{"campo": {"ip"}, "q": {"192.0.2"}, "ordem": {"ip"}, "dir": {"desc"}, "por": {"15"}, "pagina": {"2"}})
	if h := q.href("/painel/inventario"); h != "/painel/inventario?campo=ip&dir=desc&ordem=ip&pagina=2&por=15&q=192.0.2" {
		t.Errorf("canônica: %s", h)
	}

	// Páginas: primeira, última e duas de cada lado da atual.
	p := pageLinks(equipQuery{Field: "todos", Order: "nome", Per: 15, Page: 10}, 20)
	got := ""
	for _, x := range p {
		if x.Gap {
			got += "… "
		} else {
			got += fmt.Sprint(x.N, " ")
		}
	}
	if got != "1 … 8 9 10 11 12 … 20 " {
		t.Errorf("páginas: %q", got)
	}
}
