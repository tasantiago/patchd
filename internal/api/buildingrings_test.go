package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

// Grupo de implantação pelo prédio: ligar o prédio move as máquinas dele (menos as movidas à
// mão), a máquina nova do prédio entra no grupo no primeiro inventário, devolver as exceções
// as traz de volta, e remover o vínculo não move ninguém.
func TestGrupoPeloPredio(t *testing.T) {
	h, st, _ := ambientePaginas(t)
	ctx := context.Background()
	for _, nome := range []string{"pc-1001", "pc-1002", "pc-1003", "pc-2001"} {
		if _, err := st.SaveInventory(ctx, nome, protocol.InventoryReport{SchemaVersion: 1, Hash: "h-" + nome, OS: protocol.OSInfo{Hostname: nome}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.ReplaceAssetLocations(ctx, []store.AssetLocation{
		{Tombo: "1001", Building: "Bloco A"}, {Tombo: "1002", Building: "Bloco A"}, {Tombo: "1003", Building: "Bloco A"},
		{Tombo: "1004", Building: "Bloco A"}, {Tombo: "2001", Building: "Bloco B"},
	}, store.AssetImport{By: "t", File: "p.csv"}); err != nil {
		t.Fatal(err)
	}
	// O pc-1003 é piloto, posto à mão.
	if _, err := st.SetMachineRing(ctx, "pc-1003", 0); err != nil {
		t.Fatal(err)
	}
	grupo := func(id string) int {
		t.Helper()
		r, ok, err := st.MachineRing(ctx, id)
		if err != nil || !ok {
			t.Fatalf("%s: %v %v", id, ok, err)
		}
		return r
	}
	tela := func(cookie string) string {
		t.Helper()
		return pagina(h, "GET", "/painel/grupos-implantacao", cookie, nil).Body.String()
	}
	b := tela(sessaoAdmin)
	for _, tr := range []string{"<h2>Grupo de implantação por prédio</h2>", "Nenhum prédio ligado", `<option value="Bloco A">Bloco A · 3 máquina(s)</option>`,
		`action="/painel/grupos-implantacao/predio"`} {
		if !strings.Contains(b, tr) {
			t.Errorf("tela: faltou %q", tr)
		}
	}
	if strings.Contains(tela(sessaoTeste), `action="/painel/grupos-implantacao/predio"`) {
		t.Error("leitura vê o formulário")
	}
	liga := func(cookie string, f url.Values) *resposta {
		t.Helper()
		r := pagina(h, "POST", "/painel/grupos-implantacao/predio", cookie, f)
		return &resposta{code: r.Code, loc: local(r)}
	}
	if r := liga(sessaoTeste, url.Values{"predio": {"Bloco A"}, "anel": {"3"}}); r.code != http.StatusForbidden {
		t.Fatalf("leitura liga: %d", r.code)
	}
	if r := liga(sessaoAdmin, url.Values{"predio": {"Bloco Z"}, "anel": {"3"}}); !strings.Contains(r.loc, "erro=predio") {
		t.Errorf("prédio fora do patrimônio: %s", r.loc)
	}
	if r := liga(sessaoAdmin, url.Values{"predio": {"Bloco A"}, "anel": {"12"}}); !strings.Contains(r.loc, "erro=anel") {
		t.Errorf("grupo inválido: %s", r.loc)
	}

	// Ligar: pc-1001 e pc-1002 vão para o 3; o piloto fica; o Bloco B não muda.
	r := liga(sessaoAdmin, url.Values{"predio": {"Bloco A"}, "anel": {"3"}})
	if !strings.Contains(r.loc, "vinculo=1") || !strings.Contains(r.loc, "movidas=2") {
		t.Fatalf("ligar: %s", r.loc)
	}
	if grupo("pc-1001") != 3 || grupo("pc-1002") != 3 || grupo("pc-1003") != 0 || grupo("pc-2001") != 1 {
		t.Fatalf("grupos: %d %d %d %d", grupo("pc-1001"), grupo("pc-1002"), grupo("pc-1003"), grupo("pc-2001"))
	}
	b = pagina(h, "GET", "/painel/grupos-implantacao?vinculo=1&anel=3&movidas=2", sessaoAdmin, nil).Body.String()
	for _, tr := range []string{"Prédio ligado ao grupo de implantação 3: 2 máquina(s) mudaram de grupo",
		`<td>Bloco A</td><td>3</td><td class="num">3</td><td class="num">2</td><td class="num">1</td><td>thyago · `,
		"Devolver as 1 ao grupo 3", "hoje no grupo 3"} {
		if !strings.Contains(b, tr) {
			t.Errorf("depois de ligar: faltou %q", tr)
		}
	}
	ev, _ := st.AuditEvents(ctx, store.AuditFilter{Limit: 5})
	if len(ev) == 0 || ev[0].Action != "predio.grupo" || ev[0].Target != "Bloco A" || !strings.Contains(ev[0].Detail, "grupo de implantação 3 · 2 máquina(s) movida(s)") {
		t.Errorf("auditoria: %+v", ev)
	}

	// Uma máquina movida à mão depois do vínculo fica onde foi posta.
	if _, err := st.SetMachineRing(ctx, "pc-1002", 5); err != nil {
		t.Fatal(err)
	}

	// Máquina nova do prédio: entra no grupo no primeiro inventário.
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	_, _ = st.CreateEnrollmentToken(ctx, hash, "teste", time.Now().Add(time.Hour), 1)
	maq := registra(t, h, tok)
	rep := protocol.InventoryReport{SchemaVersion: protocol.InventorySchemaVersion, AgentVersion: "v0.18.0", CollectedAt: time.Now().UTC(),
		Hash: "sha256:" + strings.Repeat("a", 64), OS: protocol.OSInfo{Family: "windows", Name: "Windows 11 Pro", Hostname: "FJ001004"},
		Software: []protocol.Software{}}
	corpo, _ := json.Marshal(rep)
	if r := envia(h, "POST", "/api/v1/agent/inventory", maq.Credential, corpo); r.Code >= 300 {
		t.Fatalf("inventário: %d %s", r.Code, r.Body)
	}
	if g := grupo(maq.MachineID); g != 3 {
		t.Errorf("máquina nova do prédio no grupo %d", g)
	}

	// Devolver as exceções: o piloto e o pc-1002 voltam ao 3.
	pr := pagina(h, "POST", "/painel/grupos-implantacao/predio/excecoes", sessaoAdmin, url.Values{"predio": {"Bloco A"}})
	if l := local(pr); !strings.Contains(l, "devolvidas=2") || !strings.Contains(l, "movidas=2") {
		t.Errorf("devolver: %s", l)
	}
	if grupo("pc-1003") != 3 || grupo("pc-1002") != 3 {
		t.Errorf("depois de devolver: %d %d", grupo("pc-1003"), grupo("pc-1002"))
	}

	// Trocar o grupo do vínculo, incluindo as movidas à mão.
	if _, err := st.SetMachineRing(ctx, "pc-1001", 0); err != nil {
		t.Fatal(err)
	}
	if r := liga(sessaoAdmin, url.Values{"predio": {"Bloco A"}, "anel": {"4"}, "excecoes": {"1"}}); !strings.Contains(r.loc, "movidas=4") {
		t.Errorf("trocar com exceções: %s", r.loc)
	}

	// Remover: ninguém muda de grupo; remover de novo avisa.
	pr = pagina(h, "POST", "/painel/grupos-implantacao/predio/remover", sessaoAdmin, url.Values{"predio": {"Bloco A"}})
	if l := local(pr); !strings.Contains(l, "vinculo-removido=1") {
		t.Errorf("remover: %s", l)
	}
	if grupo("pc-1001") != 4 {
		t.Errorf("remover moveu: %d", grupo("pc-1001"))
	}
	pr = pagina(h, "POST", "/painel/grupos-implantacao/predio/remover", sessaoAdmin, url.Values{"predio": {"Bloco A"}})
	if l := local(pr); !strings.Contains(l, "erro=sem-vinculo") {
		t.Errorf("remover de novo: %s", l)
	}
}

// A importação do patrimônio aplica os vínculos: a máquina que mudou de prédio no arquivo
// novo vai para o grupo do prédio novo.
func TestGrupoPeloPredioNaImportacao(t *testing.T) {
	h, st, _ := ambientePaginas(t)
	ctx := context.Background()
	if _, err := st.SaveInventory(ctx, "pc-3001", protocol.InventoryReport{SchemaVersion: 1, Hash: "h", OS: protocol.OSInfo{Hostname: "pc-3001"}}); err != nil {
		t.Fatal(err)
	}
	importa := func(csv string) string {
		t.Helper()
		r := envioDePatrimonio(h, sessaoAdmin, "p.csv", []byte(csv))
		return local(r)
	}
	importa("TOMBO,DEPREDIO\n3001,Anexo\n9999,Sede\n")
	if r := pagina(h, "POST", "/painel/grupos-implantacao/predio", sessaoAdmin, url.Values{"predio": {"Sede"}, "anel": {"2"}}); !strings.Contains(local(r), "movidas=0") {
		t.Fatalf("ligar: %s", local(r))
	}
	if l := importa("TOMBO,DEPREDIO\n3001,Sede\n"); !strings.Contains(l, "grupo=1") {
		t.Errorf("importação: %s", l)
	}
	if g, _, _ := st.MachineRing(ctx, "pc-3001"); g != 2 {
		t.Errorf("grupo depois da importação: %d", g)
	}
}

type resposta struct {
	code int
	loc  string
}
