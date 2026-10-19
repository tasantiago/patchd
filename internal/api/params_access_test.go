package api_test

import (
	"context"
	"io"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/settings"
	"github.com/tasantiago/patchd/internal/store"
)

// Aula 19.4: tirar um grupo da lista encerra as sessões daquela origem (menos a de quem
// mudou); acrescentar não encerra nada.
func TestGrupoTiradoEncerraSessoes(t *testing.T) {
	ctx := context.Background()
	st0 := store.NewMemory()
	lista := func(v string) (string, error) { return strings.TrimSpace(v), nil }
	defs := []settings.Def{
		{Key: "PATCHD_LDAP_READ_GROUP", Group: "Acesso", Label: "Grupos de leitura do AD", Check: lista, Sessions: panel.SourceAD},
		{Key: "PATCHD_OIDC_READ_ROLE", Group: "Acesso", Label: "Papéis de leitura do RHBK", Check: lista, Sessions: panel.SourceOIDC},
	}
	env := map[string]string{"PATCHD_LDAP_READ_GROUP": "GG-Leitura"}
	params := settings.New(defs, func(k string) (string, bool) { v, ok := env[k]; return v, ok }, st0, slog.New(slog.NewTextHandler(io.Discard, nil)))
	_ = params.Refresh(ctx)
	h, st, _ := ambientePaginas(t, api.WithSettings(params))
	agora := time.Now().UTC()
	abre := func(cookie, user, source string) {
		_ = st.CreatePanelSession(ctx, identity.Hash(panel.SessionPrefix+cookie), panel.Session{Username: user, Role: panel.RoleRead, Source: source,
			CreatedAt: agora, ExpiresAt: agora.Add(time.Hour), LastSeenAt: agora})
	}
	abre("ciclano", "ciclano", panel.SourceAD)
	abre("beltrano", "beltrano", panel.SourceOIDC)
	usuarios := func() []string {
		l, _ := st.PanelSessions(ctx)
		var out []string
		for _, s := range l {
			out = append(out, s.Username)
		}
		slices.Sort(out)
		return out
	}

	// Acrescentar: ninguém sai.
	r := pagina(h, "POST", "/painel/configuracao/parametros", sessaoAdmin, url.Values{"grupo": {"Acesso"}, "PATCHD_LDAP_READ_GROUP": {"GG-Leitura, GG-Gabinetes"}})
	if strings.Contains(r.Header().Get("Location"), "encerradas") || len(usuarios()) != 4 {
		t.Fatalf("acrescentar: %s %v", r.Header().Get("Location"), usuarios())
	}
	// Tirar um grupo do AD: a sessão do AD de outra pessoa cai; a do admin (AD), a do RHBK e a local ficam.
	r = pagina(h, "POST", "/painel/configuracao/parametros", sessaoAdmin, url.Values{"grupo": {"Acesso"}, "PATCHD_LDAP_READ_GROUP": {"GG-Gabinetes"}})
	if !strings.HasSuffix(r.Header().Get("Location"), "&encerradas=1") || strings.Join(usuarios(), ",") != "beltrano,consulta,thyago" {
		t.Fatalf("tirar: %s %v", r.Header().Get("Location"), usuarios())
	}
	b := pagina(h, "GET", r.Header().Get("Location"), sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, "1 sessão(ões) aberta(s) pelo AD ou pelo RHBK encerrada(s)") {
		t.Error("aviso das sessões")
	}
	ev, _ := st.AuditEvents(ctx, store.AuditFilter{})
	if ev[0].Action != "parametro.sessoes" || ev[0].Target != "AD" || !strings.HasPrefix(ev[0].Detail, "1 sessão") {
		t.Errorf("auditoria: %+v", ev[0])
	}
	// Restaurar o padrão também tira (o .env tem GG-Leitura, a tela tinha GG-Gabinetes).
	abre("ciclano2", "ciclano", panel.SourceAD)
	r = pagina(h, "POST", "/painel/configuracao/parametros/restaurar", sessaoAdmin, url.Values{"parametro": {"PATCHD_LDAP_READ_GROUP"}})
	if !strings.HasSuffix(r.Header().Get("Location"), "&encerradas=1") || slices.Contains(usuarios(), "ciclano") {
		t.Errorf("restaurar: %s %v", r.Header().Get("Location"), usuarios())
	}
	// O RHBK: tirar um papel derruba quem entrou pelo RHBK.
	pagina(h, "POST", "/painel/configuracao/parametros", sessaoAdmin, url.Values{"grupo": {"Acesso"}, "PATCHD_OIDC_READ_ROLE": {"gabinetes"}})
	r = pagina(h, "POST", "/painel/configuracao/parametros", sessaoAdmin, url.Values{"grupo": {"Acesso"}, "PATCHD_OIDC_READ_ROLE": {""}})
	if !strings.HasSuffix(r.Header().Get("Location"), "&encerradas=1") || slices.Contains(usuarios(), "beltrano") {
		t.Errorf("RHBK: %s %v", r.Header().Get("Location"), usuarios())
	}
}
