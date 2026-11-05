package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

// Alertas em lote: o admin marca na tabela (ou pega todos os abertos do filtro), confere e
// fecha (ou reconhece). Fechar à mão zera a contagem: o problema que continua abre um alerta
// novo. A lista que muda entre a conferência e a confirmação volta para conferir de novo.
func TestAlertasEmLote(t *testing.T) {
	ctx := context.Background()
	h, st, _ := ambientePaginas(t)
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	if _, err := st.CreateEnrollmentToken(ctx, hash, "teste", time.Now().Add(time.Hour), 5); err != nil {
		t.Fatal(err)
	}
	m := registra(t, h, tok)
	agora := time.Now().UTC().Truncate(time.Minute)
	manda := func(desde time.Duration, n int) {
		t.Helper()
		var amostras []protocol.PerfSample
		for i := 0; i < n; i++ { // CPU, memória e disco além do limite
			amostras = append(amostras, protocol.PerfSample{At: agora.Add(desde + time.Duration(i)*time.Minute), CPU: 97, Mem: 96,
				Disks: map[string]float64{"C:": 4}})
		}
		corpo, _ := json.Marshal(protocol.CheckinRequest{AgentVersion: "v", SentAt: time.Now().UTC(), Perf: amostras})
		if r := envia(h, "POST", "/api/v1/agent/checkin", m.Credential, corpo); r.Code != http.StatusOK {
			t.Fatalf("check-in: %d", r.Code)
		}
	}
	manda(-12*time.Minute, 12)
	abertos := func() map[string]store.PerfAlert {
		t.Helper()
		l, _ := st.PerfAlerts(ctx, store.PerfAlertFilter{OpenOnly: true})
		out := map[string]store.PerfAlert{}
		for _, a := range l {
			out[a.Metric] = a
		}
		return out
	}
	a := abertos()
	if len(a) != 3 {
		t.Fatalf("abertos: %+v", a)
	}

	b := pagina(h, "GET", "/painel/alertas", sessaoAdmin, nil).Body.String()
	for _, quer := range []string{`<form id="alertas-lote" class="lote" method="post" action="/painel/alertas/lote">`, "todos os 3 abertos",
		`<input type="checkbox" name="alerta" value="` + itoa(a["cpu"].ID) + `" form="alertas-lote"`} {
		if !strings.Contains(b, quer) {
			t.Errorf("tela: faltou %q", quer)
		}
	}
	if b := pagina(h, "GET", "/painel/alertas?medida=disk", sessaoAdmin, nil).Body.String(); !strings.Contains(b, "todos os 1 abertos desta medida") {
		t.Error("contagem do filtro")
	}
	if b := pagina(h, "GET", "/painel/alertas", sessaoTeste, nil).Body.String(); strings.Contains(b, "alertas-lote") {
		t.Error("leitura vê o lote")
	}
	lote := func(cookie string, f url.Values) (int, string, string) {
		t.Helper()
		r := pagina(h, "POST", "/painel/alertas/lote", cookie, f)
		return r.Code, local(r), r.Body.String()
	}
	if c, _, _ := lote(sessaoTeste, url.Values{"acao": {"fechar"}, "quais": {"todos"}}); c != http.StatusForbidden {
		t.Fatalf("leitura fecha: %d", c)
	}
	if _, l, _ := lote(sessaoAdmin, url.Values{"acao": {"fechar"}, "quais": {"marcados"}}); !strings.Contains(l, "erro=lote-vazio") {
		t.Errorf("nenhum marcado: %s", l)
	}

	// Marcados: a conferência, e nada muda antes de confirmar.
	f := url.Values{"acao": {"fechar"}, "quais": {"marcados"}, "alerta": {itoa(a["cpu"].ID)}, "nota": {"servidor de testes"}}
	c, _, corpo := lote(sessaoAdmin, f)
	if c != http.StatusOK || !strings.Contains(corpo, "Conferir: Fechar 1 alerta(s)") || !strings.Contains(corpo, "fechar não esconde problema que continua") {
		t.Fatalf("conferência: %d\n%s", c, corpo)
	}
	if len(abertos()) != 3 {
		t.Fatal("a conferência fechou")
	}
	digest := regexp.MustCompile(`name="lista" value="([0-9a-f]+)"`).FindStringSubmatch(corpo)
	if digest == nil {
		t.Fatal("sem a lista conferida")
	}
	f.Set("lista", digest[1])
	f.Set("confirmar", "1")
	if _, l, _ := lote(sessaoAdmin, f); l != "/painel/alertas?fechados=1" {
		t.Fatalf("fechar: %s", l)
	}
	a2 := abertos()
	if _, ok := a2["cpu"]; ok || len(a2) != 2 {
		t.Fatalf("depois de fechar: %+v", a2)
	}
	b = pagina(h, "GET", "/painel/alertas?estado=todos", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, "fechado à mão em") || !strings.Contains(b, "por thyago: servidor de testes") {
		t.Errorf("fechado à mão na tabela:\n%s", b)
	}
	if ev, _ := st.AuditEvents(ctx, store.AuditFilter{Action: "alerta.fechar-lote"}); len(ev) != 1 || ev[0].Target != "1 alerta(s)" ||
		!strings.Contains(ev[0].Detail, "os marcados; nota: servidor de testes; ids: "+itoa(a["cpu"].ID)) {
		t.Errorf("trilha: %+v", ev)
	}

	// A CPU continua alta: depois dos minutos seguidos, um alerta novo abre.
	manda(0, 11)
	if n, ok := abertos()["cpu"]; !ok || n.ID == a["cpu"].ID {
		t.Fatalf("alerta novo da CPU: %+v", abertos())
	}

	// Todos do filtro, reconhecer: só o disco.
	f = url.Values{"acao": {"reconhecer"}, "quais": {"todos"}, "medida": {"disk"}}
	_, _, corpo = lote(sessaoAdmin, f)
	f.Set("lista", regexp.MustCompile(`name="lista" value="([0-9a-f]+)"`).FindStringSubmatch(corpo)[1])
	f.Set("confirmar", "1")
	if _, l, _ := lote(sessaoAdmin, f); l != "/painel/alertas?medida=disk&reconhecidos=1" {
		t.Fatalf("reconhecer: %s", l)
	}
	if d := abertos()["disk"]; d.AckAt == nil || d.AckBy != "thyago" {
		t.Errorf("disco: %+v", d)
	}

	// A lista muda entre a conferência e a confirmação: volta para conferir, nada fecha.
	f = url.Values{"acao": {"fechar"}, "quais": {"todos"}}
	_, _, corpo = lote(sessaoAdmin, f)
	f.Set("lista", regexp.MustCompile(`name="lista" value="([0-9a-f]+)"`).FindStringSubmatch(corpo)[1])
	f.Set("confirmar", "1")
	if _, err := st.ClosePerfAlerts(ctx, []int64{abertos()["mem"].ID}, "outro", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	c, _, corpo = lote(sessaoAdmin, f)
	if c != http.StatusOK || !strings.Contains(corpo, "A lista mudou desde a conferência") || !strings.Contains(corpo, "Conferir: Fechar 2 alerta(s)") {
		t.Fatalf("lista mudou: %d\n%s", c, corpo)
	}
	if len(abertos()) != 2 {
		t.Error("fechou com a lista mudada")
	}
}
