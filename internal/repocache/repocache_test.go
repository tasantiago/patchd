package repocache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// origem simula o archive.ubuntu.com: arquivos por caminho, contagem de pedidos e falhas
// sob comando.
type origem struct {
	*httptest.Server
	mu       sync.Mutex
	arquivos map[string][]byte
	datas    map[string]time.Time
	pedidos  map[string]int
	ims      []string // If-Modified-Since recebidos
	fora     atomic.Bool
	atraso   time.Duration
}

func novaOrigem(t *testing.T) *origem {
	o := &origem{arquivos: map[string][]byte{}, datas: map[string]time.Time{}, pedidos: map[string]int{}}
	o.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o.mu.Lock()
		o.pedidos[r.URL.Path]++
		if v := r.Header.Get("If-Modified-Since"); v != "" {
			o.ims = append(o.ims, v)
		}
		b, ok := o.arquivos[r.URL.Path]
		d := o.datas[r.URL.Path]
		o.mu.Unlock()
		if o.fora.Load() {
			http.Error(w, "fora", http.StatusServiceUnavailable)
			return
		}
		time.Sleep(o.atraso)
		if !ok {
			http.NotFound(w, r)
			return
		}
		if d.IsZero() {
			d = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
		}
		http.ServeContent(w, r, "", d, strings.NewReader(string(b)))
	}))
	t.Cleanup(o.Close)
	return o
}

func (o *origem) poe(p string, b []byte, d time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.arquivos[p], o.datas[p] = b, d
}

func (o *origem) conta(p string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.pedidos[p]
}

// ambiente: o cache atrás de um servidor e um cliente que o usa como proxy, como o apt.
func ambiente(t *testing.T) (*Cache, *origem, *http.Client) {
	t.Helper()
	o := novaOrigem(t)
	c, err := New(t.TempDir(), DefaultHosts, "patchd-teste", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	c.base = func(string) string { return o.URL }
	srv := httptest.NewServer(c)
	t.Cleanup(srv.Close)
	proxy, _ := url.Parse(srv.URL)
	cli := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxy)}}
	t.Cleanup(cli.CloseIdleConnections)
	return c, o, cli
}

func pede(t *testing.T, cli *http.Client, u string, hdr ...string) (int, string, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", u, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := cli.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get(cacheHeader), string(b)
}

func soma(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func TestByHashGuardadoPeloConteudo(t *testing.T) {
	c, o, cli := ambiente(t)
	dados := []byte(strings.Repeat("Package: x\n", 1000))
	h := soma(dados)
	p1 := "/ubuntu/dists/resolute/main/binary-amd64/by-hash/SHA256/" + h
	p2 := "/ubuntu/dists/resolute/restricted/dep11/by-hash/SHA256/" + h
	o.poe(p1, dados, time.Time{})

	if code, res, body := pede(t, cli, "http://archive.ubuntu.com"+p1); code != 200 || res != "MISS" || body != string(dados) {
		t.Fatalf("1º pedido: %d %s %d bytes", code, res, len(body))
	}
	if code, res, body := pede(t, cli, "http://archive.ubuntu.com"+p1); code != 200 || res != "HIT" || body != string(dados) {
		t.Errorf("2º pedido: %d %s", code, res)
	}
	// Mesmo hash por outro caminho (e outro host): já está no disco.
	if code, res, _ := pede(t, cli, "http://security.ubuntu.com"+p2); code != 200 || res != "HIT" {
		t.Errorf("outro caminho, mesmo hash: %d %s", code, res)
	}
	if o.conta(p1) != 1 || o.conta(p2) != 0 {
		t.Errorf("pedidos à origem: %d e %d", o.conta(p1), o.conta(p2))
	}
	if _, err := os.Stat(filepath.Join(c.Dir, "sha256", h[:2], h)); err != nil {
		t.Errorf("objeto fora do lugar: %v", err)
	}
}

func TestByHashComConteudoErradoNaoFicaGuardado(t *testing.T) {
	_, o, cli := ambiente(t)
	h := soma([]byte("o certo"))
	p := "/ubuntu/dists/resolute/main/binary-amd64/by-hash/SHA256/" + h
	o.poe(p, []byte("outro conteúdo"), time.Time{})
	for i := 1; i <= 2; i++ {
		if _, res, _ := pede(t, cli, "http://archive.ubuntu.com"+p); res != "MISS" {
			t.Errorf("pedido %d: %s (um conteúdo errado nunca vira HIT)", i, res)
		}
	}
	if o.conta(p) != 2 {
		t.Errorf("pedidos à origem: %d", o.conta(p))
	}
}

func TestPoolComRange(t *testing.T) {
	_, o, cli := ambiente(t)
	p := "/ubuntu/pool/main/s/sudo/sudo_1.9.17p2-1ubuntu1_amd64.deb"
	deb := []byte(strings.Repeat("!<arch>\n", 5000))
	o.poe(p, deb, time.Time{})
	pede(t, cli, "http://archive.ubuntu.com"+p)
	code, res, body := pede(t, cli, "http://archive.ubuntu.com"+p, "Range", "bytes=100-")
	if code != http.StatusPartialContent || res != "HIT" || body != string(deb[100:]) {
		t.Errorf("retomada pelo cache: %d %s %d bytes", code, res, len(body))
	}
	if o.conta(p) != 1 {
		t.Errorf("pedidos à origem: %d", o.conta(p))
	}
	// Um .deb que não existe: 404 repassado, nada guardado.
	if code, _, _ := pede(t, cli, "http://archive.ubuntu.com/ubuntu/pool/main/n/nada/nada_1_amd64.deb"); code != 404 {
		t.Errorf("inexistente: %d", code)
	}
}

func inRelease(data time.Time, corpo string) []byte {
	return []byte("-----BEGIN PGP SIGNED MESSAGE-----\nHash: SHA512\n\nOrigin: Ubuntu\nSuite: resolute-security\nDate: " +
		data.Format("Mon, 02 Jan 2006 15:04:05 MST") + "\nAcquire-By-Hash: yes\nSHA256:\n " + corpo + "\n-----BEGIN PGP SIGNATURE-----\n")
}

func TestInReleaseRevalidadoENuncaVoltaNoTempo(t *testing.T) {
	c, o, cli := ambiente(t)
	agora := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return agora }
	p := "/ubuntu/dists/resolute-security/InRelease"
	u := "http://security.ubuntu.com" + p
	d1 := time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)
	v1 := inRelease(d1, "v1")
	o.poe(p, v1, d1)

	if _, res, body := pede(t, cli, u); res != "MISS" || body != string(v1) {
		t.Fatalf("1º: %s", res)
	}
	// Dentro de um minuto: do disco, sem perguntar à origem.
	if _, res, _ := pede(t, cli, u); res != "FRESH" || o.conta(p) != 1 {
		t.Errorf("2º: %s, %d pedidos", res, o.conta(p))
	}
	// O apt com If-Modified-Since igual à data do arquivo: 304 do cache.
	if code, _, _ := pede(t, cli, u, "If-Modified-Since", d1.Format(http.TimeFormat)); code != http.StatusNotModified {
		t.Errorf("condicional do apt: %d", code)
	}
	// Passado o minuto: pergunta à origem com If-Modified-Since, que responde 304.
	agora = agora.Add(2 * time.Minute)
	if _, res, _ := pede(t, cli, u); res != "REVALIDATED" || len(o.ims) != 1 {
		t.Errorf("3º: %s, condicionais %v", res, o.ims)
	}
	// Publicação nova na origem: troca.
	agora = agora.Add(2 * time.Minute)
	d2 := d1.Add(3 * time.Hour)
	v2 := inRelease(d2, "v2")
	o.poe(p, v2, d2)
	if _, res, body := pede(t, cli, u); res != "MISS" || body != string(v2) {
		t.Errorf("4º: %s %q", res, body)
	}
	// Um servidor atrasado da origem entrega o v1 (Date mais velho): fica o v2.
	agora = agora.Add(2 * time.Minute)
	o.poe(p, v1, d2.Add(time.Hour)) // Last-Modified mais novo, conteúdo velho
	if _, res, body := pede(t, cli, u); res != "KEPT" || body != string(v2) {
		t.Errorf("5º (origem atrasada): %s %q", res, body)
	}
	// Origem fora: serve o guardado.
	agora = agora.Add(2 * time.Minute)
	o.fora.Store(true)
	if code, res, body := pede(t, cli, u); code != 200 || res != "STALE" || body != string(v2) {
		t.Errorf("6º (origem fora): %d %s", code, res)
	}
	// Origem fora e nada guardado: 502.
	if code, _, _ := pede(t, cli, "http://archive.ubuntu.com/ubuntu/dists/resolute/InRelease"); code != http.StatusBadGateway {
		t.Errorf("sem nada guardado: %d", code)
	}
}

func TestRepasseSemGuardar(t *testing.T) {
	_, o, cli := ambiente(t)
	p := "/ubuntu/dists/resolute/main/i18n/Translation-en.xz"
	o.poe(p, []byte("xz"), time.Time{})
	for range 2 {
		if code, res, _ := pede(t, cli, "http://archive.ubuntu.com"+p); code != 200 || res != "PASS" {
			t.Errorf("%d %s", code, res)
		}
	}
	if o.conta(p) != 2 {
		t.Errorf("pedidos à origem: %d", o.conta(p))
	}
}

func TestRecusas(t *testing.T) {
	_, o, cli := ambiente(t)
	casos := []struct {
		url    string
		status int
	}{
		{"http://exemplo.com/ubuntu/dists/resolute/InRelease", http.StatusForbidden},              // origem fora da lista
		{"http://archive.ubuntu.com:8080/ubuntu/dists/resolute/InRelease", http.StatusBadRequest}, // outra porta
		{"http://archive.ubuntu.com/ubuntu/dists/resolute/InRelease?x=1", http.StatusBadRequest},  // query
		{"http://archive.ubuntu.com/ubuntu/../etc/passwd", http.StatusBadRequest},                 // caminho fora do normal
	}
	for _, c := range casos {
		if code, _, _ := pede(t, cli, c.url); code != c.status {
			t.Errorf("%s: %d, esperado %d", c.url, code, c.status)
		}
	}
	if len(o.pedidos) != 0 {
		t.Errorf("nenhuma recusa pode chegar à origem: %v", o.pedidos)
	}
	// POST e CONNECT direto no handler.
	c, _, _ := ambiente(t)
	for _, m := range []string{"POST", "CONNECT"} {
		req := httptest.NewRequest(m, "http://archive.ubuntu.com/ubuntu/x", nil)
		rec := httptest.NewRecorder()
		c.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: %d", m, rec.Code)
		}
	}
}

// Vários apt pedem o mesmo pacote ao mesmo tempo: a origem recebe um pedido só.
func TestDownloadsSimultaneosViramUm(t *testing.T) {
	_, o, cli := ambiente(t)
	o.atraso = 150 * time.Millisecond
	p := "/ubuntu/pool/main/l/linux-firmware/linux-firmware_20260901_all.deb"
	deb := []byte(strings.Repeat("firmware", 50000))
	o.poe(p, deb, time.Time{})
	var wg sync.WaitGroup
	resultados := make([]string, 8)
	for i := range resultados {
		wg.Go(func() {
			code, res, body := pede(t, cli, "http://archive.ubuntu.com"+p)
			resultados[i] = fmt.Sprintf("%d/%s/%v", code, res, body == string(deb))
		})
	}
	wg.Wait()
	if o.conta(p) != 1 {
		t.Errorf("pedidos à origem: %d (%v)", o.conta(p), resultados)
	}
	for _, r := range resultados {
		if r != "200/MISS/true" && r != "200/HIT/true" {
			t.Errorf("resultado: %v", resultados)
			break
		}
	}
}

func TestParseHosts(t *testing.T) {
	if hs, err := ParseHosts(" archive.ubuntu.com, Security.Ubuntu.com ,"); err != nil || !hs["security.ubuntu.com"] || len(hs) != 2 {
		t.Errorf("%v %v", hs, err)
	}
	for _, ruim := range []string{"", "http://archive.ubuntu.com", "archive.ubuntu.com:80", "archive.ubuntu.com/ubuntu", "localhost"} {
		if _, err := ParseHosts(ruim); err == nil {
			t.Errorf("%q aceito", ruim)
		}
	}
	if _, err := New("relativo", DefaultHosts, "x", nil); err == nil {
		t.Error("pasta relativa aceita")
	}
}
