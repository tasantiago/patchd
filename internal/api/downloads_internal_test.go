package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/settings"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/wucache"
)

// Os downloads em andamento: o total, as máquinas distintas, por tipo, e o pico com a hora.
func TestDownloadsEmAndamento(t *testing.T) {
	var d downloadTracker
	data := func(t time.Time) string { return t.Format("02/01 15:04") }
	if got := downloadsRow(d.stats(), data).Value; got != "nenhum em andamento · sem limite" {
		t.Errorf("vazio: %q", got)
	}
	t0 := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	fimA := d.begin(dlWinCache, "pc-a", t0)
	fimA2 := d.begin(dlWinCache, "pc-a", t0.Add(time.Minute)) // a mesma máquina, dois arquivos
	fimB := d.begin(dlRelease, "pc-b", t0.Add(2*time.Minute))
	s := d.stats()
	if s.Total != 3 || s.Machines != 2 || s.ByKind[dlWinCache] != 2 || s.ByKind[dlRelease] != 1 || s.Peak != 3 || !s.PeakAt.Equal(t0.Add(2*time.Minute)) {
		t.Fatalf("três em andamento: %+v", s)
	}
	if got, quer := downloadsRow(s, data).Value, "3 em andamento, de 2 máquina(s): 2 do cache do Windows, 1 de versões do agente · pico desde a partida: 3 (17/11 09:02) · sem limite"; got != quer {
		t.Errorf("linha:\n%q\nqueria\n%q", got, quer)
	}
	fimA()
	fimA() // o fim contado duas vezes não desconta duas
	if s := d.stats(); s.Total != 2 || s.Machines != 2 {
		t.Errorf("depois de um fim: %+v", s)
	}
	fimA2()
	fimB()
	s = d.stats()
	if s.Total != 0 || s.Machines != 0 || s.Peak != 3 {
		t.Errorf("no fim: %+v", s)
	}
	if got := downloadsRow(s, data).Value; got != "nenhum em andamento · pico desde a partida: 3 (17/11 09:02) · sem limite" {
		t.Errorf("no fim: %q", got)
	}

	// O limite: passou do número, recusa (e conta); os scripts não contam nem são limitados.
	var l downloadTracker
	f1, ok1 := l.tryBegin(dlWinCache, "pc-a", t0, 2)
	f2, ok2 := l.tryBegin(dlRelease, "pc-b", t0, 2)
	fs, oks := l.tryBegin(dlBundle, "pc-c", t0, 2) // scripts: passam e não ocupam vaga
	if !ok1 || !ok2 || !oks {
		t.Fatal("dentro do limite")
	}
	if _, ok := l.tryBegin(dlPackage, "pc-d", t0, 2); ok {
		t.Fatal("passou do limite")
	}
	f1()
	f3, ok3 := l.tryBegin(dlPackage, "pc-d", t0, 2)
	if !ok3 {
		t.Fatal("a vaga solta não voltou")
	}
	if _, ok := l.tryBegin(dlWinCache, "pc-e", t0, 0); !ok {
		t.Error("0 é sem limite")
	}
	s = l.stats()
	s.Max = 2
	if s.Refused != 1 || !strings.Contains(downloadsRow(s, data).Value, "limite de 2 ao mesmo tempo · 1 recusado(s) pelo limite") {
		t.Errorf("linha com o limite: %q", downloadsRow(s, data).Value)
	}
	f2()
	f3()
	fs()
}

// Na rota: o download de um arquivo do cache do Windows entra na conta e sai quando termina.
func TestDownloadContadoNaRota(t *testing.T) {
	ctx := context.Background()
	origem := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("pacote ", 100))
	}))
	defer origem.Close()
	c, err := wucache.New(t.TempDir(), "teste", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	c.Origin = func(u *url.URL) []string { return []string{origem.URL + u.Path} }
	st := store.NewMemory()
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	if _, err := st.CreateEnrollmentToken(ctx, hash, "teste", time.Now().Add(time.Hour), 5); err != nil {
		t.Fatal(err)
	}
	var a *api
	h := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), WithWinCache(c), func(x *api) { a = x })
	envia := func(method, path, bearer string, body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+bearer)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	corpo, _ := json.Marshal(protocol.EnrollRequest{AgentVersion: "v0.4.0-teste",
		Evidence: protocol.IdentityEvidence{InstallID: "guid-1", Hostname: "pc-teste", OSFamily: "windows"}})
	var m protocol.EnrollResponse
	if r := envia("POST", "/api/v1/enroll", tok, corpo); r.Code != http.StatusCreated || json.Unmarshal(r.Body.Bytes(), &m) != nil {
		t.Fatalf("enrollment: %d", r.Code)
	}
	pedido, _ := json.Marshal(protocol.WinCacheRequest{URLs: []string{"http://download.windowsupdate.com/c/a.msu"}})
	var resp protocol.WinCacheResponse
	for i := 0; i < 300; i++ {
		r := envia("POST", protocol.WinCachePath, m.Credential, pedido)
		if r.Code != http.StatusOK || json.Unmarshal(r.Body.Bytes(), &resp) != nil {
			t.Fatalf("pedido: %d %s", r.Code, r.Body)
		}
		if resp.Files[0].State == wucache.StateReady {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if resp.Files[0].State != wucache.StateReady {
		t.Fatalf("arquivo: %+v", resp.Files[0])
	}
	// Um arquivo que não existe não conta (nada foi transferido).
	if r := envia("GET", protocol.WinCachePath+"/0123456789abcdef-nada.cab", m.Credential, nil); r.Code != http.StatusNotFound || a.downloads.stats().Peak != 0 {
		t.Fatalf("arquivo que não existe: %d, pico %d", r.Code, a.downloads.stats().Peak)
	}
	if r := envia("GET", protocol.WinCachePath+"/"+resp.Files[0].Key, m.Credential, nil); r.Code != http.StatusOK {
		t.Fatalf("download: %d", r.Code)
	}
	if s := a.downloads.stats(); s.Peak != 1 || s.Total != 0 || s.Machines != 0 {
		t.Errorf("depois do download: %+v", s)
	}

	// Com o limite do parâmetro (vale na hora): outra máquina segurando as vagas, o download
	// recebe "ocupado" com Retry-After, sem transferir; com a vaga solta, baixa.
	params := settings.New([]settings.Def{{Key: DownloadMaxKey, Group: "Agentes", Label: "Downloads ao mesmo tempo",
		Default: "5", Check: func(v string) (string, error) { return strings.TrimSpace(v), nil }}},
		func(string) (string, bool) { return "", false }, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	_ = params.Refresh(ctx)
	a.params = params
	if _, err := params.Apply(ctx, map[string]string{DownloadMaxKey: "5"}, "admin"); err != nil {
		t.Fatal(err)
	}
	var presos []func()
	for range 5 {
		f, _ := a.downloads.tryBegin(dlRelease, "outra", time.Now(), 5)
		presos = append(presos, f)
	}
	r := envia("GET", protocol.WinCachePath+"/"+resp.Files[0].Key, m.Credential, nil)
	if r.Code != http.StatusServiceUnavailable || r.Header().Get("Retry-After") == "" || !strings.Contains(r.Body.String(), `"busy"`) {
		t.Errorf("acima do limite: %d %q %.80s", r.Code, r.Header().Get("Retry-After"), r.Body)
	}
	presos[0]()
	if r := envia("GET", protocol.WinCachePath+"/"+resp.Files[0].Key, m.Credential, nil); r.Code != http.StatusOK {
		t.Errorf("com a vaga solta: %d", r.Code)
	}
	if _, err := params.Apply(ctx, map[string]string{DownloadMaxKey: "0"}, "admin"); err != nil {
		t.Fatal(err)
	}
	if r := envia("GET", protocol.WinCachePath+"/"+resp.Files[0].Key, m.Credential, nil); r.Code != http.StatusOK {
		t.Errorf("sem limite: %d", r.Code)
	}
	for _, f := range presos[1:] {
		f()
	}
	if s := a.downloads.stats(); s.Refused != 1 || s.Total != 0 {
		t.Errorf("no fim: %+v", s)
	}
}
