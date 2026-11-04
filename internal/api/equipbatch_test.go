package api_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

// Selecionar por faixa de IP ou prédio: o Inventário filtra pela faixa e pelo prédio do
// patrimônio, e o admin põe todas as encontradas num grupo de implantação de uma vez. O
// servidor refaz o filtro e só move se a contagem bate; sem filtro, não move nada.
func TestGrupoPorFaixaOuPredio(t *testing.T) {
	h, st, _ := ambientePaginas(t)
	ctx := context.Background()
	maq := map[string][2]string{"pc-1001": {"10.1.2.10/24", ""}, "pc-1002": {"10.1.2.60/24", ""}, "pc-1003": {"10.9.0.5/16", ""}, "pc-1004": {"10.1.2.200/24", ""}}
	for nome, ip := range maq {
		if _, err := st.SaveInventory(ctx, nome, protocol.InventoryReport{SchemaVersion: 1, Hash: "h-" + nome, OS: protocol.OSInfo{Hostname: nome}}); err != nil {
			t.Fatal(err)
		}
		s := &protocol.MachineState{Interfaces: []protocol.NetInterface{{Name: "eth0", MAC: "00:11:22:33:44:55", IPv4: []string{ip[0]}, Up: true}}}
		if err := st.SaveHostState(ctx, nome, s, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.ReplaceAssetLocations(ctx, []store.AssetLocation{
		{Tombo: "1001", Building: "Bloco A"}, {Tombo: "1002", Building: "Bloco A"}, {Tombo: "1003", Building: "Bloco B"},
	}, store.AssetImport{By: "t", File: "p.csv"}); err != nil {
		t.Fatal(err)
	}
	lista := func(q string) string {
		t.Helper()
		return pagina(h, "GET", "/painel/inventario?"+q, sessaoAdmin, nil).Body.String()
	}
	tem := func(b string, nomes ...string) string {
		var out []string
		for _, n := range []string{"pc-1001", "pc-1002", "pc-1003", "pc-1004"} {
			if strings.Contains(b, ">"+n+"<") {
				out = append(out, n)
			}
		}
		return strings.Join(out, ",")
	}
	for q, quer := range map[string]string{
		"campo=ip&q=" + url.QueryEscape("10.1.2.0/24"):                                            "pc-1001,pc-1002,pc-1004",
		"campo=ip&q=" + url.QueryEscape("10.1.2.1-100"):                                           "pc-1001,pc-1002",
		"q=" + url.QueryEscape("10.1.2.50-10.1.2.250"):                                            "pc-1002,pc-1004",
		"campo=ip&q=" + url.QueryEscape("10.9.*"):                                                 "pc-1003",
		"predio=" + url.QueryEscape("Bloco A"):                                                    "pc-1001,pc-1002",
		"predio=" + url.QueryEscape("Bloco A") + "&campo=ip&q=" + url.QueryEscape("10.1.2.50-99"): "pc-1002",
	} {
		if got := tem(lista(q)); got != quer {
			t.Errorf("%s: %s, quero %s", q, got, quer)
		}
	}
	b := lista("campo=ip&q=" + url.QueryEscape("10.1.2.0/24"))
	if !strings.Contains(b, "Mostrando a faixa de IP de 10.1.2.0 a 10.1.2.255") || !strings.Contains(b, "Pôr as 3 máquinas encontradas no grupo de implantação") {
		t.Fatalf("a página não mostra a faixa e o lote:\n%.3000s", b)
	}
	if b := lista(""); strings.Contains(b, `action="/painel/inventario/grupo"`) {
		t.Error("sem filtro, sem lote")
	}
	if b := pagina(h, "GET", "/painel/inventario?predio=Bloco+A", sessaoTeste, nil).Body.String(); strings.Contains(b, `action="/painel/inventario/grupo"`) {
		t.Error("leitura vê o lote")
	}

	move := func(cookie string, f url.Values) *http.Response {
		t.Helper()
		return pagina(h, "POST", "/painel/inventario/grupo", cookie, f).Result()
	}
	f := url.Values{"campo": {"ip"}, "q": {"10.1.2.0/24"}, "n": {"3"}, "anel": {"2"}}
	if r := move(sessaoTeste, f); r.StatusCode != http.StatusForbidden {
		t.Fatalf("leitura move: %d", r.StatusCode)
	}
	// A contagem não bate (a página mostrava 2): nada muda.
	ruim := url.Values{"campo": {"ip"}, "q": {"10.1.2.0/24"}, "n": {"2"}, "anel": {"2"}}
	if loc := move(sessaoAdmin, ruim).Header.Get("Location"); !strings.Contains(loc, "erro=lote-mudou") || !strings.Contains(loc, "agora=3") {
		t.Fatalf("contagem errada: %s", loc)
	}
	// Sem filtro: recusa.
	if loc := move(sessaoAdmin, url.Values{"n": {"6"}, "anel": {"2"}}).Header.Get("Location"); !strings.Contains(loc, "erro=lote-filtro") {
		t.Fatalf("sem filtro: %s", loc)
	}
	ring := func(id string) int {
		r, _, _ := st.MachineRing(ctx, id)
		return r
	}
	if ring("pc-1001") != 1 {
		t.Fatal("moveu antes da hora")
	}
	r := move(sessaoAdmin, f)
	loc := r.Header.Get("Location")
	if !strings.Contains(loc, "movidas=3") || !strings.Contains(loc, "/painel/inventario?") || !strings.Contains(loc, "q=10.1.2.0") {
		t.Fatalf("lote: %d %s", r.StatusCode, loc)
	}
	if ring("pc-1001") != 2 || ring("pc-1002") != 2 || ring("pc-1004") != 2 || ring("pc-1003") != 1 {
		t.Fatalf("grupos: %d %d %d %d", ring("pc-1001"), ring("pc-1002"), ring("pc-1004"), ring("pc-1003"))
	}
	if b := pagina(h, "GET", loc, sessaoAdmin, nil).Body.String(); !strings.Contains(b, "3 máquina(s) no grupo de implantação 2.") {
		t.Errorf("aviso da volta:\n%.2000s", b)
	}
	// De novo, pelo prédio: as do Bloco A já estão no 2.
	loc = move(sessaoAdmin, url.Values{"predio": {"Bloco A"}, "n": {"2"}, "anel": {"2"}}).Header.Get("Location")
	if !strings.Contains(loc, "movidas=0") || !strings.Contains(loc, "ja=2") {
		t.Fatalf("prédio: %s", loc)
	}
	if b := pagina(h, "GET", loc, sessaoAdmin, nil).Body.String(); !strings.Contains(b, "2 já estava(m) nesse grupo.") {
		t.Error("aviso das que já estavam")
	}
	ev, _ := st.AuditEvents(ctx, store.AuditFilter{Limit: 10})
	achou := false
	for _, e := range ev {
		if e.Action == "maquina.anel_lote" && strings.Contains(e.Detail, "IP de 10.1.2.0 a 10.1.2.255") {
			achou = true
		}
	}
	if !achou {
		t.Errorf("auditoria sem o filtro: %+v", ev)
	}
}
