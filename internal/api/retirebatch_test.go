package api_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/store"
)

// Limpeza do parque: o filtro "sem inventário" acha os registros que nunca mandaram dados; o
// lote aposenta só as offline; em Aposentadas, "excluir de vez" apaga as marcadas, menos a
// que guarda a senha da BIOS. Dados fictícios.
func TestAposentarEExcluirEmLote(t *testing.T) {
	h, st, _ := ambientePaginas(t)
	ctx := context.Background()
	registra := func(at time.Time) string {
		t.Helper()
		st.SetClock(func() time.Time { return at })
		defer st.SetClock(time.Now)
		_, th := identity.NewSecret(identity.EnrollmentPrefix)
		if _, err := st.CreateEnrollmentToken(ctx, th, "teste", at.Add(time.Hour), 1); err != nil {
			t.Fatal(err)
		}
		_, ch := identity.NewSecret(identity.CredentialPrefix)
		id := identity.NewMachineID()
		if _, err := st.Enroll(ctx, th, identity.NewMachine{ID: id, CredentialHash: ch, AgentVersion: "v0.18.15"}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	antigo := time.Now().Add(-13 * 24 * time.Hour)
	fantasmas := []string{registra(antigo), registra(antigo), registra(antigo)}
	recente := registra(time.Now()) // registrou agora: ainda online, sem inventário

	// Uma delas guarda a senha da BIOS (de antes de parar de falar): não é excluída.
	if _, _, err := st.CreateBIOSPassword(ctx, store.BIOSPassword{MachineID: fantasmas[2], Escrow: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	lista := pagina(h, "GET", "/painel/inventario?status=sem-inventario", sessaoAdmin, nil).Body.String()
	for _, quer := range []string{`<span class="kpi-rot">Sem inventário</span><span class="kpi-val v-laranja">4</span>`,
		"Aposentar as 3 encontradas que estão offline", `action="/painel/inventario/aposentar"`, `name="n" value="4"`} {
		if !strings.Contains(lista, quer) {
			t.Errorf("lista sem %q", quer)
		}
	}
	if b := pagina(h, "GET", "/painel/inventario?status=sem-inventario", sessaoTeste, nil).Body.String(); strings.Contains(b, "/painel/inventario/aposentar") {
		t.Error("leitura vê o lote")
	}
	aposenta := func(cookie string, f url.Values) *http.Response {
		t.Helper()
		return pagina(h, "POST", "/painel/inventario/aposentar", cookie, f).Result()
	}
	f := url.Values{"status": {"sem-inventario"}, "n": {"4"}, "motivo": {"registro sem inventário, agente não volta"}}
	if r := aposenta(sessaoTeste, f); r.StatusCode != http.StatusForbidden {
		t.Errorf("leitura aposenta: %d", r.StatusCode)
	}
	for nome, ruim := range map[string]url.Values{
		"sem filtro": {"n": {"6"}, "motivo": {"registro sem inventário"}},
		"sem motivo": {"status": {"sem-inventario"}, "n": {"4"}},
		"contagem":   {"status": {"sem-inventario"}, "n": {"3"}, "motivo": {"registro sem inventário"}},
	} {
		if loc := aposenta(sessaoAdmin, ruim).Header.Get("Location"); !strings.Contains(loc, "erro=") {
			t.Errorf("%s: %s", nome, loc)
		}
	}
	if l, _ := st.RetiredMachines(ctx); len(l) != 0 {
		t.Fatalf("as recusas aposentaram: %d", len(l))
	}
	loc := aposenta(sessaoAdmin, f).Header.Get("Location")
	if !strings.Contains(loc, "aposentadas=3") || !strings.Contains(loc, "online=1") {
		t.Fatalf("aposentar: %s", loc)
	}
	if b := pagina(h, "GET", loc, sessaoAdmin, nil).Body.String(); !strings.Contains(b, "3 máquina(s) aposentada(s)") || !strings.Contains(b, "1 estava(m) online") {
		t.Error("mensagem do lote")
	}
	ret, _ := st.RetiredMachines(ctx)
	if len(ret) != 3 {
		t.Fatalf("aposentadas: %d", len(ret))
	}
	for _, m := range ret {
		if m.ID == recente {
			t.Error("a online foi aposentada")
		}
	}

	b := pagina(h, "GET", "/painel/aposentadas", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, `action="/painel/aposentadas/excluir"`) || !strings.Contains(b, `name="confirmar"`) {
		t.Fatal("página das aposentadas sem a exclusão")
	}
	exclui := url.Values{"maquina": fantasmas}
	if loc := pagina(h, "POST", "/painel/aposentadas/excluir", sessaoAdmin, exclui).Header().Get("Location"); !strings.Contains(loc, "erro=confirmar") {
		t.Errorf("sem confirmar: %s", loc)
	}
	exclui.Set("confirmar", "1")
	if r := pagina(h, "POST", "/painel/aposentadas/excluir", sessaoTeste, exclui); r.Code != http.StatusForbidden {
		t.Errorf("leitura exclui: %d", r.Code)
	}
	exclui["maquina"] = append(exclui["maquina"], recente) // a ativa não sai
	loc = pagina(h, "POST", "/painel/aposentadas/excluir", sessaoAdmin, exclui).Header().Get("Location")
	if !strings.Contains(loc, "excluidas=2") || !strings.Contains(loc, "ficou_bios=1") || !strings.Contains(loc, "ficou_outras=1") {
		t.Fatalf("excluir: %s", loc)
	}
	if b := pagina(h, "GET", loc, sessaoAdmin, nil).Body.String(); !strings.Contains(b, "2 máquina(s) excluída(s) de vez") || !strings.Contains(b, "senha da BIOS guardada") {
		t.Error("mensagem da exclusão")
	}
	ret, _ = st.RetiredMachines(ctx)
	if len(ret) != 1 || ret[0].ID != fantasmas[2] {
		t.Errorf("ficou: %+v", ret)
	}
	ev, _ := st.AuditEvents(ctx, store.AuditFilter{Limit: 50})
	n := map[string]int{}
	for _, e := range ev {
		n[e.Action]++
	}
	if n["maquina.aposentar"] != 3 || n["maquina.aposentar_lote"] != 1 || n["maquina.excluir"] != 2 {
		t.Errorf("auditoria: %v", n)
	}
}
