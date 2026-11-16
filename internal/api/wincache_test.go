package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/settings"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/wucache"
)

// TestCacheDoWindowsPelaAPI (docs/cache-windows.md): o agente pede os arquivos, o servidor os
// baixa uma vez da origem e os entrega com retomada; endereço de fora é recusado; sem
// credencial, nada.
func TestCacheDoWindowsPelaAPI(t *testing.T) {
	ctx := context.Background()
	conteudo := strings.Repeat("pacote ", 10000)
	origem := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "a.msu", time.Time{}, strings.NewReader(conteudo))
	}))
	defer origem.Close()
	c, err := wucache.New(t.TempDir(), "teste", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	c.Origin = func(u *url.URL) []string { return []string{origem.URL + u.Path} }
	h, st, _ := ambientePaginas(t, api.WithWinCache(c))
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	if _, err := st.CreateEnrollmentToken(ctx, hash, "t", time.Now().Add(time.Hour), 1); err != nil {
		t.Fatal(err)
	}
	m := registra(t, h, tok)
	pede := func(urls ...string) protocol.WinCacheResponse {
		t.Helper()
		corpo, _ := json.Marshal(protocol.WinCacheRequest{URLs: urls})
		r := envia(h, "POST", protocol.WinCachePath, m.Credential, corpo)
		if r.Code != http.StatusOK {
			t.Fatalf("pedido: %d %s", r.Code, r.Body)
		}
		var resp protocol.WinCacheResponse
		_ = json.Unmarshal(r.Body.Bytes(), &resp)
		return resp
	}
	const bom = "http://download.windowsupdate.com/c/msdownload/update/software/secu/2026/11/windows11.0-kb5070001-x64.msu"
	var resp protocol.WinCacheResponse
	for i := 0; i < 200; i++ {
		resp = pede(bom, "http://exemplo.com/virus.exe")
		if resp.Files[0].State != wucache.StateFetching {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	f := resp.Files[0]
	if f.State != wucache.StateReady || f.Size != int64(len(conteudo)) || len(f.SHA256) != 64 {
		t.Fatalf("arquivo: %+v", f)
	}
	if resp.Files[1].State != wucache.StateRefused {
		t.Errorf("host de fora: %+v", resp.Files[1])
	}
	// O download, inteiro e retomado.
	req := httptest.NewRequest("GET", protocol.WinCachePath+"/"+f.Key, nil)
	req.Header.Set("Authorization", "Bearer "+m.Credential)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != conteudo || rec.Header().Get("X-Patchd-Sha256") != f.SHA256 {
		t.Errorf("download: %d, %d bytes", rec.Code, rec.Body.Len())
	}
	req = httptest.NewRequest("GET", protocol.WinCachePath+"/"+f.Key, nil)
	req.Header.Set("Authorization", "Bearer "+m.Credential)
	req.Header.Set("Range", "bytes=100-")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent || rec.Body.String() != conteudo[100:] {
		t.Errorf("retomada: %d", rec.Code)
	}
	// O prazo de escrita anda a cada escrita: os .wim passam de 1 GB, e o WriteTimeout de 60 s
	// do servidor cortava o download no meio.
	req = httptest.NewRequest("GET", protocol.WinCachePath+"/"+f.Key, nil)
	req.Header.Set("Authorization", "Bearer "+m.Credential)
	prazo := &comPrazo{ResponseRecorder: httptest.NewRecorder()}
	h.ServeHTTP(prazo, req)
	if prazo.Code != http.StatusOK || prazo.vezes < 2 {
		t.Errorf("download sem estender o prazo de escrita: %d, %d vezes", prazo.Code, prazo.vezes)
	}
	// Sem credencial, e chave fora do formato.
	if r := httptest.NewRecorder(); func() int {
		h.ServeHTTP(r, httptest.NewRequest("GET", protocol.WinCachePath+"/"+f.Key, nil))
		return r.Code
	}() != http.StatusUnauthorized {
		t.Error("baixou sem credencial")
	}
	req = httptest.NewRequest("GET", protocol.WinCachePath+"/..%2F..%2Fetc%2Fpasswd", nil)
	req.Header.Set("Authorization", "Bearer "+m.Credential)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("chave fora do formato: %d", rec.Code)
	}
	// O cartão da aba Sistema.
	if b := pagina(h, "GET", "/painel/configuracao", sessaoAdmin, nil).Body.String(); !strings.Contains(b, "Cache do Windows Update") ||
		!strings.Contains(b, "windows11.0-kb5070001-x64.msu") || !strings.Contains(b, "desligado: as máquinas seguem") {
		t.Error("cartão do cache na aba Sistema")
	}
}

// WinCacheFor: só com o parâmetro, a capacidade do agente e, no modo piloto, o grupo piloto.
func TestQuemUsaOCacheDoWindows(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	for _, id := range []string{"m-piloto", "m-outra", "m-antiga"} {
		if _, err := st.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: id}); err != nil {
			t.Fatal(err)
		}
	}
	_ = st.SetMachineCapabilities(ctx, "m-piloto", []string{protocol.CapabilityWinCache})
	_ = st.SetMachineCapabilities(ctx, "m-outra", []string{protocol.CapabilityWinCache})
	_, _ = st.SetMachineRing(ctx, "m-piloto", store.PilotRing)
	_, _ = st.SetMachineRing(ctx, "m-antiga", store.PilotRing)
	for _, c := range []struct {
		mode, id string
		quer     bool
	}{
		{api.WinCacheOff, "m-piloto", false},
		{api.WinCachePilot, "m-piloto", true},
		{api.WinCachePilot, "m-outra", false},
		{api.WinCachePilot, "m-antiga", false}, // agente sem a capacidade
		{api.WinCacheAll, "m-outra", true},
		{api.WinCacheAll, "m-antiga", false},
		{"qualquer", "m-piloto", false},
	} {
		if got, err := api.WinCacheFor(ctx, c.mode, st, c.id); err != nil || got != c.quer {
			t.Errorf("%s/%s: %v %v", c.mode, c.id, got, err)
		}
	}
}

// comPrazo conta os SetWriteDeadline que o http.ResponseController faz chegar à conexão.
type comPrazo struct {
	*httptest.ResponseRecorder
	vezes int
}

func (c *comPrazo) SetWriteDeadline(time.Time) error { c.vezes++; return nil }

// O horário para baixar da Microsoft: fora dele, o pedido da máquina volta "agendado", com a
// hora em que abre (no texto e entre colchetes, para a campanha), e o cartão mostra a fila;
// dentro dele, a fila é baixada e o mesmo pedido encontra o arquivo pronto.
func TestHorarioParaBaixarDaMicrosoft(t *testing.T) {
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
	st0 := store.NewMemory()
	hhmm := func(t time.Time) string { return t.UTC().Format("15:04") }
	agora := time.Now()
	fechado := hhmm(agora.Add(2*time.Hour)) + "-" + hhmm(agora.Add(3*time.Hour))
	aberto := hhmm(agora.Add(-time.Hour)) + "-" + hhmm(agora.Add(2*time.Hour))
	params := settings.New([]settings.Def{{Key: api.WinCacheFetchKey, Group: "Agentes", Label: "Horário para baixar da Microsoft",
		Check: func(v string) (string, error) {
			w, err := api.ParseFetchHours(v, time.UTC)
			return api.FetchHoursText(w), err
		}}}, func(string) (string, bool) { return "", false }, st0, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := params.Apply(ctx, map[string]string{api.WinCacheFetchKey: fechado}, "admin"); err != nil {
		t.Fatal(err)
	}
	h, st, _ := ambientePaginas(t, api.WithWinCache(c), api.WithSettings(params))
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	if _, err := st.CreateEnrollmentToken(ctx, hash, "t", time.Now().Add(time.Hour), 1); err != nil {
		t.Fatal(err)
	}
	m := registra(t, h, tok)
	const arq = "http://download.windowsupdate.com/c/msdownload/windows11.0-kb5070009-x64.msu"
	pede := func() protocol.WinCacheFile {
		t.Helper()
		corpo, _ := json.Marshal(protocol.WinCacheRequest{URLs: []string{arq}})
		r := envia(h, "POST", protocol.WinCachePath, m.Credential, corpo)
		var resp protocol.WinCacheResponse
		if r.Code != http.StatusOK || json.Unmarshal(r.Body.Bytes(), &resp) != nil || len(resp.Files) != 1 {
			t.Fatalf("pedido: %d %s", r.Code, r.Body)
		}
		return resp.Files[0]
	}
	f := pede()
	if f.State != wucache.StateScheduled || !strings.Contains(f.Detail, api.ScheduledMark) ||
		!regexp.MustCompile(`a partir de \d\d/\d\d \d\d:\d\d \[\d{4}-\d\d-\d\dT\d\d:\d\d:00Z\]$`).MatchString(f.Detail) {
		t.Fatalf("fora do horário: %+v", f)
	}
	b := pagina(h, "GET", "/painel/configuracao", sessaoAdmin, nil).Body.String()
	for _, quer := range []string{"só das " + fechado, "fechado agora, abre em", "Na fila para baixar da Microsoft", "windows11.0-kb5070009-x64.msu"} {
		if !strings.Contains(b, quer) {
			t.Errorf("cartão sem %q", quer)
		}
	}
	// O horário abre (o parâmetro vale na hora): a fila é baixada.
	if _, err := params.Apply(ctx, map[string]string{api.WinCacheFetchKey: aberto}, "admin"); err != nil {
		t.Fatal(err)
	}
	if n := c.Drain(); n != 1 {
		t.Fatalf("horário aberto: %d download(s) começados", n)
	}
	for i := 0; i < 200 && f.State != wucache.StateReady; i++ {
		time.Sleep(10 * time.Millisecond)
		f = pede()
	}
	if f.State != wucache.StateReady {
		t.Fatalf("depois do horário: %+v", f)
	}
	if b := pagina(h, "GET", "/painel/configuracao", sessaoAdmin, nil).Body.String(); !strings.Contains(b, "aberto agora") || strings.Contains(b, "Na fila para baixar") {
		t.Error("cartão com o horário aberto")
	}
}

// ParseFetchHours: o formato, os limites e a volta pela meia-noite.
func TestHorarioDoCacheFormato(t *testing.T) {
	for v, quer := range map[string]string{"": "", " 22:00-06:00 ": "22:00-06:00", "01:30-02:00": "01:30-02:00", "00:00-23:00": "00:00-23:00", "2:00-5:00": "02:00-05:00"} {
		w, err := api.ParseFetchHours(v, time.UTC)
		if err != nil || api.FetchHoursText(w) != quer {
			t.Errorf("%q: %q %v", v, api.FetchHoursText(w), err)
		}
	}
	for _, v := range []string{"22:00", "22:00-22:00", "22:00-22:10", "00:00-23:30", "25:00-02:00", "22h-06h", "a-b", "22:00-06:00-07:00"} {
		if _, err := api.ParseFetchHours(v, time.UTC); err == nil {
			t.Errorf("%q aceito", v)
		}
	}
	w, _ := api.ParseFetchHours("22:00-06:00", time.UTC)
	dia := time.Date(2026, 11, 16, 23, 30, 0, 0, time.UTC)
	if s, e := w.Next(dia, time.Minute); !s.Equal(time.Date(2026, 11, 16, 22, 0, 0, 0, time.UTC)) || !e.Equal(time.Date(2026, 11, 17, 6, 0, 0, 0, time.UTC)) {
		t.Errorf("janela pela meia-noite: %v %v", s, e)
	}
}
