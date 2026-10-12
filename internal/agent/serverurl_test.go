package agent

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

// doisCanais é o servidor da transição (Aula 9.2): o mesmo handler e o mesmo banco numa
// porta HTTP e numa HTTPS.
func doisCanais(t *testing.T) (st *store.Memory, httpSrv, tlsSrv *httptest.Server, id, cred string) {
	t.Helper()
	st = store.NewMemory()
	h := api.New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	httpSrv, tlsSrv = httptest.NewServer(h), httptest.NewTLSServer(h)
	t.Cleanup(httpSrv.Close)
	t.Cleanup(tlsSrv.Close)
	ctx := context.Background()
	_, th := identity.NewSecret(identity.EnrollmentPrefix)
	if _, err := st.CreateEnrollmentToken(ctx, th, "teste", time.Now().Add(time.Hour), 1); err != nil {
		t.Fatal(err)
	}
	cred, ch := identity.NewSecret(identity.CredentialPrefix)
	id = identity.NewMachineID()
	if _, err := st.Enroll(ctx, th, identity.NewMachine{ID: id, CredentialHash: ch}); err != nil {
		t.Fatal(err)
	}
	return st, httpSrv, tlsSrv, id, cred
}

func TestTrocaParaHTTPS(t *testing.T) {
	st, httpSrv, tlsSrv, id, cred := doisCanais(t)
	pub, priv, _ := jobs.GenerateKey()
	ctx := context.Background()
	pede := func(url string) int64 {
		t.Helper()
		params, err := jobs.ServerURLParamsJSON(url)
		if err != nil {
			t.Fatal(err)
		}
		rec, err := jobs.Issue(ctx, st, priv, jobs.Request{MachineID: id, Type: jobs.TypeServerURL, TTL: time.Hour, By: "teste", Params: params}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return rec.ID
	}
	estado := func(job int64) store.JobRow {
		j, _, _ := st.JobForMachine(ctx, id, job)
		return j
	}
	canal := func() string {
		v, _ := st.CheckinTLS(ctx, id)
		switch {
		case v == nil:
			return "nenhum"
		case *v:
			return "https"
		}
		return "http"
	}

	var buscas atomic.Int32
	log := &bytes.Buffer{}
	a := &Agent{
		Client:    NewClient(httpSrv.URL, cred, "v-teste"),
		Inventory: func(context.Context) (protocol.InventoryReport, error) { return inventarioDeTeste(), nil },
		Scan: func(context.Context) protocol.PatchScanReport {
			buscas.Add(1)
			return protocol.PatchScanReport{SchemaVersion: protocol.PatchSchemaVersion, ScannedAt: time.Now().UTC(), Scans: []protocol.ScanResult{{Source: "wua-default"}}}
		},
		DataDir: t.TempDir(), Interval: time.Hour, ScanEvery: 24 * time.Hour,
		Logger:    slog.New(slog.NewTextHandler(log, nil)),
		MachineID: id, JobKey: pub,
	}
	var gravadas []string
	a.SaveServerURL = func(u string) error { gravadas = append(gravadas, u); return nil }

	if out := a.CheckIn(ctx); out != outcomeOK || canal() != "http" {
		t.Fatalf("primeiro ciclo: %v, canal %s", out, canal())
	}

	// 1. O agente não confia na CA do servidor novo: a troca falha, ele fica no HTTP e o
	// motivo (o certificado) chega pelo endereço atual.
	j1 := pede(tlsSrv.URL)
	if out := a.CheckIn(ctx); out != outcomeOK {
		t.Fatalf("ciclo da troca sem CA: %v", out)
	}
	if e := estado(j1); e.State != jobs.StateFailed || !strings.Contains(e.Detail, "certificate") || !strings.Contains(e.Detail, "continua em "+httpSrv.URL) {
		t.Errorf("sem CA: %s %q", e.State, e.Detail)
	}
	if a.Client.BaseURL != httpSrv.URL || len(gravadas) != 0 {
		t.Errorf("sem CA, o agente não troca: %s %v", a.Client.BaseURL, gravadas)
	}

	// 2. Com a CA (o -ca-file faz isso no agente de verdade): troca, grava e o próximo
	// check-in já chega por TLS.
	a.Client.HTTP = tlsSrv.Client()
	a.Client.HTTP.Timeout = 10 * time.Second
	j2 := pede(tlsSrv.URL)
	if out := a.CheckIn(ctx); out != outcomeOK {
		t.Fatalf("ciclo da troca: %v", out)
	}
	if e := estado(j2); e.State != jobs.StateDone || !strings.Contains(e.Detail, "por HTTPS em "+tlsSrv.URL) {
		t.Errorf("troca: %s %q", e.State, e.Detail)
	}
	if a.Client.BaseURL != tlsSrv.URL || len(gravadas) != 1 || gravadas[0] != tlsSrv.URL {
		t.Errorf("depois da troca: %s %v", a.Client.BaseURL, gravadas)
	}
	if canal() != "http" {
		t.Errorf("o check-in deste ciclo ainda foi por HTTP: %s", canal())
	}
	a.CheckIn(ctx)
	if canal() != "https" {
		t.Errorf("o check-in seguinte vai por HTTPS: %s", canal())
	}

	// 3. Pedido repetido para o endereço atual: "já usa", concluído (chegou por TLS).
	j3 := pede(tlsSrv.URL)
	a.CheckIn(ctx)
	if e := estado(j3); e.State != jobs.StateDone || len(gravadas) != 1 {
		t.Errorf("já usa: %s %q, gravações %d", e.State, e.Detail, len(gravadas))
	}
	if !strings.Contains(log.String(), "endereço do servidor trocado") {
		t.Errorf("log sem a troca:\n%s", log.String())
	}
}

// TestTrocaSoComTLS: o servidor não aceita um "ok" de troca que chega por HTTP, nem um que
// chega por TLS em outro host.
func TestTrocaSoComTLS(t *testing.T) {
	st, httpSrv, tlsSrv, id, cred := doisCanais(t)
	_, priv, _ := jobs.GenerateKey()
	ctx := context.Background()
	cria := func(url string) int64 {
		params, _ := jobs.ServerURLParamsJSON(url)
		rec, err := jobs.Issue(ctx, st, priv, jobs.Request{MachineID: id, Type: jobs.TypeServerURL, TTL: time.Hour, By: "teste", Params: params}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		_, _ = st.DeliverJobs(ctx, id, time.Now())
		return rec.ID
	}
	ok := protocol.JobResult{Status: protocol.JobResultOK, Detail: "troquei", FinishedAt: time.Now().UTC()}

	j1 := cria(tlsSrv.URL)
	if _, err := NewClient(httpSrv.URL, cred, "v").SendJobResult(ctx, j1, ok); err != nil {
		t.Fatal(err)
	}
	if j, _, _ := st.JobForMachine(ctx, id, j1); j.State != jobs.StateFailed || !strings.Contains(j.Detail, "sem TLS") {
		t.Errorf("ok por HTTP: %s %q", j.State, j.Detail)
	}

	j2 := cria("https://outro.exemplo:8443")
	c := NewClient(tlsSrv.URL, cred, "v")
	c.HTTP = tlsSrv.Client()
	if _, err := c.SendJobResult(ctx, j2, ok); err != nil {
		t.Fatal(err)
	}
	if j, _, _ := st.JobForMachine(ctx, id, j2); j.State != jobs.StateFailed || !strings.Contains(j.Detail, "não no endereço pedido") {
		t.Errorf("ok por TLS em outro host: %s %q", j.State, j.Detail)
	}
}
