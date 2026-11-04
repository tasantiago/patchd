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

// A página do grupo de implantação: o resumo em cartões, o nome, as máquinas e o adicionar
// com filtro (texto ou faixa de IP), o mover com conferência, o devolver às regras, o colar
// lista e as regras por faixa de IP, com a prévia e os conflitos.
func TestPaginaDoGrupo(t *testing.T) {
	h, st, _ := ambientePaginas(t)
	ctx := context.Background()
	maq := map[string]string{"pc-1001": "10.1.2.10/24", "pc-1002": "10.1.2.60/24", "pc-1003": "10.9.0.5/16", "pc-1004": "10.1.2.200/24"}
	for nome, ip := range maq {
		if _, err := st.SaveInventory(ctx, nome, protocol.InventoryReport{SchemaVersion: 1, Hash: "h-" + nome, OS: protocol.OSInfo{Hostname: nome}}); err != nil {
			t.Fatal(err)
		}
		s := &protocol.MachineState{Interfaces: []protocol.NetInterface{{Name: "eth0", MAC: "00:11:22:33:44:55", IPv4: []string{ip}, Up: true}}}
		if err := st.SaveHostState(ctx, nome, s, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.ReplaceAssetLocations(ctx, []store.AssetLocation{{Tombo: "1001", Building: "Bloco A"}, {Tombo: "1002", Building: "Bloco A"},
		{Tombo: "1004", Building: "Bloco B"}}, store.AssetImport{By: "t", File: "p.csv"}); err != nil {
		t.Fatal(err)
	}
	grupo := func(id string) int {
		t.Helper()
		r, _, _ := st.MachineRing(ctx, id)
		return r
	}
	get := func(path string) string {
		t.Helper()
		return pagina(h, "GET", path, sessaoAdmin, nil).Body.String()
	}
	post := func(path string, f url.Values) (int, string, string) {
		t.Helper()
		r := pagina(h, "POST", path, sessaoAdmin, f)
		return r.Code, local(r), r.Body.String()
	}

	// Nome do grupo: aparece no cartão e na página.
	if _, l, _ := post("/painel/grupos-implantacao/nome", url.Values{"anel": {"3"}, "nome": {"  Fórum   Cível "}}); !strings.Contains(l, "nome=1") {
		t.Fatalf("nome: %s", l)
	}
	if _, l, _ := post("/painel/grupos-implantacao/nome", url.Values{"anel": {"3"}, "nome": {strings.Repeat("x", 41)}}); !strings.Contains(l, "erro=nome") {
		t.Errorf("nome longo: %s", l)
	}
	b := get("/painel/grupos-implantacao")
	if !strings.Contains(b, "Grupo 3 · Fórum Cível") || !strings.Contains(b, `href="/painel/grupos-implantacao/3"`) || !strings.Contains(b, "Grupo 1 (padrão)") {
		t.Errorf("resumo:\n%s", b)
	}

	// Máquinas do grupo 1 e o adicionar do grupo 3, com a faixa de IP como filtro.
	b = get("/painel/grupos-implantacao/1")
	if !strings.Contains(b, ">pc-1001</a>") || !strings.Contains(b, `<span class="estado sem_dado_recente">padrão</span>`) {
		t.Errorf("máquinas do 1:\n%s", b)
	}
	b = get("/painel/grupos-implantacao/3?aba=adicionar&q=" + url.QueryEscape("10.1.2.0/24"))
	if !strings.Contains(b, ">pc-1001</a>") || !strings.Contains(b, ">pc-1004</a>") || strings.Contains(b, ">pc-1003</a>") || !strings.Contains(b, "Conferir todas as 3 do filtro") {
		t.Errorf("adicionar com faixa:\n%s", b)
	}
	if b := pagina(h, "GET", "/painel/grupos-implantacao/1", sessaoTeste, nil).Body.String(); strings.Contains(b, `name="sel"`) || strings.Contains(b, "aba=adicionar") {
		t.Error("leitura vê o lote ou o adicionar")
	}
	if r := pagina(h, "POST", "/painel/grupos-implantacao/mover", sessaoTeste, url.Values{"destino": {"3"}, "sel": {"pc-1001"}}); r.Code != http.StatusForbidden {
		t.Errorf("leitura move: %d", r.Code)
	}
	if r := pagina(h, "GET", "/painel/grupos-implantacao/12", sessaoAdmin, nil); r.Code != http.StatusNotFound {
		t.Errorf("grupo 12: %d", r.Code)
	}

	// Mover: a conferência diz de onde saem; só o confirmar grava (e a máquina fica "à mão").
	code, _, body := post("/painel/grupos-implantacao/mover", url.Values{"destino": {"3"}, "volta": {"3"}, "aba": {"adicionar"}, "sel": {"pc-1001", "nao-existe"}})
	if code != http.StatusOK || !strings.Contains(body, "1 máquina(s) vão para o grupo 3 · Fórum Cível: 1 saem do grupo 1 (padrão). 1 não estão mais no parque.") {
		t.Fatalf("conferência: %d\n%s", code, body)
	}
	if grupo("pc-1001") != 1 {
		t.Fatal("a conferência gravou")
	}
	_, l, _ := post("/painel/grupos-implantacao/mover", url.Values{"destino": {"3"}, "volta": {"3"}, "aba": {"adicionar"}, "id": {"pc-1001"}, "confirmar": {"1"}})
	if !strings.HasPrefix(l, "/painel/grupos-implantacao/3?aba=adicionar&") || !strings.Contains(l, "movidas=1") || grupo("pc-1001") != 3 {
		t.Fatalf("mover: %s grupo=%d", l, grupo("pc-1001"))
	}
	if b := get("/painel/grupos-implantacao/3"); !strings.Contains(b, `<span class="estado reboot_pendente">à mão</span>`) {
		t.Error("a movida não aparece à mão")
	}
	// Todas do filtro: a conferência leva a lista do filtro, fechada.
	code, _, body = post("/painel/grupos-implantacao/mover", url.Values{"destino": {"3"}, "volta": {"3"}, "aba": {"adicionar"}, "filtro": {"1"},
		"q": {"10.1.2.0/24"}})
	if code != http.StatusOK || !strings.Contains(body, "2 máquina(s) vão para o grupo 3 · Fórum Cível: 2 saem do grupo 1 (padrão).") ||
		strings.Count(body, `name="id"`) != 2 {
		t.Fatalf("todas do filtro: %d\n%s", code, body)
	}
	// Nada a mover: volta com erro.
	if _, l, _ := post("/painel/grupos-implantacao/mover", url.Values{"destino": {"3"}, "volta": {"3"}, "sel": {"pc-1001"}}); !strings.Contains(l, "erro=lote-vazio") {
		t.Errorf("já no grupo: %s", l)
	}

	// Colar lista: nome com domínio e outra coluna, IP, tombo, um não achado com sugestões, repetida.
	lista := "PC-1002.tj.local\tsala 3\n10.9.0.5\n1004\npc-1009\npc-1002\n\n"
	code, _, body = post("/painel/grupos-implantacao/colar", url.Values{"destino": {"3"}, "lista": {lista}})
	for _, tr := range []string{"5 linha(s): 3 máquina(s) para mover, 1 não achada(s), 1 repetida(s).", "<td>nome</td>", "<td>IP</td>", "<td>tombo 1004</td>",
		"<code>pc-1009</code></td><td>pc-1001, pc-1002, pc-1003</td>"} {
		if code != http.StatusOK || !strings.Contains(body, tr) {
			t.Errorf("colar: faltou %q\n%s", tr, body)
		}
	}
	if strings.Count(body, `name="sel"`) != 3 || grupo("pc-1002") != 1 {
		t.Errorf("colar: caixas ou gravou")
	}
	if _, l, _ := post("/painel/grupos-implantacao/colar", url.Values{"destino": {"3"}, "lista": {"  \n "}}); !strings.Contains(l, "erro=colar-vazia") {
		t.Errorf("colar vazia: %s", l)
	}

	// Devolver às regras: sem regra, a máquina fica onde está, mas deixa de ser "à mão".
	if _, l, _ := post("/painel/grupos-implantacao/devolver", url.Values{"volta": {"3"}, "sel": {"pc-1001"}}); !strings.Contains(l, "devolvidas=1") || grupo("pc-1001") != 3 {
		t.Errorf("devolver: %s %d", l, grupo("pc-1001"))
	}

	// Faixa de IP: prévia, gravar, repetida, inválida.
	f := url.Values{"anel": {"5"}, "faixa": {"10.1.2.0/24"}}
	code, _, body = post("/painel/grupos-implantacao/faixa", f)
	if code != http.StatusOK || !strings.Contains(body, "A faixa <code>10.1.2.0/24</code> (10.1.2.0 – 10.1.2.255) pega 3 máquina(s). 3 vão para o grupo 5: 2 do grupo 1 (padrão), 1 do grupo 3 · Fórum Cível.") {
		t.Fatalf("prévia da faixa: %d\n%s", code, body)
	}
	f.Set("confirmar", "1")
	if _, l, _ := post("/painel/grupos-implantacao/faixa", f); !strings.Contains(l, "faixa-ok=1") || !strings.Contains(l, "movidas=3") {
		t.Fatalf("gravar faixa: %s", l)
	}
	if grupo("pc-1001") != 5 || grupo("pc-1002") != 5 || grupo("pc-1004") != 5 || grupo("pc-1003") != 1 {
		t.Errorf("faixa: %d %d %d %d", grupo("pc-1001"), grupo("pc-1002"), grupo("pc-1004"), grupo("pc-1003"))
	}
	if _, l, _ := post("/painel/grupos-implantacao/faixa", url.Values{"anel": {"6"}, "faixa": {"10.1.2.0/24"}}); !strings.Contains(l, "erro=faixa-repetida") {
		t.Errorf("repetida: %s", l)
	}
	if _, l, _ := post("/painel/grupos-implantacao/faixa", url.Values{"anel": {"6"}, "faixa": {"10.1.2.5"}}); !strings.Contains(l, "erro=faixa") {
		t.Errorf("inválida: %s", l)
	}
	b = get("/painel/grupos-implantacao/5?aba=regras")
	if !strings.Contains(b, "<td><code>10.1.2.0/24</code></td><td><code>10.1.2.0 – 10.1.2.255</code></td><td class=\"num\">3</td><td class=\"num\">3</td>") {
		t.Errorf("tabela das faixas:\n%s", b)
	}
	if b := get("/painel/grupos-implantacao/5"); !strings.Contains(b, "pela faixa 10.1.2.0/24") {
		t.Error("como entrou: pela faixa")
	}

	// Faixa menor vence a maior.
	f = url.Values{"anel": {"6"}, "faixa": {"10.1.2.200-250"}, "confirmar": {"1"}}
	if _, l, _ := post("/painel/grupos-implantacao/faixa", f); !strings.Contains(l, "movidas=1") || grupo("pc-1004") != 6 {
		t.Errorf("faixa menor: %s %d", l, grupo("pc-1004"))
	}
	// Mesmo tamanho, grupos diferentes: conflito, ninguém sai do lugar.
	f = url.Values{"anel": {"7"}, "faixa": {"10.1.2.200-10.1.2.250"}, "confirmar": {"1"}}
	if _, l, _ := post("/painel/grupos-implantacao/faixa", f); !strings.Contains(l, "movidas=0") || grupo("pc-1004") != 6 {
		t.Errorf("conflito: %s %d", l, grupo("pc-1004"))
	}
	b = get("/painel/grupos-implantacao/6?como=conflito")
	if !strings.Contains(b, ">pc-1004</a>") || !strings.Contains(b, "discordam: a máquina fica onde está") {
		t.Errorf("conflito na página:\n%s", b)
	}
	if b := get("/painel/grupos-implantacao"); !strings.Contains(b, "1 em conflito") {
		t.Error("conflito no cartão")
	}

	// O prédio vence a faixa, com o aviso.
	if _, l, _ := post("/painel/grupos-implantacao/predio", url.Values{"anel": {"3"}, "predio": {"Bloco A"}, "confirmar": {"1"}}); !strings.Contains(l, "movidas=2") {
		t.Fatalf("prédio: %s", l)
	}
	if grupo("pc-1001") != 3 || grupo("pc-1002") != 3 {
		t.Errorf("prédio vence: %d %d", grupo("pc-1001"), grupo("pc-1002"))
	}
	if b := get("/painel/grupos-implantacao/3"); !strings.Contains(b, "a faixa 10.1.2.0/24 diz grupo 5; vale o prédio") {
		t.Errorf("aviso prédio x faixa:\n%s", b)
	}

	// Remover a faixa: ninguém muda de grupo.
	ips, _ := st.IPRings(ctx)
	if _, l, _ := post("/painel/grupos-implantacao/faixa/remover", url.Values{"id": {"1"}}); !strings.HasPrefix(l, "/painel/grupos-implantacao/5?") || len(ips) != 3 {
		t.Errorf("remover faixa: %s", l)
	}
	if grupo("pc-1004") != 6 {
		t.Error("remover a faixa moveu")
	}
	ev, _ := st.AuditEvents(ctx, store.AuditFilter{Limit: 30})
	acoes := map[string]bool{}
	for _, e := range ev {
		acoes[e.Action] = true
	}
	for _, a := range []string{"grupo.nome", "maquina.anel_lote", "maquina.anel_devolver", "faixa.grupo", "faixa.grupo_remover", "predio.grupo"} {
		if !acoes[a] {
			t.Errorf("auditoria sem %s", a)
		}
	}
}
