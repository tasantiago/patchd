package api_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/biospw"
	"github.com/tasantiago/patchd/internal/machinebox"
	"github.com/tasantiago/patchd/internal/store"
)

func TestAdministrativeMachineMergePreservesBIOS(t *testing.T) {
	key, _ := machinebox.Generate()
	pub, _ := machinebox.Public(key)
	h, st, log := ambientePaginas(t, api.WithBIOSEscrow(key))
	ctx := context.Background()
	const target = "60ef97e4-0001"
	const source = "7aa604f7-0002"
	path := "/painel/maquinas/" + target
	password, _ := biospw.Generate()
	envelope, err := biospw.Seal(pub, source, password)
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := st.CreateBIOSPassword(ctx, store.BIOSPassword{MachineID: source, Escrow: envelope, EscrowKeyID: machinebox.KeyID(pub), CreatedAt: time.Now().UTC(), CreatedBy: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{path + "/mesclar", path + "/historico", path + "/historico/inventario/1?registro=" + source} {
		if r := pagina(h, "GET", route, sessaoTeste, nil); r.Code != http.StatusForbidden {
			t.Fatalf("read-only GET %s: %d", route, r.Code)
		}
	}
	review := pagina(h, "GET", path+"/mesclar?origem="+source, sessaoAdmin, nil)
	if review.Code != 200 || !strings.Contains(review.Body.String(), "Mesclar e preservar histórico") || !strings.Contains(review.Body.String(), source) {
		t.Fatal("missing review page")
	}
	form := url.Values{"origem": {source}, "motivo": {"Reinstalação do agente no mesmo computador"}, "confirmo": {"sim"}}
	if r := pagina(h, "POST", path+"/mesclar", sessaoTeste, form); r.Code != 403 {
		t.Fatal("reader merged")
	}
	if r := pagina(h, "POST", path+"/mesclar", sessaoAdmin, form, "Origin", "https://evil.example"); r.Code != 403 {
		t.Fatal("cross-origin merge accepted")
	}
	form.Del("confirmo")
	pagina(h, "POST", path+"/mesclar", sessaoAdmin, form)
	if members, _ := st.MachineMergeMembers(ctx, target); len(members) != 1 {
		t.Fatal("merged without confirmation")
	}
	form.Set("confirmo", "sim")
	r := pagina(h, "POST", path+"/mesclar", sessaoAdmin, form)
	if r.Code != 303 || !strings.Contains(local(r), "/historico?mesclada=1") {
		t.Fatalf("merge failed: %d %s", r.Code, local(r))
	}
	history := pagina(h, "GET", path+"/historico?registro="+source, sessaoAdmin, nil)
	body := history.Body.String()
	for _, want := range []string{"credencial revogada", source, "Senhas de BIOS de todos os registros", "/painel/maquinas/" + source + "/bios/" + itoa(b.ID) + "/revelar"} {
		if !strings.Contains(body, want) {
			t.Errorf("history missing %q", want)
		}
	}
	if history.Code != 200 || strings.Contains(body, password) {
		t.Fatal("history failed or leaked a password")
	}
	jsonReport := pagina(h, "GET", path+"/historico/inventario/1?registro="+source, sessaoAdmin, nil)
	if jsonReport.Code != 200 || !strings.Contains(jsonReport.Header().Get("Content-Type"), "application/json") {
		t.Fatal("old report unavailable")
	}
	if r := pagina(h, "GET", path+"/historico/inventario/1?registro=unrelated", sessaoAdmin, nil); r.Code != 404 {
		t.Fatal("unrelated identity included")
	}
	reveal := pagina(h, "POST", "/painel/maquinas/"+source+"/bios/"+itoa(b.ID)+"/revelar", sessaoAdmin, url.Values{"motivo": {"Chamado de teste da mesclagem"}})
	if reveal.Code != 200 || !strings.Contains(reveal.Body.String(), biospw.Grouped(password)) || !strings.Contains(reveal.Body.String(), path+"/historico") {
		t.Fatal("lost access to BIOS guard")
	}
	if strings.Contains(log.String(), password) || strings.Contains(log.String(), biospw.Grouped(password)) {
		t.Fatal("password leaked in log")
	}
	if changed, err := st.RestoreMachine(ctx, source); changed || err != nil {
		t.Fatal("incorporated identity restored")
	}
	events, err := st.AuditEvents(ctx, store.AuditFilter{Action: "maquina.mesclar"})
	if err != nil || len(events) != 1 {
		t.Fatal("merge audit missing", err)
	}
	if r := pagina(h, "POST", path+"/mesclar", sessaoAdmin, form); !strings.Contains(local(r), "erro=validacao") {
		t.Fatal("duplicate merge accepted")
	}
}
