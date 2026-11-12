package api_test

import (
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

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/protocol"
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
