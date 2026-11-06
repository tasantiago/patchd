package api_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
)

// Programa aberto: o check-in guarda as capacidades do agente e marca os jobs adiados cujo
// programa fechou; a campanha de terceiros ganha a opção "fechar com aviso", só com horário.
func TestProgramaAbertoPeloPainel(t *testing.T) {
	ctx := context.Background()
	_, priv, _ := jobs.GenerateKey()
	m, _ := thirdparty.Default()
	h, st, _ := ambientePaginas(t, api.WithJobKey(priv), api.WithThirdPartyManifest(&m))
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	if _, err := st.CreateEnrollmentToken(ctx, hash, "t", time.Now().Add(time.Hour), 1); err != nil {
		t.Fatal(err)
	}
	maq := registra(t, h, tok)
	checkin := func(req protocol.CheckinRequest) {
		t.Helper()
		req.AgentVersion, req.SentAt = "v", time.Now().UTC()
		corpo, _ := json.Marshal(req)
		if r := envia(h, "POST", "/api/v1/agent/checkin", maq.Credential, corpo); r.Code != 200 {
			t.Fatalf("check-in: %d", r.Code)
		}
	}
	checkin(protocol.CheckinRequest{Capabilities: []string{protocol.CapabilityJobs, protocol.CapabilityCloseApps, "Inválida!", protocol.CapabilityJobs}})
	if caps, _ := st.MachineCapabilities(ctx, maq.MachineID); strings.Join(caps, ",") != "fechar-programa-v1,jobs-v1" {
		t.Errorf("capacidades: %v", caps)
	}
	id, _ := st.NextJobID(ctx)
	j := jobs.Job{ID: id, MachineID: maq.MachineID, Type: jobs.TypeRescan, IssuedAt: time.Now().UTC(), NotAfter: time.Now().Add(time.Hour).UTC()}
	env, _ := jobs.Sign(priv, j)
	if err := st.CreateJob(ctx, store.NewJob{ID: id, MachineID: maq.MachineID, Type: j.Type, Signed: env, CreatedBy: "t", NotAfter: j.NotAfter}); err != nil {
		t.Fatal(err)
	}
	st.DeliverJobs(ctx, maq.MachineID, time.Now())
	st.FinishJob(ctx, id, jobs.StatePostponed, "programa aberto", time.Now())
	checkin(protocol.CheckinRequest{Capabilities: []string{protocol.CapabilityJobs}, Unblocked: []int64{id, 999}})
	if n, _ := st.MarkJobsUnblocked(ctx, maq.MachineID, []int64{id}, time.Now()); n != 0 {
		t.Error("o check-in não marcou o job")
	}
	if caps, _ := st.MachineCapabilities(ctx, maq.MachineID); strings.Join(caps, ",") != "jobs-v1" {
		t.Errorf("capacidades depois: %v", caps)
	}

	// O formulário: o campo, e "fechar" só com horário.
	b := pagina(h, "GET", "/painel/campanhas", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, `class="campo so-fechar"`) || !strings.Contains(b, `<option value="15" >fechar com aviso de 15 min</option>`) {
		t.Fatalf("formulário sem o campo:\n%s", b)
	}
	f := formCampanha("tipo", "terceiros_todos", "nome", "terceiros", "fechar", "15")
	if b := pagina(h, "POST", "/painel/campanhas/nova", sessaoAdmin, f).Body.String(); !strings.Contains(b, "Fechar o programa aberto só em horário fixo") {
		t.Errorf("com prazo:\n%s", b)
	}
	f = formCampanha("tipo", "terceiros_todos", "nome", "terceiros", "fechar", "45", "quando", "horario", "hora", "02:00", "duracao", "180")
	if b := pagina(h, "POST", "/painel/campanhas/nova", sessaoAdmin, f).Body.String(); !strings.Contains(b, "Escolha o aviso antes de fechar o programa da lista") {
		t.Errorf("aviso fora da lista:\n%s", b)
	}
	f.Set("fechar", "15")
	b = pagina(h, "POST", "/painel/campanhas/nova", sessaoAdmin, f).Body.String()
	if !strings.Contains(b, "programa aberto: fecha com aviso de 15 min") || !strings.Contains(b, `<input type="hidden" name="fechar" value="15">`) ||
		!strings.Contains(b, "fecha à força: o que não foi salvo se perde") || !strings.Contains(b, "assim que o programa fechar, o agente avisa") {
		t.Fatalf("conferência:\n%s", b)
	}
	f.Set("confirmar", "1")
	pagina(h, "POST", "/painel/campanhas/nova", sessaoAdmin, f)
	l, _ := st.Campaigns(ctx, 5)
	if len(l) != 1 || l[0].CloseDelay != 15*time.Minute {
		t.Fatalf("campanha: %+v", l)
	}
	if b := pagina(h, "GET", "/painel/campanhas/"+itoa(l[0].ID), sessaoAdmin, nil).Body.String(); !strings.Contains(b, "<dt>Programa aberto</dt><dd>fecha com aviso de 15 min (no horário da campanha)</dd>") {
		t.Errorf("detalhe:\n%s", b)
	}
	// Em outro tipo, o campo é ignorado.
	f = formCampanha("tipo", "rescan", "nome", "busca", "fechar", "15", "confirmar", "1")
	pagina(h, "POST", "/painel/campanhas/nova", sessaoAdmin, f)
	if l, _ = st.Campaigns(ctx, 5); len(l) != 2 || l[0].CloseDelay != 0 {
		t.Errorf("rescan: %+v", l)
	}
}
