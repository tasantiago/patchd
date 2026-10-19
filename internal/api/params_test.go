package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/settings"
	"github.com/tasantiago/patchd/internal/store"
)

// parametrosDeTeste: um serviço com quatro parâmetros, como o do servidor (as regras reais
// ficam em cmd/server e têm os testes delas).
func parametrosDeTeste(st *store.Memory, env map[string]string) *settings.Service {
	dias := func(v string) (string, error) {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || n < 30 || n > 3650 {
			return "", errors.New("use de 30 a 3650 dias")
		}
		return strconv.Itoa(n), nil
	}
	defs := []settings.Def{
		{Key: "PATCHD_PANEL_TITLE", Group: "Aparência", Label: "Nome do painel", Default: "patchd", Effect: "na hora",
			Check: func(v string) (string, error) {
				v = strings.TrimSpace(v)
				if v == "" || len([]rune(v)) > 40 {
					return "", errors.New("use de 1 a 40 caracteres")
				}
				return v, nil
			}},
		{Key: "PATCHD_RETENTION_HISTORY_DAYS", Group: "Retenção", Label: "Inventários e buscas", Kind: settings.Number, Unit: "dias",
			Default: "90", Check: dias, Effect: "na próxima passada"},
		{Key: "PATCHD_CATALOG_INTERVAL", Group: "Catálogo", Label: "Sincronização automática", Kind: settings.Select, Default: "6h",
			Options: []settings.Option{{Value: "0", Label: "desligada"}, {Value: "6h", Label: "a cada 6 horas"}, {Value: "24h", Label: "uma vez por dia"}},
			Check:   func(v string) (string, error) { return strings.TrimSpace(v), nil }},
		{Key: "PATCHD_REPO_CACHE_URL", Group: "Cache de repositórios", Label: "URL anunciada",
			Check: func(v string) (string, error) { return strings.TrimSpace(v), nil }},
	}
	look := func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	s := settings.New(defs, look, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	_ = s.Refresh(context.Background())
	return s
}

// Aula 19.2: Configuração › Parâmetros. Só admin; a tela vence e vale na hora; tudo ou nada;
// antes e depois na auditoria e no e-mail; restaurar volta ao .env.
func TestParametrosPelaTela(t *testing.T) {
	ctx := context.Background()
	st0 := store.NewMemory()
	params := parametrosDeTeste(st0, map[string]string{"PATCHD_PANEL_TITLE": "patchd · lab", "PATCHD_CATALOG_INTERVAL": "4h"})
	c := &carteiro{}
	h, st, _ := ambientePaginas(t, api.WithSettings(params), api.WithNotifier(c), api.WithBrand("patchd · lab"),
		api.WithRepoCacheLive(func() *protocol.RepoCache {
			if u := params.Value("PATCHD_REPO_CACHE_URL"); u != "" {
				return &protocol.RepoCache{URL: u, AptHosts: []string{"archive.ubuntu.com"}}
			}
			return nil
		}))
	_ = st // a sessão e a auditoria ficam no armazenamento do ambiente; os parâmetros, no st0

	b := pagina(h, "GET", "/painel/configuracao?aba=parametros", sessaoAdmin, nil).Body.String()
	for _, tr := range []string{`aria-current="page">Parâmetros</a>`, "<h2>Aparência</h2>", "<h2>Retenção</h2>",
		`<input id="p-PATCHD_PANEL_TITLE" name="PATCHD_PANEL_TITLE" type="text" value="patchd · lab"`,
		`<span class="fonte env">.env</span>vale <b>patchd · lab</b>`, `<span class="fonte padrao">padrão</span>vale <b>90</b>`,
		`<option value="4h" selected>4h</option>`, `name="grupo" value="Retenção"`, "e, por e-mail, para a equipe"} {
		if !strings.Contains(b, tr) {
			t.Errorf("aba: faltou %q", tr)
		}
	}
	if r := pagina(h, "GET", "/painel/configuracao?aba=parametros", sessaoTeste, nil); r.Code != http.StatusForbidden {
		t.Errorf("leitura vendo: %d", r.Code)
	}
	if r := pagina(h, "POST", "/painel/configuracao/parametros", sessaoTeste, url.Values{"grupo": {"Aparência"}, "PATCHD_PANEL_TITLE": {"x"}}); r.Code != http.StatusForbidden {
		t.Errorf("leitura gravando: %d", r.Code)
	}

	// Inválido: nada grava, o que foi digitado volta com o problema.
	r := pagina(h, "POST", "/painel/configuracao/parametros", sessaoAdmin, url.Values{"grupo": {"Retenção"}, "PATCHD_RETENTION_HISTORY_DAYS": {"7"}})
	if r.Code != http.StatusUnprocessableEntity || !strings.Contains(r.Body.String(), `value="7"`) ||
		!strings.Contains(r.Body.String(), `<span class="erro-campo" role="alert">use de 30 a 3650 dias</span>`) {
		t.Fatalf("inválido: %d\n%s", r.Code, r.Body)
	}
	if l, _ := st0.Settings(ctx); len(l) != 0 {
		t.Fatal("gravou com erro")
	}

	// Um cartão por vez: campos de outro cartão no mesmo envio não entram.
	r = pagina(h, "POST", "/painel/configuracao/parametros", sessaoAdmin, url.Values{"grupo": {"Aparência"},
		"PATCHD_PANEL_TITLE": {"  Painel do TJ  "}, "PATCHD_RETENTION_HISTORY_DAYS": {"120"}})
	if r.Code != http.StatusSeeOther || r.Header().Get("Location") != "/painel/configuracao?aba=parametros&gravado=PATCHD_PANEL_TITLE" {
		t.Fatalf("gravar: %d %s", r.Code, r.Header().Get("Location"))
	}
	if params.Value("PATCHD_RETENTION_HISTORY_DAYS") != "90" {
		t.Error("campo de outro cartão gravou")
	}
	b = pagina(h, "GET", r.Header().Get("Location"), sessaoAdmin, nil).Body.String()
	for _, tr := range []string{"Gravado: Nome do painel.", `<span class="rot">Painel do TJ</span>`, `<title>Configuração · Painel do TJ</title>`,
		`<span class="fonte tela">tela</span>vale <b>Painel do TJ</b> · gravado por thyago em`, "Sem a tela: patchd · lab (.env)",
		`formaction="/painel/configuracao/parametros/restaurar" name="parametro" value="PATCHD_PANEL_TITLE"`} {
		if !strings.Contains(b, tr) {
			t.Errorf("depois de gravar: faltou %q", tr)
		}
	}
	if b := pagina(h, "GET", "/painel/entrar", "", nil).Body.String(); !strings.Contains(b, "<span>Painel do TJ</span>") {
		t.Error("o login também muda na hora")
	}
	ev, _ := st.AuditEvents(ctx, store.AuditFilter{})
	if len(ev) == 0 || ev[0].Action != "parametro.alterar" || ev[0].Target != "PATCHD_PANEL_TITLE" || ev[0].Detail != "patchd · lab → Painel do TJ" {
		t.Errorf("auditoria: %+v", ev)
	}
	esperaCarta := func(trecho string) {
		t.Helper()
		fim := time.Now().Add(2 * time.Second)
		for {
			c.mu.Lock()
			achou := len(c.msgs) > 0 && strings.Contains(c.msgs[len(c.msgs)-1], trecho)
			c.mu.Unlock()
			if achou {
				return
			}
			if time.Now().After(fim) {
				t.Fatalf("e-mail sem %q: %v", trecho, c.msgs)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	esperaCarta("Parâmetros alterados\nParâmetros do patchd alterados por thyago (AD)")
	esperaCarta("Nome do painel (PATCHD_PANEL_TITLE): patchd · lab → Painel do TJ")

	// O mesmo valor de novo: nada muda, nada na auditoria.
	r = pagina(h, "POST", "/painel/configuracao/parametros", sessaoAdmin, url.Values{"grupo": {"Aparência"}, "PATCHD_PANEL_TITLE": {"Painel do TJ"}})
	if r.Header().Get("Location") != "/painel/configuracao?aba=parametros&nada=1" {
		t.Errorf("igual: %s", r.Header().Get("Location"))
	}

	// O anúncio do cache muda no check-in seguinte.
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	_, _ = st.CreateEnrollmentToken(ctx, hash, "teste", time.Now().Add(time.Hour), 1)
	maq := registra(t, h, tok)
	checkin := func() *protocol.RepoCache {
		body, _ := json.Marshal(protocol.CheckinRequest{AgentVersion: "v0.17.6", SentAt: time.Now().UTC()})
		var resp protocol.CheckinResponse
		_ = json.Unmarshal(envia(h, "POST", "/api/v1/agent/checkin", maq.Credential, body).Body.Bytes(), &resp)
		return resp.RepoCache
	}
	if checkin() != nil {
		t.Error("sem URL, sem anúncio")
	}
	pagina(h, "POST", "/painel/configuracao/parametros", sessaoAdmin, url.Values{"grupo": {"Cache de repositórios"}, "PATCHD_REPO_CACHE_URL": {"http://cache.exemplo:8080"}})
	if rc := checkin(); rc == nil || rc.URL != "http://cache.exemplo:8080" {
		t.Errorf("anúncio: %+v", rc)
	}

	// Restaurar: o .env volta.
	r = pagina(h, "POST", "/painel/configuracao/parametros/restaurar", sessaoAdmin, url.Values{"parametro": {"PATCHD_PANEL_TITLE"}})
	if r.Header().Get("Location") != "/painel/configuracao?aba=parametros&restaurado=PATCHD_PANEL_TITLE" || params.Value("PATCHD_PANEL_TITLE") != "patchd · lab" {
		t.Fatalf("restaurar: %s", r.Header().Get("Location"))
	}
	if b := pagina(h, "GET", r.Header().Get("Location"), sessaoAdmin, nil).Body.String(); !strings.Contains(b, "Restaurado: Nome do painel volta a seguir o .env.") {
		t.Error("aviso do restaurar")
	}
	ev, _ = st.AuditEvents(ctx, store.AuditFilter{})
	if ev[0].Action != "parametro.restaurar" || ev[0].Detail != "Painel do TJ → patchd · lab (.env)" {
		t.Errorf("auditoria do restaurar: %+v", ev[0])
	}
	if r := pagina(h, "POST", "/painel/configuracao/parametros/restaurar", sessaoAdmin, url.Values{"parametro": {"PATCHD_LDAP_HOST"}}); r.Code != http.StatusBadRequest {
		t.Errorf("fora da lista: %d", r.Code)
	}
}

// Sem o serviço (testes antigos, outro armazenamento): sem a aba.
func TestSemParametros(t *testing.T) {
	h, _, _ := ambientePaginas(t)
	b := pagina(h, "GET", "/painel/configuracao?aba=parametros", sessaoAdmin, nil).Body.String()
	if strings.Contains(b, "Parâmetros</a>") || !strings.Contains(b, "<h2>Servidor</h2>") {
		t.Error("sem o serviço, sem a aba (cai na Sistema)")
	}
	if r := pagina(h, "POST", "/painel/configuracao/parametros", sessaoAdmin, url.Values{"grupo": {"x"}}); r.Code != http.StatusNotFound {
		t.Errorf("POST sem o serviço: %d", r.Code)
	}
}
