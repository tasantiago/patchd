package api_test

import (
	"compress/gzip"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

// A busca rápida do topo: um resultado só abre a máquina; vários ou nenhum caem na lista
// de equipamentos já filtrada. O nome inteiro vence o pedaço, e o começo do ID também acha.
func TestBuscaRapida(t *testing.T) {
	h, st, _ := ambientePaginas(t)
	semeiaEquipamentos(t, st, 12)
	for busca, destino := range map[string]string{
		"":                 "/painel/inventario",
		"   ":              "/painel/inventario",
		"pc-007":           "/painel/maquinas/e007-0000",
		"PC-007":           "/painel/maquinas/e007-0000",
		"SER0003":          "/painel/maquinas/e003-0000",
		"192.0.2.11":       "/painel/maquinas/e010-0000",
		"e005":             "/painel/maquinas/e005-0000",
		"pc-0":             "/painel/inventario?q=pc-0",
		"nada assim":       "/painel/inventario?q=nada+assim",
		"e0":               "/painel/inventario?q=e0",
		"../../etc/passwd": "/painel/inventario?q=..%2F..%2Fetc%2Fpasswd",
	} {
		r := pagina(h, "GET", "/painel/buscar?q="+strings.ReplaceAll(busca, " ", "+"), sessaoTeste, nil)
		if got := r.Header().Get("Location"); r.Code != 302 || got != destino {
			t.Errorf("busca %q: %d para %q, esperava %q", busca, r.Code, got, destino)
		}
	}
	// Sem sessão, a busca não responde nada do parque: vai para o login.
	if r := pagina(h, "GET", "/painel/buscar?q=pc-007", "", nil); r.Code != 303 || strings.Contains(r.Header().Get("Location"), "maquinas") {
		t.Errorf("sem sessão: %d para %q", r.Code, r.Header().Get("Location"))
	}
}

// A trilha da barra superior: a página de entrada mostra só o nome (com o grupo do menu,
// quando há); a tela de dentro ganha o link de volta; o Patch distingue as três telas.
func TestTrilhaDaBarraSuperior(t *testing.T) {
	h, st, _ := ambientePaginas(t)
	semeiaEquipamentos(t, st, 2)
	for path, trilha := range map[string]string{
		"/painel/parque":                 `<nav class="trilha" aria-label="onde você está"><span class="titulo">Parque</span></nav>`,
		"/painel/patch":                  `<span class="secao">Patch</span><span class="sep" aria-hidden="true">›</span><span class="titulo">Sistema operacional</span>`,
		"/painel/patch?aba=geral":        `<span class="secao">Patch</span><span class="sep" aria-hidden="true">›</span><span class="titulo">Visão geral</span>`,
		"/painel/patch?aba=terceiros":    `<span class="secao">Patch</span><span class="sep" aria-hidden="true">›</span><span class="titulo">Programas de terceiros</span>`,
		"/painel/campanhas":              `<span class="secao">Automação</span><span class="sep" aria-hidden="true">›</span><span class="titulo">Campanhas</span>`,
		"/painel/maquinas/60ef97e4-0001": `<a class="secao" href="/painel/inventario">Inventário</a><span class="sep" aria-hidden="true">›</span><span class="titulo">vm-ubuntu</span>`,
		"/painel/grupos-implantacao/1":   `<span class="secao">Automação</span><span class="sep" aria-hidden="true">›</span><a class="secao" href="/painel/grupos-implantacao">Grupos de implantação</a>`,
	} {
		b := pagina(h, "GET", path, sessaoTeste, nil).Body.String()
		if !strings.Contains(b, trilha) {
			i := strings.Index(b, `class="trilha"`)
			if i < 0 {
				t.Errorf("%s: sem trilha", path)
				continue
			}
			t.Errorf("%s: trilha %q", path, b[i:min(i+320, len(b))])
		}
	}
}

// O Parque em páginas de 100: a lista inteira de um parque grande eram megabytes de HTML. O
// estado e a busca seguem nos links; o CSV continua com todas as máquinas.
func TestParqueEmPaginas(t *testing.T) {
	agora := time.Now().UTC()
	st := store.NewMemory()
	abreSessao(t, st, sessaoTeste, agora)
	fleet := protocol.ComplianceFleet{EvaluatedAt: agora, Counts: map[string]int{"faltando": 130, "em_dia": 120}}
	for i := range 250 {
		estado := "faltando"
		if i >= 130 {
			estado = "em_dia"
		}
		fleet.Machines = append(fleet.Machines, protocol.ComplianceStatus{MachineID: fmt.Sprintf("%08d-0001", i), Hostname: fmt.Sprintf("pc-%04d", i),
			OS: "Windows 11 Pro 25H2", State: estado, LastSeenAt: agora, EvaluatedAt: agora})
	}
	h := api.New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), api.WithCompliance(&complianceFalso{fleet: fleet}))
	conta := func(b string) int { return strings.Count(b, `<td><a href="/painel/maquinas/`) }

	b := pagina(h, "GET", "/painel/parque", sessaoTeste, nil).Body.String()
	if conta(b) != 100 || !strings.Contains(b, "1–100 de 250") || !strings.Contains(b, `<a href="/painel/parque?pagina=2">próxima ›</a>`) ||
		strings.Contains(b, "‹ anterior") || !strings.Contains(b, `<span class="muted">(250)</span>`) || !strings.Contains(b, "pc-0099") || strings.Contains(b, "pc-0100") {
		t.Errorf("primeira página: %d linhas", conta(b))
	}
	b = pagina(h, "GET", "/painel/parque?pagina=3", sessaoTeste, nil).Body.String()
	if conta(b) != 50 || !strings.Contains(b, "201–250 de 250") || !strings.Contains(b, `<a href="/painel/parque?pagina=2">‹ anterior</a>`) || strings.Contains(b, "próxima ›") {
		t.Errorf("última página: %d linhas", conta(b))
	}
	// Além da última, ou lixo: a última e a primeira.
	if b := pagina(h, "GET", "/painel/parque?pagina=99", sessaoTeste, nil).Body.String(); !strings.Contains(b, "201–250 de 250") {
		t.Error("página além da última")
	}
	if b := pagina(h, "GET", "/painel/parque?pagina=abc", sessaoTeste, nil).Body.String(); !strings.Contains(b, "1–100 de 250") {
		t.Error("página ilegível")
	}
	// O estado e a busca seguem nos links.
	b = pagina(h, "GET", "/painel/parque?estado=faltando&pagina=2", sessaoTeste, nil).Body.String()
	if conta(b) != 30 || !strings.Contains(b, "101–130 de 130") || !strings.Contains(b, `<a href="/painel/parque?estado=faltando">‹ anterior</a>`) {
		t.Errorf("estado nas páginas: %d linhas", conta(b))
	}
	b = pagina(h, "GET", "/painel/parque?q=pc-0", sessaoTeste, nil).Body.String()
	if conta(b) != 100 || !strings.Contains(b, `<a href="/painel/parque?pagina=2&amp;q=pc-0">próxima ›</a>`) {
		t.Errorf("busca nas páginas: %d linhas", conta(b))
	}
	// Uma página só: sem a navegação.
	if b := pagina(h, "GET", "/painel/parque?q=pc-000", sessaoTeste, nil).Body.String(); conta(b) != 10 || strings.Contains(b, `class="paginas"`) {
		t.Errorf("lista curta: %d linhas", conta(b))
	}
	// O CSV traz todas.
	if csv := pagina(h, "GET", "/painel/parque.csv", sessaoTeste, nil).Body.String(); strings.Count(csv, "\n") < 251 {
		t.Errorf("CSV: %d linhas", strings.Count(csv, "\n"))
	}
}

// No painel de verdade: a mesma página, pedida com e sem gzip, tem o mesmo conteúdo; a
// resposta de um POST (onde ficam os segredos mostrados uma vez) nunca sai comprimida.
func TestPainelComprimeAsPaginas(t *testing.T) {
	h, st, _ := ambientePaginas(t)
	semeiaEquipamentos(t, st, 40)
	pura := pagina(h, "GET", "/painel/inventario?por=50", sessaoTeste, nil)
	comp := pagina(h, "GET", "/painel/inventario?por=50", sessaoTeste, nil, "Accept-Encoding", "gzip")
	if comp.Header().Get("Content-Encoding") != "gzip" || comp.Body.Len() >= pura.Body.Len()/3 {
		t.Fatalf("inventário: %q, %d bytes (pura: %d)", comp.Header().Get("Content-Encoding"), comp.Body.Len(), pura.Body.Len())
	}
	zr, err := gzip.NewReader(comp.Body)
	if err != nil {
		t.Fatal(err)
	}
	aberta, _ := io.ReadAll(zr)
	if string(aberta) != pura.Body.String() {
		t.Error("a página comprimida não é a mesma")
	}
	if comp.Header().Get("Content-Security-Policy") == "" || comp.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("os cabeçalhos de segurança continuam: %v", comp.Header())
	}
	if w := pagina(h, "POST", "/painel/sair", sessaoTeste, url.Values{}, "Accept-Encoding", "gzip"); w.Header().Get("Content-Encoding") != "" {
		t.Error("resposta de POST comprimida")
	}
	if w := pagina(h, "GET", "/painel/estilo.css", "", nil, "Accept-Encoding", "gzip"); w.Header().Get("Content-Encoding") != "gzip" {
		t.Error("a folha de estilo sai comprimida")
	}
}
