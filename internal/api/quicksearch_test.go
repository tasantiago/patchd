package api_test

import (
	"strings"
	"testing"
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
