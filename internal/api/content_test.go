package api_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/catalog/wsusscan"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

// conteudoFalso é a tabela content_files.
type conteudoFalso struct {
	f   wsusscan.File
	ok  bool
	err error
}

func (c *conteudoFalso) ContentFile(context.Context, string) (wsusscan.File, bool, error) {
	return c.f, c.ok, c.err
}

// ambienteConteudo monta a API com o diretório de conteúdo e um catálogo publicado nele.
func ambienteConteudo(t *testing.T) (http.Handler, protocol.EnrollResponse, *conteudoFalso, string, []byte) {
	t.Helper()
	dir := t.TempDir()
	dados := []byte("MSCF" + string(make([]byte, 5000)) + "assinatura")
	soma := sha256.Sum256(dados)
	sum := hex.EncodeToString(soma[:])
	if err := os.WriteFile(filepath.Join(dir, wsusscan.FileName(sum)), dados, 0o640); err != nil {
		t.Fatal(err)
	}
	pub := time.Date(2026, 10, 3, 1, 8, 4, 0, time.UTC)
	src := &conteudoFalso{ok: true, f: wsusscan.File{Name: wsusscan.FileName(sum), Size: int64(len(dados)), SHA256: sum, LastModified: pub}}

	st := store.NewMemory()
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	if _, err := st.CreateEnrollmentToken(context.Background(), hash, "teste", time.Now().Add(time.Hour), 5); err != nil {
		t.Fatal(err)
	}
	h := api.New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), api.WithContent(dir, src))
	return h, registra(t, h, tok), src, dir, dados
}

func checkin(t *testing.T, h http.Handler, cred string) protocol.CheckinResponse {
	t.Helper()
	corpo, _ := json.Marshal(protocol.CheckinRequest{AgentVersion: "v-teste", SentAt: time.Now().UTC()})
	r := envia(h, "POST", "/api/v1/agent/checkin", cred, corpo)
	if r.Code != http.StatusOK {
		t.Fatalf("check-in: %d %s", r.Code, r.Body)
	}
	var resp protocol.CheckinResponse
	if err := json.Unmarshal(r.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestCheckinAnunciaCatalogoOffline(t *testing.T) {
	h, m, src, dir, dados := ambienteConteudo(t)

	oc := checkin(t, h, m.Credential).OfflineCatalog
	if oc == nil || oc.SHA256 != src.f.SHA256 || oc.Size != int64(len(dados)) || !oc.PublishedAt.Equal(src.f.LastModified) {
		t.Fatalf("anúncio: %+v", oc)
	}

	// Sem linha na tabela, com erro no banco ou sem o arquivo no disco: check-in sem anúncio.
	src.ok = false
	if oc := checkin(t, h, m.Credential).OfflineCatalog; oc != nil {
		t.Errorf("sem catálogo baixado: %+v", oc)
	}
	src.ok, src.err = true, errors.New("banco fora")
	if oc := checkin(t, h, m.Credential).OfflineCatalog; oc != nil {
		t.Errorf("erro no banco: %+v", oc)
	}
	src.err = nil
	if err := os.Remove(filepath.Join(dir, src.f.Name)); err != nil {
		t.Fatal(err)
	}
	if oc := checkin(t, h, m.Credential).OfflineCatalog; oc != nil {
		t.Errorf("arquivo fora do disco: %+v", oc)
	}

	// Sem WithContent, nunca há anúncio.
	h2, tok := ambiente(t)
	if oc := checkin(t, h2, registra(t, h2, tok).Credential).OfflineCatalog; oc != nil {
		t.Errorf("servidor sem diretório de conteúdo: %+v", oc)
	}
}

func TestDownloadDoCatalogoOffline(t *testing.T) {
	h, m, src, _, dados := ambienteConteudo(t)
	caminho := protocol.OfflineCatalogPath + src.f.SHA256

	r := envia(h, "GET", caminho, m.Credential, nil)
	if r.Code != http.StatusOK || r.Body.Len() != len(dados) || r.Header().Get("ETag") != `"`+src.f.SHA256+`"` {
		t.Fatalf("download: %d, %d bytes, ETag %q", r.Code, r.Body.Len(), r.Header().Get("ETag"))
	}

	// Retomada: Range com If-Range igual ao ETag devolve só o resto (206).
	req := httptest.NewRequest("GET", caminho, nil)
	req.Header.Set("Authorization", "Bearer "+m.Credential)
	req.Header.Set("Range", "bytes=1000-")
	req.Header.Set("If-Range", `"`+src.f.SHA256+`"`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent || rec.Body.Len() != len(dados)-1000 {
		t.Errorf("retomada: %d, %d bytes", rec.Code, rec.Body.Len())
	}
	// If-Range de outro arquivo: o arquivo inteiro (200), nunca o pedaço.
	req.Header.Set("If-Range", `"`+hex.EncodeToString(make([]byte, 32))+`"`)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.Len() != len(dados) {
		t.Errorf("If-Range diferente: %d, %d bytes", rec.Code, rec.Body.Len())
	}

	// Sem credencial: 401. Hash desconhecido ou fora do formato: 404.
	if r := envia(h, "GET", caminho, "", nil); r.Code != http.StatusUnauthorized {
		t.Errorf("sem credencial: %d", r.Code)
	}
	outro := hex.EncodeToString(make([]byte, 32))
	for _, p := range []string{outro, src.f.SHA256[:16], "..%2F..%2Fetc%2Fpasswd"} {
		if r := envia(h, "GET", protocol.OfflineCatalogPath+p, m.Credential, nil); r.Code != http.StatusNotFound {
			t.Errorf("%s: %d", p, r.Code)
		}
	}
}

func TestCheckinAnunciaCacheDeRepositorios(t *testing.T) {
	st := store.NewMemory()
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	if _, err := st.CreateEnrollmentToken(context.Background(), hash, "teste", time.Now().Add(time.Hour), 2); err != nil {
		t.Fatal(err)
	}
	h := api.New(st, slog.New(slog.NewTextHandler(io.Discard, nil)),
		api.WithRepoCache("http://patchd.exemplo:8080", []string{"security.ubuntu.com", "archive.ubuntu.com"}))
	rc := checkin(t, h, registra(t, h, tok).Credential).RepoCache
	if rc == nil || rc.URL != "http://patchd.exemplo:8080" || len(rc.AptHosts) != 2 || rc.AptHosts[0] != "archive.ubuntu.com" {
		t.Errorf("anúncio: %+v", rc)
	}
	h2, tok2 := ambiente(t)
	if rc := checkin(t, h2, registra(t, h2, tok2).Credential).RepoCache; rc != nil {
		t.Errorf("sem WithRepoCache: %+v", rc)
	}
}
