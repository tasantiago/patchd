package repocache

import (
	"encoding/json"
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
)

const baseEspelho = "/pub/fedora/linux/updates/44/Everything/x86_64/"

// metalinkFalso responde o metalink com o estado do teste.
type metalinkFalso struct {
	*httptest.Server
	mu           sync.Mutex
	atual        string
	alternativos []string
	espelhos     []string // bases (com / no fim)
	pedidos      atomic.Int32
	fora         atomic.Bool
}

func novoMetalink(t *testing.T) *metalinkFalso {
	m := &metalinkFalso{}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.pedidos.Add(1)
		if m.fora.Load() {
			http.Error(w, "fora", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Query().Get("repo") != "updates-released-f44" || r.URL.Query().Get("arch") != "x86_64" {
			http.Error(w, "repo/arch inesperados: "+r.URL.RawQuery, http.StatusBadRequest)
			return
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		var b strings.Builder
		b.WriteString(`<?xml version="1.0" encoding="utf-8"?>
<metalink version="3.0" xmlns="http://www.metalinker.org/" xmlns:mm0="http://fedorahosted.org/mirrormanager">
 <files><file name="repomd.xml"><mm0:timestamp>1791336879</mm0:timestamp><size>7055</size>
  <verification><hash type="md5">x</hash><hash type="sha256">` + m.atual + `</hash></verification>
  <mm0:alternates>`)
		for _, a := range m.alternativos {
			b.WriteString(`<mm0:alternate><mm0:timestamp>1</mm0:timestamp><size>1</size><verification><hash type="sha256">` + a + `</hash></verification></mm0:alternate>`)
		}
		b.WriteString(`</mm0:alternates><resources maxconnections="1">`)
		// Um espelho rsync e um sem o caminho do repomd.xml: ignorados.
		b.WriteString(`<url protocol="rsync" type="rsync">rsync://espelho/fedora/repodata/repomd.xml</url>`)
		for _, e := range m.espelhos {
			b.WriteString(`<url protocol="http" type="http" preference="100">` + e + `repodata/repomd.xml</url>`)
		}
		b.WriteString(`</resources></file></files></metalink>`)
		io.WriteString(w, b.String())
	}))
	t.Cleanup(m.Close)
	return m
}

func (m *metalinkFalso) define(atual string, alternativos []string, espelhos ...*origem) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.atual, m.alternativos, m.espelhos = atual, alternativos, nil
	for _, e := range espelhos {
		m.espelhos = append(m.espelhos, e.URL+baseEspelho)
	}
}

// ambienteFedora: o cache, o metalink falso e a base do repositório updates/44/x86_64.
func ambienteFedora(t *testing.T) (*Cache, *metalinkFalso, string) {
	t.Helper()
	c, err := New(t.TempDir(), DefaultHosts, "patchd-teste", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	m := novoMetalink(t)
	c.MetalinkURL = m.URL + "/metalink"
	c.httpOK = true
	srv := httptest.NewServer(c)
	t.Cleanup(srv.Close)
	return c, m, srv.URL + "/repo/fedora/updates/44/x86_64/"
}

func get(t *testing.T, u string, hdr ...string) (int, string, string) {
	t.Helper()
	return pede(t, http.DefaultClient, u, hdr...)
}

func TestFedoraRepomdConferidoPeloMetalink(t *testing.T) {
	c, m, base := ambienteFedora(t)
	agora := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return agora }
	a, b := novaOrigem(t), novaOrigem(t)
	v1, v2 := []byte("<repomd>v1</repomd>"), []byte("<repomd>v2</repomd>")
	a.poe(baseEspelho+"repodata/repomd.xml", v1, time.Time{})
	b.poe(baseEspelho+"repodata/repomd.xml", v2, time.Time{})
	m.define(soma(v1), nil, a)

	if _, res, body := get(t, base+"repodata/repomd.xml"); res != "MISS" || body != string(v1) {
		t.Fatalf("1º: %s %q", res, body)
	}
	if _, res, _ := get(t, base+"repodata/repomd.xml"); res != "FRESH" || m.pedidos.Load() != 1 {
		t.Errorf("2º: %s, %d pedidos ao metalink", res, m.pedidos.Load())
	}
	agora = agora.Add(2 * time.Minute)
	if _, res, _ := get(t, base+"repodata/repomd.xml"); res != "REVALIDATED" || m.pedidos.Load() != 2 {
		t.Errorf("3º: %s, %d pedidos ao metalink", res, m.pedidos.Load())
	}

	// Publicação nova: o espelho A ainda tem a anterior (aceita como alternativa), o B tem
	// a atual. O cache fica com a atual.
	agora = agora.Add(2 * time.Minute)
	m.define(soma(v2), []string{soma(v1)}, a, b)
	if _, res, body := get(t, base+"repodata/repomd.xml"); res != "MISS" || body != string(v2) {
		t.Errorf("4º: %s %q", res, body)
	}

	// Um espelho que entrega um repomd.xml fora do metalink: recusado. Com o guardado ainda
	// aceito, serve o guardado; sem nada aceito, 502.
	agora = agora.Add(2 * time.Minute)
	falso := []byte("<repomd>alterado</repomd>")
	a.poe(baseEspelho+"repodata/repomd.xml", falso, time.Time{})
	m.define(soma([]byte("v3 que ninguém tem")), []string{soma(v2)}, a)
	if code, res, body := get(t, base+"repodata/repomd.xml"); code != 200 || res != "KEPT" || body != string(v2) {
		t.Errorf("5º (espelho alterado, guardado aceito): %d %s %q", code, res, body)
	}
	agora = agora.Add(2 * time.Minute)
	m.define(soma([]byte("v4")), nil, a)
	if code, _, body := get(t, base+"repodata/repomd.xml"); code != http.StatusBadGateway || strings.Contains(body, "alterado") {
		t.Errorf("6º (nada aceito): %d %q", code, body)
	}

	// Metalink fora: o guardado é servido; a segunda vez nem pergunta (falha recente).
	agora = agora.Add(2 * time.Minute)
	m.fora.Store(true)
	antes := m.pedidos.Load()
	if code, res, _ := get(t, base+"repodata/repomd.xml"); code != 200 || res != "STALE" {
		t.Errorf("7º (metalink fora): %d %s", code, res)
	}
	agora = agora.Add(30 * time.Second) // a memória do metalink já venceu; a falha, não
	if _, res, _ := get(t, base+"repodata/repomd.xml"); res != "STALE" || m.pedidos.Load() != antes+1 {
		t.Errorf("8º: %s, %d pedidos novos ao metalink (esperado 1)", res, m.pedidos.Load()-antes)
	}
}

func TestFedoraObjetos(t *testing.T) {
	c, m, base := ambienteFedora(t)
	a, b := novaOrigem(t), novaOrigem(t)
	m.define(soma([]byte("qualquer")), nil, a, b)

	// repodata pelo hash: o espelho A não tem (404), o B tem.
	primary := []byte(strings.Repeat("<package/>", 3000))
	nome := "repodata/" + soma(primary) + "-primary.xml.zst"
	b.poe(baseEspelho+nome, primary, time.Time{})
	if code, res, body := get(t, base+nome); code != 200 || res != "MISS" || body != string(primary) {
		t.Fatalf("repodata 1º: %d %s", code, res)
	}
	if _, res, _ := get(t, base+nome); res != "HIT" {
		t.Errorf("repodata 2º: %s", res)
	}
	if _, err := os.Stat(c.shaFile(soma(primary))); err != nil {
		t.Errorf("repodata fora do lugar: %v", err)
	}
	// Conteúdo que não bate com o hash do nome: entregue (o dnf recusa), nunca guardado.
	errado := "repodata/" + soma([]byte("outro")) + "-filelists.xml.zst"
	b.poe(baseEspelho+errado, []byte("não é o outro"), time.Time{})
	for i := 0; i < 2; i++ {
		if _, res, _ := get(t, base+errado); res != "MISS" {
			t.Errorf("hash errado, pedido %d: %s", i+1, res)
		}
	}

	// rpm pelo caminho.
	rpm := "Packages/o/openssl-libs-3.5.9-1.fc44.x86_64.rpm"
	conteudo := []byte(strings.Repeat("rpm!", 20000))
	a.poe(baseEspelho+rpm, conteudo, time.Time{})
	get(t, base+rpm)
	if _, res, _ := get(t, base+rpm); res != "HIT" || a.conta(baseEspelho+rpm) != 1 {
		t.Errorf("rpm: %s, %d pedidos ao espelho", res, a.conta(baseEspelho+rpm))
	}
	if _, err := os.Stat(filepath.Join(c.Dir, "pool", "fedora", "updates", "44", "x86_64", "Packages", "o", "openssl-libs-3.5.9-1.fc44.x86_64.rpm")); err != nil {
		t.Errorf("rpm fora do lugar: %v", err)
	}

	// Pedaço (Range) de um arquivo que não está no disco, como o zchunk do dnf: o pedaço vem
	// do espelho na hora, e o arquivo inteiro fica guardado em segundo plano.
	zck := []byte(strings.Repeat("zchunk", 10000))
	nomeZck := "repodata/" + soma(zck) + "-primary.xml.zck"
	a.poe(baseEspelho+nomeZck, zck, time.Time{})
	code, res, body := get(t, base+nomeZck, "Range", "bytes=0-99")
	if code != http.StatusPartialContent || res != "PASS" || body != string(zck[:100]) {
		t.Errorf("Range sem cache: %d %s %d bytes", code, res, len(body))
	}
	prazo := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(c.shaFile(soma(zck))); err == nil {
			break
		}
		if time.Now().After(prazo) {
			t.Fatal("o arquivo não foi guardado em segundo plano")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if code, res, body := get(t, base+nomeZck, "Range", "bytes=100-199"); code != http.StatusPartialContent || res != "HIT" || body != string(zck[100:200]) {
		t.Errorf("Range com cache: %d %s", code, res)
	}

	// Inexistente em todos os espelhos: 404.
	if code, _, _ := get(t, base+"Packages/n/nada-1-1.fc44.x86_64.rpm"); code != http.StatusNotFound {
		t.Errorf("inexistente: %d", code)
	}
}

func TestFedoraCaminhosRecusados(t *testing.T) {
	_, m, base := ambienteFedora(t)
	raiz := strings.TrimSuffix(base, "updates/44/x86_64/")
	casos := map[string]int{
		raiz + "rawhide/44/x86_64/repodata/repomd.xml":     http.StatusNotFound,
		raiz + "updates/latest/x86_64/repodata/repomd.xml": http.StatusNotFound,
		raiz + "updates/44/i386/repodata/repomd.xml":       http.StatusNotFound,
		raiz + "updates/44/x86_64/":                        http.StatusBadRequest,
		raiz + "updates/44/x86_64/repodata/repomd.xml?x=1": http.StatusBadRequest,
		raiz + "updates/44/x86_64/a/./b":                   http.StatusBadRequest,
	}
	for u, want := range casos {
		if code, _, _ := get(t, u); code != want {
			t.Errorf("%s: %d, esperado %d", u, code, want)
		}
	}
	resp, err := http.Post(base+"repodata/repomd.xml", "text/plain", nil)
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("POST: %d", resp.StatusCode)
		}
	}
	if m.pedidos.Load() != 0 {
		t.Errorf("nenhuma recusa pode chegar ao metalink: %d", m.pedidos.Load())
	}
}

func TestParseMetalinkSoHTTPS(t *testing.T) {
	x := `<metalink xmlns="http://www.metalinker.org/" xmlns:mm0="http://fedorahosted.org/mirrormanager"><files><file name="repomd.xml">
<verification><hash type="sha256">` + strings.Repeat("a", 64) + `</hash></verification><resources>
<url protocol="http">http://espelho1/fedora/repodata/repomd.xml</url>
<url protocol="https">https://espelho2/fedora/repodata/repomd.xml</url>
<url protocol="https">https://espelho3/fedora/outra-coisa.xml</url></resources></file></files></metalink>`
	info, err := parseMetalink([]byte(x), false)
	if err != nil || len(info.mirrors) != 1 || info.mirrors[0] != "https://espelho2/fedora/" {
		t.Errorf("%+v %v", info, err)
	}
	if _, err := parseMetalink([]byte(strings.Replace(x, "https://espelho2", "http://espelho2", 1)), false); err == nil {
		t.Error("metalink só com espelhos http aceito")
	}
}

// Com a origem fora, o InRelease guardado sai na hora a partir da segunda vez, sem esperar
// outra falha (Aula 6.6 parte 4b: o DNS fora levava 24 s por pedido).
func TestStaleImediatoComOrigemFora(t *testing.T) {
	c, o, cli := ambiente(t)
	agora := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return agora }
	p := "/ubuntu/dists/resolute/InRelease"
	o.poe(p, inRelease(agora.Add(-time.Hour), "x"), time.Time{})
	pede(t, cli, "http://archive.ubuntu.com"+p)

	o.fora.Store(true)
	agora = agora.Add(2 * time.Minute)
	if _, res, _ := pede(t, cli, "http://archive.ubuntu.com"+p); res != "STALE" || o.conta(p) != 2 {
		t.Fatalf("1ª falha: %s, %d pedidos", res, o.conta(p))
	}
	agora = agora.Add(35 * time.Second) // o FRESH já venceu; a falha tem menos de 1 min
	if _, res, _ := pede(t, cli, "http://archive.ubuntu.com"+p); res != "STALE" || o.conta(p) != 2 {
		t.Errorf("dentro de 1 min da falha: %s, %d pedidos (esperado nenhum novo)", res, o.conta(p))
	}
	agora = agora.Add(time.Minute)
	if _, _, _ = pede(t, cli, "http://archive.ubuntu.com"+p); o.conta(p) != 3 {
		t.Errorf("passado 1 min: %d pedidos (esperado uma nova tentativa)", o.conta(p))
	}
}

func TestStats(t *testing.T) {
	c, o, cli := ambiente(t)
	p := "/ubuntu/pool/main/s/sudo/sudo_1_amd64.deb"
	o.poe(p, []byte("deb"), time.Time{})
	pede(t, cli, "http://archive.ubuntu.com"+p)
	pede(t, cli, "http://archive.ubuntu.com"+p)
	rec := httptest.NewRecorder()
	c.ServeHTTP(rec, httptest.NewRequest("GET", "/repo/stats", nil))
	var st map[string]int64
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil || st["MISS"] != 1 || st["HIT"] != 1 {
		t.Errorf("stats: %s %v", rec.Body, err)
	}
	if len(st) != len(results) {
		t.Errorf("stats sem todos os resultados: %v", st)
	}
}
