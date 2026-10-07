package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/catalog/wsusscan"
	"github.com/tasantiago/patchd/internal/protocol"
)

type conteudo struct{ f wsusscan.File }

func (c *conteudo) ContentFile(context.Context, string) (wsusscan.File, bool, error) {
	return c.f, c.f.SHA256 != "", nil
}

// publica grava um catálogo no diretório de conteúdo do servidor e devolve o anúncio.
func publica(t *testing.T, dir string, c *conteudo, dados []byte) protocol.OfflineCatalog {
	t.Helper()
	s := sha256.Sum256(dados)
	sum := hex.EncodeToString(s[:])
	if err := os.WriteFile(filepath.Join(dir, wsusscan.FileName(sum)), dados, 0o640); err != nil {
		t.Fatal(err)
	}
	c.f = wsusscan.File{Name: wsusscan.FileName(sum), Size: int64(len(dados)), SHA256: sum, LastModified: time.Now().UTC()}
	return protocol.OfflineCatalog{SHA256: sum, Size: int64(len(dados)), PublishedAt: c.f.LastModified}
}

// assinatura simula o WinVerifyTrust: aceita os arquivos que terminam com "MICROSOFT".
type assinatura struct {
	mu     sync.Mutex
	vistos []string
}

func (a *assinatura) verifica(path string) (string, error) {
	a.mu.Lock()
	a.vistos = append(a.vistos, filepath.Base(path))
	a.mu.Unlock()
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if !bytes.HasSuffix(b, []byte("MICROSOFT")) {
		return "Google LLC <- Raiz Qualquer", errors.New("assinado por \"Google LLC\", não pela Microsoft")
	}
	return "Microsoft Corporation <- Microsoft Root Certificate Authority 2011", nil
}

func catalogo(n int, fim string) []byte {
	b := bytes.Repeat([]byte("x"), n)
	return append(b, fim...)
}

func cabDeTeste(t *testing.T, opts ...api.Option) (*servidor, *OfflineCab, *assinatura, *bytes.Buffer) {
	t.Helper()
	srv, _, cred := novoServidor(t, opts...)
	ass := &assinatura{}
	var log bytes.Buffer
	cab := &OfflineCab{Client: NewClient(srv.URL, cred, "v-teste"), DataDir: t.TempDir(), Verify: ass.verifica,
		Logger: slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	return srv, cab, ass, &log
}

func TestOfflineCabBaixaConfereEReaproveita(t *testing.T) {
	dir, c := t.TempDir(), &conteudo{}
	srv, cab, ass, _ := cabDeTeste(t, api.WithContent(dir, c))
	adv := publica(t, dir, c, catalogo(300000, "MICROSOFT"))
	ctx := context.Background()

	if cab.Path() != "" {
		t.Fatal("sem download, não há catálogo")
	}
	if err := cab.Sync(ctx, adv); err != nil {
		t.Fatal(err)
	}
	p := cab.Path()
	if filepath.Base(p) != "wsusscn2-"+adv.SHA256[:16]+".cab" || len(ass.vistos) != 1 {
		t.Fatalf("Path = %q, conferências %v", p, ass.vistos)
	}
	if st := cab.State(); st.Signer == "" || st.VerifiedAt.IsZero() {
		t.Errorf("estado: %+v", st)
	}
	// O mesmo anúncio de novo: nenhum pedido ao servidor.
	caminho := protocol.OfflineCatalogPath + adv.SHA256
	if err := cab.Sync(ctx, adv); err != nil || srv.conta(caminho) != 1 {
		t.Errorf("segundo Sync: %v, %d pedidos", err, srv.conta(caminho))
	}

	// Catálogo novo no servidor: baixa, troca e apaga o anterior.
	adv2 := publica(t, dir, c, catalogo(200000, "MICROSOFT"))
	if err := cab.Sync(ctx, adv2); err != nil {
		t.Fatal(err)
	}
	if cab.Path() == p || cab.Path() == "" {
		t.Errorf("Path depois do novo: %q", cab.Path())
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("o catálogo anterior ficou no disco: %v", err)
	}
	sobras, _ := filepath.Glob(filepath.Join(cab.dir(), "*.part*"))
	if len(sobras) != 0 {
		t.Errorf("sobraram arquivos do download: %v", sobras)
	}
}

func TestOfflineCabRetomaDeOndeParou(t *testing.T) {
	dir, c := t.TempDir(), &conteudo{}
	var ranges []string
	var mu sync.Mutex
	srv, cab, _, _ := cabDeTeste(t, api.WithContent(dir, c))
	// Intercepta para ver o Range que o agente manda.
	orig := srv.Config.Handler
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ranges = append(ranges, r.Header.Get("Range")+"|"+r.Header.Get("If-Range"))
		mu.Unlock()
		orig.ServeHTTP(w, r)
	})
	dados := catalogo(500000, "MICROSOFT")
	adv := publica(t, dir, c, dados)

	// Um pedaço do mesmo arquivo já baixado.
	if err := os.MkdirAll(cab.dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	part := filepath.Join(cab.dir(), offlinePart)
	os.WriteFile(part, dados[:123456], 0o600)
	os.WriteFile(part+".sha256", []byte(adv.SHA256), 0o600)
	if err := cab.Sync(context.Background(), adv); err != nil {
		t.Fatal(err)
	}
	if len(ranges) != 1 || ranges[0] != `bytes=123456-|"`+adv.SHA256+`"` {
		t.Errorf("pedidos: %q", ranges)
	}

	// Um pedaço de outro arquivo: recomeça do zero, sem Range.
	ranges = nil
	adv2 := publica(t, dir, c, catalogo(400000, "MICROSOFT"))
	os.WriteFile(part, dados[:1000], 0o600)
	os.WriteFile(part+".sha256", []byte(adv.SHA256), 0o600)
	if err := cab.Sync(context.Background(), adv2); err != nil {
		t.Fatal(err)
	}
	if len(ranges) != 1 || ranges[0] != "|" {
		t.Errorf("pedidos: %q", ranges)
	}
}

func TestOfflineCabConexaoParada(t *testing.T) {
	dados := catalogo(200000, "MICROSOFT")
	s := sha256.Sum256(dados)
	adv := protocol.OfflineCatalog{SHA256: hex.EncodeToString(s[:]), Size: int64(len(dados))}
	var trava atomic.Bool
	trava.Store(true)
	parado := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !trava.Load() {
			w.Header().Set("ETag", `"`+adv.SHA256+`"`)
			http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(dados))
			return
		}
		w.Header().Set("Content-Length", "200009")
		w.WriteHeader(http.StatusOK)
		w.Write(dados[:50000])
		w.(http.Flusher).Flush()
		select { // a conexão fica aberta, sem mandar mais nada
		case <-r.Context().Done():
		case <-parado:
		}
	}))
	defer ts.Close()
	defer close(parado)
	ass := &assinatura{}
	cab := &OfflineCab{Client: NewClient(ts.URL, "cred", "v"), DataDir: t.TempDir(), Verify: ass.verifica,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), idle: 200 * time.Millisecond}

	err := cab.Sync(context.Background(), adv)
	if err == nil || !strings.Contains(err.Error(), "nenhum byte") {
		t.Fatalf("erro = %v", err)
	}
	if fi, err := os.Stat(filepath.Join(cab.dir(), offlinePart)); err != nil || fi.Size() != 50000 {
		t.Fatalf("o pedaço baixado precisa ficar para a retomada: %v %v", fi, err)
	}
	trava.Store(false)
	if err := cab.Sync(context.Background(), adv); err != nil || cab.Path() == "" {
		t.Fatalf("retomada: %v, Path %q", err, cab.Path())
	}
}

func TestOfflineCabRecusas(t *testing.T) {
	dir, c := t.TempDir(), &conteudo{}
	srv, cab, _, _ := cabDeTeste(t, api.WithContent(dir, c))
	ctx := context.Background()
	agora := time.Now()
	cab.now = func() time.Time { return agora }

	// Assinatura de outra empresa: recusado, o arquivo some e o mesmo anúncio não é baixado
	// de novo antes de 24 h.
	adv := publica(t, dir, c, catalogo(100000, "GOOGLE"))
	err := cab.Sync(ctx, adv)
	if err == nil || !strings.Contains(err.Error(), "não pela Microsoft") || !strings.Contains(err.Error(), "Google LLC <- Raiz Qualquer") {
		t.Fatalf("erro = %v", err)
	}
	if cab.Path() != "" {
		t.Error("um catálogo recusado nunca é usado")
	}
	if fs, _ := filepath.Glob(filepath.Join(cab.dir(), "*.cab*")); len(fs) != 0 {
		t.Errorf("sobrou no disco: %v", fs)
	}
	caminho := protocol.OfflineCatalogPath + adv.SHA256
	if err := cab.Sync(ctx, adv); err != nil || srv.conta(caminho) != 1 {
		t.Errorf("recusado de novo antes de 24 h: %v, %d pedidos", err, srv.conta(caminho))
	}
	agora = agora.Add(25 * time.Hour)
	_ = cab.Sync(ctx, adv)
	if srv.conta(caminho) != 2 {
		t.Errorf("depois de 24 h, nova tentativa: %d pedidos", srv.conta(caminho))
	}

	// O servidor entrega outro conteúdo com o hash anunciado: recusado sem conferir assinatura.
	adv2 := publica(t, dir, c, catalogo(100000, "MICROSOFT"))
	os.WriteFile(filepath.Join(dir, wsusscan.FileName(adv2.SHA256)), catalogo(100000, "MICROSOFX"), 0o640)
	if err := cab.Sync(ctx, adv2); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Errorf("hash diferente: %v", err)
	}

	// Anúncios fora do formato ou grandes demais: nenhum pedido.
	for _, a := range []protocol.OfflineCatalog{
		{SHA256: "abc", Size: 10},
		{SHA256: strings.Repeat("a", 64), Size: 0},
		{SHA256: strings.Repeat("a", 64), Size: 3 << 30},
	} {
		if err := cab.Sync(ctx, a); err == nil || !strings.Contains(err.Error(), "anúncio inválido") {
			t.Errorf("%+v: %v", a, err)
		}
	}

	// Arquivo que sumiu do servidor (substituído): erro recusado, nada gravado como em uso.
	sumiu := protocol.OfflineCatalog{SHA256: strings.Repeat("b", 64), Size: 1000}
	var rej *RejectedError
	if err := cab.Sync(ctx, sumiu); !errors.As(err, &rej) || rej.Status != http.StatusNotFound {
		t.Errorf("404: %v", err)
	}

	// Sem conferência de assinatura, nada é baixado.
	sem := &OfflineCab{Client: cab.Client, DataDir: t.TempDir(), Logger: cab.Logger}
	if err := sem.Sync(ctx, adv); err == nil {
		t.Error("sem Verify, o Sync precisa recusar")
	}
}

func TestCheckInEntregaOCatalogoAntesDaBusca(t *testing.T) {
	dir, c := t.TempDir(), &conteudo{}
	srv, _, cred := novoServidor(t, api.WithContent(dir, c))
	adv := publica(t, dir, c, catalogo(1000, "MICROSOFT"))
	var buscas atomic.Int32
	a := agenteDeTeste(t, srv, cred, &buscas, io.Discard)
	var ordem []string
	a.OfflineCatalog = func(_ context.Context, got protocol.OfflineCatalog) {
		if got.SHA256 != adv.SHA256 {
			t.Errorf("anúncio: %+v", got)
		}
		ordem = append(ordem, "catálogo")
	}
	scan := a.Scan
	a.Scan = func(ctx context.Context) protocol.PatchScanReport {
		ordem = append(ordem, "busca")
		return scan(ctx)
	}
	if out := a.CheckIn(context.Background()); out != outcomeOK {
		t.Fatalf("ciclo: %v", out)
	}
	if strings.Join(ordem, ",") != "catálogo,busca" {
		t.Errorf("ordem: %v", ordem)
	}
}
