package wsusscan

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// gabinete monta um .cab de mentira: cabeçalho "MSCF" com cbCabinet, o conteúdo e, depois
// do gabinete, uma "assinatura" anexada, como no arquivo real.
func gabinete(tamanho int, marca byte) []byte {
	assinatura := bytes.Repeat([]byte{0xAA}, 100)
	b := make([]byte, tamanho)
	copy(b, "MSCF")
	binary.LittleEndian.PutUint32(b[8:12], uint32(tamanho-len(assinatura)))
	for i := 12; i < tamanho-len(assinatura); i++ {
		b[i] = marca + byte(i%7)
	}
	copy(b[tamanho-len(assinatura):], assinatura)
	return b
}

// microsoftFalsa serve o arquivo com ETag, Range e If-Range (http.ServeContent), e pode
// cortar ou travar o corpo nos primeiros pedidos.
type microsoftFalsa struct {
	mu       sync.Mutex
	conteudo []byte
	etag     string
	cortes   int           // quantos GETs são cortados no meio
	trava    time.Duration // o primeiro GET para por esse tempo no meio
	status   int           // responde só isso, se diferente de zero
	gets     []string      // o Range de cada GET
}

func (m *microsoftFalsa) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	conteudo, etag, status := m.conteudo, m.etag, m.status
	cortar, travar := false, time.Duration(0)
	if r.Method == http.MethodGet {
		m.gets = append(m.gets, r.Header.Get("Range"))
		if m.cortes > 0 {
			m.cortes--
			cortar = true
		}
		travar, m.trava = m.trava, 0
	}
	m.mu.Unlock()
	if status != 0 {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("ETag", etag)
	w.Header().Set("Last-Modified", "Sat, 03 Oct 2026 01:08:04 GMT")
	if cortar || travar > 0 {
		inicio := 0
		if rg := r.Header.Get("Range"); rg != "" && r.Header.Get("If-Range") == etag {
			inicio = int(rangeStart(strings.Replace(rg, "=", " ", 1)))
			w.Header().Set("Content-Range", "bytes "+strconv.Itoa(inicio)+"-"+strconv.Itoa(len(conteudo)-1)+"/"+strconv.Itoa(len(conteudo)))
			w.Header().Set("Content-Length", strconv.Itoa(len(conteudo)-inicio))
			w.WriteHeader(http.StatusPartialContent)
		} else {
			w.Header().Set("Content-Length", strconv.Itoa(len(conteudo)))
			w.WriteHeader(http.StatusOK)
		}
		resto := conteudo[inicio:]
		w.Write(resto[:len(resto)/2])
		w.(http.Flusher).Flush()
		if travar > 0 {
			time.Sleep(travar)
			return
		}
		conn, _, _ := w.(http.Hijacker).Hijack()
		conn.Close()
		return
	}
	http.ServeContent(w, r, "wsusscn2.cab", time.Date(2026, 10, 3, 1, 8, 4, 0, time.UTC), bytes.NewReader(conteudo))
}

func (m *microsoftFalsa) pedidos() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.gets...)
}

func cliente(url string) *Client {
	c := NewClient("patchd-teste")
	c.URL, c.Wait, c.Tries = url, time.Millisecond, 3
	return c
}

func hash(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func TestSyncBaixaEPublica(t *testing.T) {
	ms := &microsoftFalsa{conteudo: gabinete(100000, 1), etag: `"0325ba4d352dd1:0"`}
	srv := httptest.NewServer(ms)
	defer srv.Close()
	dir := t.TempDir()
	c := cliente(srv.URL)

	f, mudou, err := c.Sync(context.Background(), dir, nil, nil)
	if err != nil || !mudou {
		t.Fatalf("primeira: %+v %t %v", f, mudou, err)
	}
	if f.SHA256 != hash(ms.conteudo) || f.Name != "wsusscn2-"+f.SHA256[:16]+".cab" || f.Size != 100000 || f.Cabinet != 99900 ||
		f.ETag != ms.etag || !f.LastModified.Equal(time.Date(2026, 10, 3, 1, 8, 4, 0, time.UTC)) {
		t.Errorf("arquivo: %+v", f)
	}
	if _, err := os.Stat(filepath.Join(dir, partName)); !os.IsNotExist(err) {
		t.Error("o .part deveria ter virado o arquivo publicado")
	}

	// Mesmo ETag: nada é baixado.
	f2, mudou, err := c.Sync(context.Background(), dir, &f, nil)
	if err != nil || mudou || f2 != f || len(ms.pedidos()) != 1 {
		t.Errorf("em dia: %t %v, GETs %q", mudou, err, ms.pedidos())
	}

	// O arquivo sumiu do disco: baixa de novo.
	os.Remove(filepath.Join(dir, f.Name))
	var linhas []string
	if _, mudou, err = c.Sync(context.Background(), dir, &f, func(s string) { linhas = append(linhas, s) }); err != nil || !mudou ||
		!strings.Contains(strings.Join(linhas, "\n"), "sumiu do disco") {
		t.Errorf("arquivo apagado: %t %v %q", mudou, err, linhas)
	}
}

func TestSyncRetoma(t *testing.T) {
	ms := &microsoftFalsa{conteudo: gabinete(200000, 2), etag: `"v1"`, cortes: 1}
	srv := httptest.NewServer(ms)
	defer srv.Close()
	dir := t.TempDir()
	var linhas []string
	f, _, err := cliente(srv.URL).Sync(context.Background(), dir, nil, func(s string) { linhas = append(linhas, s) })
	if err != nil || f.SHA256 != hash(ms.conteudo) {
		t.Fatalf("com corte: %v %q", err, linhas)
	}
	// O segundo pedido continua de onde o primeiro parou (metade: 100000 bytes).
	if g := ms.pedidos(); len(g) != 2 || g[0] != "" || g[1] != "bytes=100000-" {
		t.Errorf("pedidos: %q", g)
	}
	if !strings.Contains(strings.Join(linhas, "\n"), "tentativa 2, a partir de 0.1 MiB") {
		t.Errorf("andamento: %q", linhas)
	}

	// Entre execuções: um .part do mesmo ETag é retomado; de outro ETag, recomeça do zero.
	for _, caso := range []struct {
		etag, range0 string
	}{{`"v1"`, "bytes=5000-"}, {`"antigo"`, ""}} {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, partName), ms.conteudo[:5000], 0o640)
		os.WriteFile(filepath.Join(dir, partName+".etag"), []byte(caso.etag), 0o640)
		ms.mu.Lock()
		ms.gets = nil
		ms.mu.Unlock()
		f, _, err := cliente(srv.URL).Sync(context.Background(), dir, nil, nil)
		if err != nil || f.SHA256 != hash(ms.conteudo) || ms.pedidos()[0] != caso.range0 {
			t.Errorf(".part de %s: %v, pedidos %q", caso.etag, err, ms.pedidos())
		}
	}
}

func TestSyncConexaoParada(t *testing.T) {
	ms := &microsoftFalsa{conteudo: gabinete(50000, 3), etag: `"v1"`, trava: 700 * time.Millisecond}
	srv := httptest.NewServer(ms)
	defer srv.Close()
	c := cliente(srv.URL)
	c.Idle = 100 * time.Millisecond
	var linhas []string
	f, _, err := c.Sync(context.Background(), t.TempDir(), nil, func(s string) { linhas = append(linhas, s) })
	if err != nil || f.SHA256 != hash(ms.conteudo) || !strings.Contains(strings.Join(linhas, "\n"), "nenhum byte em 100ms") {
		t.Errorf("conexão parada: %v %q", err, linhas)
	}
}

func TestSyncRecusa(t *testing.T) {
	// Não é um gabinete: recusado, e o .part some.
	falso := bytes.Repeat([]byte("x"), 1000)
	ms := &microsoftFalsa{conteudo: falso, etag: `"x"`}
	srv := httptest.NewServer(ms)
	defer srv.Close()
	dir := t.TempDir()
	if _, _, err := cliente(srv.URL).Sync(context.Background(), dir, nil, nil); err == nil || !strings.Contains(err.Error(), `esperado "MSCF"`) {
		t.Errorf("não-gabinete: %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*")); len(left) != 0 {
		t.Errorf("sobrou: %v", left)
	}

	// cbCabinet maior que o arquivo.
	ruim := gabinete(1000, 1)
	binary.LittleEndian.PutUint32(ruim[8:12], 5000)
	ms.mu.Lock()
	ms.conteudo, ms.etag = ruim, `"y"`
	ms.mu.Unlock()
	if _, _, err := cliente(srv.URL).Sync(context.Background(), dir, nil, nil); err == nil || !strings.Contains(err.Error(), "o gabinete declara 5000 bytes") {
		t.Errorf("cbCabinet: %v", err)
	}

	// 404 no GET: erro definitivo, sem repetir (um GET só).
	ms.mu.Lock()
	ms.conteudo, ms.etag, ms.gets = gabinete(1000, 1), `"z"`, nil
	ms.mu.Unlock()
	srv404 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			ms.ServeHTTP(w, r)
			return
		}
		ms.mu.Lock()
		ms.gets = append(ms.gets, "404")
		ms.mu.Unlock()
		http.NotFound(w, r)
	}))
	defer srv404.Close()
	if _, _, err := cliente(srv404.URL).Sync(context.Background(), t.TempDir(), nil, nil); err == nil || !strings.Contains(err.Error(), "404") || len(ms.pedidos()) != 1 {
		t.Errorf("404: %v, pedidos %q", err, ms.pedidos())
	}
}

func TestSyncTrocaDeArquivoMantemOAnterior(t *testing.T) {
	ms := &microsoftFalsa{conteudo: gabinete(3000, 1), etag: `"1"`}
	srv := httptest.NewServer(ms)
	defer srv.Close()
	dir := t.TempDir()
	c := cliente(srv.URL)
	var publicados []File
	var prev *File
	for i := byte(1); i <= 3; i++ {
		ms.mu.Lock()
		ms.conteudo, ms.etag = gabinete(3000, i), `"`+string(rune('0'+i))+`"`
		ms.mu.Unlock()
		f, mudou, err := c.Sync(context.Background(), dir, prev, nil)
		if err != nil || !mudou {
			t.Fatalf("versão %d: %t %v", i, mudou, err)
		}
		publicados = append(publicados, f)
		prev = &publicados[len(publicados)-1]
	}
	// Ficam o atual e o anterior; o primeiro sai.
	files, _ := filepath.Glob(filepath.Join(dir, "wsusscn2-*.cab"))
	if len(files) != 2 {
		t.Errorf("arquivos: %v", files)
	}
	if _, err := os.Stat(filepath.Join(dir, publicados[0].Name)); !os.IsNotExist(err) {
		t.Error("o primeiro deveria ter saído")
	}
}

// Origem sem ETag (só Last-Modified, como o http.ServeFile): a retomada usa a data no If-Range.
func TestSyncSemETag(t *testing.T) {
	conteudo := gabinete(80000, 5)
	var mu sync.Mutex
	var ranges []string
	cortou := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if r.Method == http.MethodGet {
			ranges = append(ranges, r.Header.Get("Range")+" | "+r.Header.Get("If-Range"))
		}
		corta := r.Method == http.MethodGet && !cortou
		if corta {
			cortou = true
		}
		mu.Unlock()
		if corta {
			w.Header().Set("Content-Length", strconv.Itoa(len(conteudo)))
			w.Write(conteudo[:30000])
			w.(http.Flusher).Flush()
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		http.ServeContent(w, r, "wsusscn2.cab", time.Date(2026, 10, 3, 1, 8, 4, 0, time.UTC), bytes.NewReader(conteudo))
	}))
	defer srv.Close()
	f, _, err := cliente(srv.URL).Sync(context.Background(), t.TempDir(), nil, nil)
	mu.Lock()
	defer mu.Unlock()
	if err != nil || f.SHA256 != hash(conteudo) || len(ranges) != 2 || ranges[1] != "bytes=30000- | Sat, 03 Oct 2026 01:08:04 GMT" {
		t.Errorf("sem ETag: %v, pedidos %q", err, ranges)
	}
}
