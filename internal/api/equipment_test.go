package api_test

import (
	"context"
	"encoding/csv"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

// semeiaEquipamentos grava n máquinas na memória, uma delas com nome hostil e serial que
// uma planilha leria como fórmula.
func semeiaEquipamentos(t *testing.T, st *store.Memory, n int) {
	t.Helper()
	ctx := context.Background()
	for i := range n {
		id := fmt.Sprintf("e%03d-0000", i)
		rep := protocol.InventoryReport{SchemaVersion: 1, Hash: id, CollectedAt: time.Now().UTC(),
			OS:       protocol.OSInfo{Family: "windows", Name: "Windows 11 Pro", Version: "25H2", Hostname: fmt.Sprintf("pc-%03d", i)},
			Hardware: &protocol.Hardware{Manufacturer: "Dell Inc.", Model: "OptiPlex", ChassisType: "3", SerialNumber: fmt.Sprintf("SER%04d", i)}}
		if i == 0 {
			rep.OS.Hostname = `<script>alert(1)</script>`
			rep.Hardware.SerialNumber = "=HYPERLINK(1)"
		}
		if _, err := st.SaveInventory(ctx, id, rep); err != nil {
			t.Fatal(err)
		}
		if i%2 == 0 {
			_ = st.SaveHostState(ctx, id, &protocol.MachineState{CollectedAt: time.Now().UTC(),
				Interfaces: []protocol.NetInterface{{Name: "eth0", Up: true, IPv4: []string{fmt.Sprintf("192.0.2.%d/24", i+1)}}}}, "203.0.113.1")
		}
	}
}

func TestPaginaDeEquipamentos(t *testing.T) {
	h, st, _ := ambientePaginas(t)
	semeiaEquipamentos(t, st, 30)
	b := pagina(h, "GET", "/painel/inventario", sessaoTeste, nil).Body.String()
	// A memória do ambiente já tem 2 máquinas: 32 no total, 25 por página.
	for _, tr := range []string{
		`<span class="kpi-rot">Equipamentos</span><span class="kpi-val">32</span>`,
		`<span class="kpi-rot">Sem rede e discos</span><span class="kpi-val">17</span>`,
		"1–25 de 32", `href="/painel/inventario?pagina=2"`, "próxima ›",
		`aria-sort="ascending"><a href="/painel/inventario?dir=desc">Nome ▲</a>`,
		`<a href="/painel/inventario?ordem=ip">IP</a>`,
		"&lt;script&gt;alert(1)&lt;/script&gt;", "<td>Desktop</td>", "192.0.2.3",
		`class="ativo" aria-current="page"><span class="ic">🗂️</span>Inventário</a>`,
	} {
		if !strings.Contains(b, tr) {
			t.Errorf("faltou %q", tr)
		}
	}
	if strings.Contains(b, "<script>alert") {
		t.Error("o nome tem de sair escapado")
	}

	// Busca no campo IP, ordem decrescente, 15 por página.
	b = pagina(h, "GET", "/painel/inventario?campo=ip&q=192.0.2.1&ordem=ip&dir=desc&por=15", sessaoTeste, nil).Body.String()
	// 192.0.2.1, .11, .13, .15, .17, .19 (máquinas pares 0, 10, 12...): o "1" casa em qualquer parte.
	if !strings.Contains(b, "1–6 de 6") || strings.Index(b, "192.0.2.19") > strings.Index(b, "192.0.2.11") {
		t.Errorf("busca por IP em ordem decrescente:\n%s", b)
	}
	if !strings.Contains(b, `<option value="ip" selected>IP</option>`) || !strings.Contains(b, `<option value="15" selected>15</option>`) {
		t.Error("o formulário mantém o campo e o tamanho da página")
	}
	// Página além da última: mostra a última.
	if b := pagina(h, "GET", "/painel/inventario?pagina=99", sessaoTeste, nil).Body.String(); !strings.Contains(b, "26–32 de 32") {
		t.Error("página além da última")
	}
	if b := pagina(h, "GET", "/painel/inventario?q=nada-assim", sessaoTeste, nil).Body.String(); !strings.Contains(b, "Nenhum equipamento com esse filtro") {
		t.Error("busca sem resultado")
	}
	if r := pagina(h, "GET", "/painel/inventario", "", nil); r.Code != 303 {
		t.Errorf("sem sessão: %d", r.Code)
	}
}

func TestCSVDeEquipamentos(t *testing.T) {
	h, st, log := ambientePaginas(t)
	semeiaEquipamentos(t, st, 4)
	r := pagina(h, "GET", "/painel/inventario.csv?campo=serial&q=ser", sessaoTeste, nil)
	if r.Code != 200 || !strings.HasPrefix(r.Header().Get("Content-Type"), "text/csv") || !strings.HasPrefix(r.Body.String(), "\xef\xbb\xbf") {
		t.Fatalf("CSV: %d %s", r.Code, r.Header().Get("Content-Type"))
	}
	cr := csv.NewReader(strings.NewReader(strings.TrimPrefix(r.Body.String(), "\xef\xbb\xbf")))
	cr.Comma = ';'
	rows, err := cr.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	// Cabeçalho + as 3 com serial "SER..." (a e000 tem o serial-fórmula, que não casa).
	if len(rows) != 4 || rows[0][0] != "maquina" || rows[1][1] != "pc-001" || rows[1][13] != "SER0001" {
		t.Fatalf("linhas: %v", rows)
	}
	all := pagina(h, "GET", "/painel/inventario.csv", sessaoTeste, nil).Body.String()
	if !strings.Contains(all, ";'=HYPERLINK(1);") {
		t.Error("o serial com cara de fórmula sai com apóstrofo")
	}
	if !strings.Contains(log.String(), "exportação CSV") {
		t.Error("a exportação fica no log, com quem exportou")
	}
}
