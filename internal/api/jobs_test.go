package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

func TestJobsNoCheckinENoResultado(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	if _, err := st.CreateEnrollmentToken(ctx, hash, "t", time.Now().Add(time.Hour), 5); err != nil {
		t.Fatal(err)
	}
	h := api.New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	a, b := registra(t, h, tok), registra(t, h, tok)
	_, priv, _ := jobs.GenerateKey()
	cria := func(maq string) int64 {
		t.Helper()
		id, _ := st.NextJobID(ctx)
		agora := time.Now().UTC()
		env, err := jobs.Sign(priv, jobs.Job{ID: id, MachineID: maq, Type: jobs.TypeRescan, IssuedAt: agora, NotAfter: agora.Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		if err := st.CreateJob(ctx, store.NewJob{ID: id, MachineID: maq, Type: jobs.TypeRescan, Signed: env, CreatedBy: "teste", NotAfter: agora.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	checkinCom := func(cred string, caps ...string) protocol.CheckinResponse {
		t.Helper()
		corpo, _ := json.Marshal(protocol.CheckinRequest{AgentVersion: "v", SentAt: time.Now().UTC(), Capabilities: caps})
		r := envia(h, "POST", "/api/v1/agent/checkin", cred, corpo)
		var resp protocol.CheckinResponse
		_ = json.Unmarshal(r.Body.Bytes(), &resp)
		return resp
	}
	resultado := func(cred string, id int64, status string) (int, string) {
		t.Helper()
		corpo, _ := json.Marshal(protocol.JobResult{Status: status, Detail: "teste", FinishedAt: time.Now().UTC()})
		r := envia(h, "POST", protocol.JobResultPath+strconv.FormatInt(id, 10)+"/result", cred, corpo)
		var sr protocol.SubmitResponse
		_ = json.Unmarshal(r.Body.Bytes(), &sr)
		return r.Code, sr.Status
	}
	estado := func(maq string, id int64) store.JobRow {
		j, _, _ := st.JobForMachine(ctx, maq, id)
		return j
	}

	j1 := cria(a.MachineID)
	// Agente antigo (sem a capacidade): nada é entregue, e o job continua pendente.
	if resp := checkinCom(a.Credential); len(resp.Jobs) != 0 || estado(a.MachineID, j1).State != jobs.StatePending {
		t.Fatalf("agente antigo: %+v", resp.Jobs)
	}
	resp := checkinCom(a.Credential, protocol.CapabilityJobs)
	if len(resp.Jobs) != 1 || jobs.ID(resp.Jobs[0]) != j1 || estado(a.MachineID, j1).State != jobs.StateDelivered {
		t.Fatalf("entrega: %+v", resp.Jobs)
	}
	if resp := checkinCom(a.Credential, protocol.CapabilityJobs); len(resp.Jobs) != 0 {
		t.Error("no máximo uma vez")
	}

	// "ok" sem busca nova: falhou (RF-08: o efeito decide, não o que o agente diz).
	if code, st := resultado(a.Credential, j1, protocol.JobResultOK); code != http.StatusOK || st != "stored" {
		t.Fatalf("resultado: %d %s", code, st)
	}
	if j := estado(a.MachineID, j1); j.State != jobs.StateFailed || j.Detail == "" {
		t.Errorf("sem busca: %+v", j)
	}
	// Repetido: nada muda.
	if _, st := resultado(a.Credential, j1, protocol.JobResultOK); st != "unchanged" {
		t.Errorf("repetido: %s", st)
	}

	// Com a busca enviada depois da entrega: concluido.
	j2 := cria(a.MachineID)
	checkinCom(a.Credential, protocol.CapabilityJobs)
	scan, _ := json.Marshal(protocol.PatchScanReport{SchemaVersion: protocol.PatchSchemaVersion, ScannedAt: time.Now().UTC()})
	if r := envia(h, "POST", "/api/v1/agent/scan", a.Credential, scan); r.Code != http.StatusCreated {
		t.Fatalf("busca: %d", r.Code)
	}
	resultado(a.Credential, j2, protocol.JobResultOK)
	if j := estado(a.MachineID, j2); j.State != jobs.StateDone {
		t.Errorf("com busca: %+v", j)
	}

	// Recusado pelo agente.
	j3 := cria(a.MachineID)
	checkinCom(a.Credential, protocol.CapabilityJobs)
	resultado(a.Credential, j3, protocol.JobResultRejected)
	if j := estado(a.MachineID, j3); j.State != jobs.StateRejected || j.Detail != "recusado pelo agente: teste" {
		t.Errorf("recusado: %+v", j)
	}

	// A credencial de uma máquina não fecha o job de outra; status inválido; sem credencial.
	jb := cria(b.MachineID)
	if code, _ := resultado(a.Credential, jb, protocol.JobResultOK); code != http.StatusNotFound {
		t.Errorf("job de outra máquina: %d", code)
	}
	if j := estado(b.MachineID, jb); j.State != jobs.StatePending {
		t.Errorf("o job da outra máquina não muda: %+v", j)
	}
	if code, _ := resultado(b.Credential, jb, "talvez"); code != http.StatusUnprocessableEntity {
		t.Errorf("status inválido: %d", code)
	}
	if code, _ := resultado("", jb, protocol.JobResultOK); code != http.StatusUnauthorized {
		t.Errorf("sem credencial: %d", code)
	}
	if r := envia(h, "POST", protocol.JobResultPath+"abc/result", b.Credential, []byte(`{"status":"ok"}`)); r.Code != http.StatusNotFound {
		t.Errorf("ID inválido: %d", r.Code)
	}
}
