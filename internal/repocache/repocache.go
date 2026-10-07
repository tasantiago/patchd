// Package repocache é o cache sob demanda dos repositórios Ubuntu (Aula 6.6, parte 4b;
// RF-29). O apt das máquinas usa o patchd-server como proxy HTTP só para os hosts das
// fontes (Acquire::http::Proxy::<host>): o pedido chega na forma absoluta
// ("GET http://archive.ubuntu.com/ubuntu/dists/...") e o servidor responde do disco ou
// busca na origem.
//
// Nada é assinado nem alterado aqui: quem confere a assinatura (InRelease, chave da
// Canonical) e os hashes (listas e pacotes) é o apt da máquina. O cache só precisa não
// servir nada velho e não guardar nada errado:
//
//   - InRelease, Release e Release.gpg mudam a cada publicação: são revalidados na origem
//     (If-Modified-Since), no máximo uma vez por minuto; um InRelease com Date mais velho
//     que o guardado é recusado (um servidor atrasado do archive.ubuntu.com não faz a
//     frota voltar no tempo).
//   - .../by-hash/SHA256/<hash> nunca muda: guardado pelo hash (o mesmo conteúdo pedido
//     por caminhos diferentes fica uma vez só no disco), e só se o SHA-256 conferir.
//   - pool/...deb nunca muda (a versão está no nome): guardado pelo caminho.
//   - O resto passa direto, sem guardar.
//
// https://wiki.debian.org/DebianRepository/Format
// https://manpages.ubuntu.com/manpages/resolute/man5/apt.conf.5.html
package repocache

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tasantiago/patchd/internal/httpx"
)

const (
	// DefaultHosts são as origens das fontes padrão do Ubuntu.
	DefaultHosts = "archive.ubuntu.com,security.ubuntu.com"

	indexFresh    = time.Minute      // InRelease revalidado no máximo uma vez nesse intervalo
	maxIndexSize  = 64 << 20         // InRelease tem ~140 KB; folga larga
	maxObjectSize = 4 << 30          // o maior .deb do Ubuntu (firmware) tem centenas de MB
	idleTimeout   = 2 * time.Minute  // sem bytes por esse tempo, na origem ou no cliente
	headerTimeout = time.Minute      // espera pelos cabeçalhos da origem
	cacheHeader   = "X-Patchd-Cache" // HIT, MISS, FRESH, REVALIDATED, KEPT, STALE, PASS
)

// Cache atende os pedidos de proxy do apt. Os campos são exportados para os testes.
type Cache struct {
	Dir    string
	Hosts  map[string]bool
	HTTP   *http.Client // origem; sem Timeout global (vale o idle)
	Logger *slog.Logger
	Idle   time.Duration

	now      func() time.Time
	base     func(host string) string // testes: aponta a origem para um servidor local
	mu       sync.Mutex
	inflight map[string]*call
}

// New monta o cache com as origens permitidas (lista separada por vírgulas).
func New(dir, hosts, userAgent string, logger *slog.Logger) (*Cache, error) {
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("PATCHD_REPO_CACHE_DIR precisa ser um caminho absoluto (recebido %q)", dir)
	}
	hs, err := ParseHosts(hosts)
	if err != nil {
		return nil, err
	}
	t := http.DefaultTransport.(*http.Transport).Clone() // mantém o proxy do ambiente
	t.ResponseHeaderTimeout = headerTimeout
	c := &Cache{Dir: dir, Hosts: hs, Logger: logger, Idle: idleTimeout,
		HTTP: &http.Client{Transport: &userAgentTransport{next: t, ua: userAgent},
			// A origem não redireciona o apt para outro lugar através do cache.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	for _, d := range []string{"sha256", "pool", "index", "tmp"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o750); err != nil {
			return nil, err
		}
	}
	return c, nil
}

var hostPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// ParseHosts lê a lista de origens: nomes de host, sem esquema, porta ou caminho.
func ParseHosts(list string) (map[string]bool, error) {
	hs := map[string]bool{}
	for _, h := range strings.Split(list, ",") {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		if !hostPattern.MatchString(h) {
			return nil, fmt.Errorf("PATCHD_REPO_CACHE_HOSTS: %q não é um nome de host (sem http://, porta ou caminho)", h)
		}
		hs[h] = true
	}
	if len(hs) == 0 {
		return nil, errors.New("PATCHD_REPO_CACHE_HOSTS: nenhuma origem")
	}
	return hs, nil
}

type userAgentTransport struct {
	next http.RoundTripper
	ua   string
}

func (t *userAgentTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("User-Agent", t.ua)
	return t.next.RoundTrip(r)
}

// IsProxyRequest diz se o pedido é de proxy (forma absoluta ou CONNECT), e não da API.
func IsProxyRequest(r *http.Request) bool {
	return r.Method == http.MethodConnect || r.URL.IsAbs()
}

// kind é o tratamento de cada caminho.
type kind int

const (
	kindPass   kind = iota // repassado sem guardar
	kindIndex              // InRelease, Release, Release.gpg: revalidado
	kindByHash             // by-hash/SHA256/<hash>: guardado pelo hash
	kindPool               // pool/...deb: guardado pelo caminho
)

var (
	indexPattern  = regexp.MustCompile(`/dists/[^/]+/(InRelease|Release|Release\.gpg)$`)
	byHashPattern = regexp.MustCompile(`/by-hash/SHA256/([0-9a-f]{64})$`)
	poolPattern   = regexp.MustCompile(`/pool/.+\.(deb|udeb|ddeb)$`)
)

func classify(p string) (kind, string) {
	switch {
	case indexPattern.MatchString(p):
		return kindIndex, ""
	case byHashPattern.MatchString(p):
		return kindByHash, byHashPattern.FindStringSubmatch(p)[1]
	case poolPattern.MatchString(p):
		return kindPool, ""
	}
	return kindPass, ""
}

// ServeHTTP atende um pedido de proxy do apt.
func (c *Cache) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := c.clock()
	w = httpx.ExtendWrites(w, c.idle())
	host := strings.ToLower(r.URL.Hostname())
	p := r.URL.Path
	switch {
	case r.Method == http.MethodConnect:
		// HTTPS pelo cache seria um túnel opaco: nada a guardar, e um proxy aberto.
		http.Error(w, "o cache do patchd não abre túneis (CONNECT)", http.StatusMethodNotAllowed)
		return
	case r.Method != http.MethodGet && r.Method != http.MethodHead:
		http.Error(w, "método não permitido", http.StatusMethodNotAllowed)
		return
	case r.URL.Scheme != "http" || (r.URL.Port() != "" && r.URL.Port() != "80"):
		http.Error(w, "só http:// na porta 80", http.StatusBadRequest)
		return
	case !c.Hosts[host]:
		c.Logger.Warn("cache de repositórios: origem fora da lista", "host", host, "remote", r.RemoteAddr)
		http.Error(w, "origem fora da lista do cache (PATCHD_REPO_CACHE_HOSTS)", http.StatusForbidden)
		return
	case r.URL.RawQuery != "" || p == "" || path.Clean(p) != p || strings.Contains(p, "\\"):
		http.Error(w, "caminho inválido", http.StatusBadRequest)
		return
	}

	k, sum := classify(p)
	if r.Method == http.MethodHead {
		k = kindPass
	}
	var result string
	var n int64
	var err error
	switch k {
	case kindIndex:
		result, n, err = c.serveIndex(w, r, host, p)
	case kindByHash:
		result, n, err = c.serveObject(w, r, host, p, filepath.Join(c.Dir, "sha256", sum[:2], sum), sum)
	case kindPool:
		result, n, err = c.serveObject(w, r, host, p, filepath.Join(c.Dir, "pool", host, filepath.FromSlash(p)), "")
	default:
		result, n, err = c.pass(w, r, host, p)
	}
	attrs := []any{"result", result, "host", host, "path", p, "bytes", n,
		"duration_ms", c.clock().Sub(start).Milliseconds(), "remote", r.RemoteAddr}
	switch {
	case err != nil:
		c.Logger.Warn("cache de repositórios", append(attrs, "error", err)...)
	case result == "MISS" || result == "KEPT" || result == "STALE":
		c.Logger.Info("cache de repositórios", attrs...)
	default:
		c.Logger.Debug("cache de repositórios", attrs...)
	}
}

// clock é o relógio (substituível nos testes; nunca escrito durante o atendimento).
func (c *Cache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *Cache) idle() time.Duration {
	if c.Idle <= 0 {
		return idleTimeout
	}
	return c.Idle
}

// ---- índices (InRelease) ----

// indexMeta acompanha o índice guardado.
type indexMeta struct {
	LastModified string    `json:"last_modified,omitempty"` // da origem, para o If-Modified-Since
	ETag         string    `json:"etag,omitempty"`
	Date         time.Time `json:"date,omitzero"` // campo Date: do próprio arquivo
	CheckedAt    time.Time `json:"checked_at"`    // última conversa com a origem
}

func (c *Cache) serveIndex(w http.ResponseWriter, r *http.Request, host, p string) (string, int64, error) {
	file := filepath.Join(c.Dir, "index", host, filepath.FromSlash(p))
	metaFile := file + ".meta.json"
	unlock := c.lock("index:" + host + p)
	defer unlock()

	var meta indexMeta
	stored := false
	if b, err := os.ReadFile(metaFile); err == nil && json.Unmarshal(b, &meta) == nil {
		if _, err := os.Stat(file); err == nil {
			stored = true
		}
	}
	if stored && c.clock().Sub(meta.CheckedAt) < indexFresh {
		return c.serveFile(w, r, file, "FRESH")
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, c.origin(host, p), nil)
	if err != nil {
		return "", 0, err
	}
	if stored {
		if meta.LastModified != "" {
			req.Header.Set("If-Modified-Since", meta.LastModified)
		}
		if meta.ETag != "" {
			req.Header.Set("If-None-Match", meta.ETag)
		}
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return c.stale(w, r, file, stored, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotModified && stored:
		meta.CheckedAt = c.clock().UTC()
		_ = writeJSON(metaFile, meta)
		return c.serveFile(w, r, file, "REVALIDATED")
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests:
		return c.stale(w, r, file, stored, fmt.Errorf("origem respondeu %s", resp.Status))
	default:
		// 404 (um Release.gpg que não existe, uma suite errada): repassado como veio.
		w.Header().Set(cacheHeader, "PASS")
		http.Error(w, "origem respondeu "+resp.Status, resp.StatusCode)
		return "PASS", 0, nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxIndexSize+1))
	if err != nil {
		return c.stale(w, r, file, stored, err)
	}
	if len(body) > maxIndexSize {
		return c.stale(w, r, file, stored, errors.New("índice maior que o limite"))
	}
	date := releaseDate(body)
	if stored && !meta.Date.IsZero() && !date.IsZero() && date.Before(meta.Date) {
		// Um servidor atrasado da origem: o guardado é mais novo e continua valendo.
		meta.CheckedAt = c.clock().UTC()
		_ = writeJSON(metaFile, meta)
		c.Logger.Warn("cache de repositórios: a origem entregou um índice mais velho que o guardado; mantido o guardado",
			"host", host, "path", p, "origin_date", date, "stored_date", meta.Date)
		return c.serveFile(w, r, file, "KEPT")
	}
	if err := writeAtomic(file, body); err != nil {
		return "", 0, err
	}
	meta = indexMeta{LastModified: resp.Header.Get("Last-Modified"), ETag: resp.Header.Get("ETag"),
		Date: date, CheckedAt: c.clock().UTC()}
	if t, err := http.ParseTime(meta.LastModified); err == nil {
		_ = os.Chtimes(file, t, t) // o ServeContent responde 304 ao apt por essa data
	}
	if err := writeJSON(metaFile, meta); err != nil {
		return "", 0, err
	}
	return c.serveFile(w, r, file, "MISS")
}

// stale serve o índice guardado quando a origem falha; sem nada guardado, 502.
func (c *Cache) stale(w http.ResponseWriter, r *http.Request, file string, stored bool, cause error) (string, int64, error) {
	if !stored {
		w.Header().Set(cacheHeader, "MISS")
		http.Error(w, "origem indisponível", http.StatusBadGateway)
		return "MISS", 0, cause
	}
	res, n, err := c.serveFile(w, r, file, "STALE")
	if err == nil {
		err = fmt.Errorf("origem indisponível, servido o guardado: %w", cause)
	}
	return res, n, err
}

// releaseDate lê o campo "Date:" de um Release/InRelease (o InRelease é texto assinado
// em claro: o campo fica no corpo). Zero quando não há (Release.gpg).
func releaseDate(b []byte) time.Time {
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if v, ok := strings.CutPrefix(line, "Date: "); ok {
			// "Mon, 05 Oct 2026 13:50:19 UTC", às vezes com o dia com espaço ("  1:08:00").
			v = strings.Join(strings.Fields(v), " ")
			for _, layout := range []string{"Mon, 02 Jan 2006 15:04:05 MST", "Mon, 2 Jan 2006 15:04:05 MST", "Mon, 02 Jan 2006 15:04:05 -0700"} {
				if t, err := time.Parse(layout, v); err == nil {
					return t.UTC()
				}
			}
			return time.Time{}
		}
		if line == "MD5Sum:" || line == "SHA256:" {
			break // os campos do cabeçalho acabaram
		}
	}
	return time.Time{}
}

// ---- objetos imutáveis (by-hash e pool) ----

// call é um download em andamento: quem pede o mesmo arquivo espera por ele.
type call struct {
	done chan struct{}
	err  error
}

func (c *Cache) serveObject(w http.ResponseWriter, r *http.Request, host, p, file, sum string) (string, int64, error) {
	if _, err := os.Stat(file); err == nil {
		return c.serveFile(w, r, file, "HIT")
	}
	key := file
	c.mu.Lock()
	if c.inflight == nil {
		c.inflight = map[string]*call{}
	}
	if cl, ok := c.inflight[key]; ok {
		// Outro pedido já está baixando: espera e serve do disco.
		c.mu.Unlock()
		select {
		case <-cl.done:
		case <-r.Context().Done():
			return "MISS", 0, r.Context().Err()
		}
		if _, err := os.Stat(file); cl.err != nil || err != nil {
			// O download que estava em andamento falhou ou não guardou nada (404 na origem).
			w.Header().Set(cacheHeader, "MISS")
			http.Error(w, "origem não entregou o arquivo", http.StatusBadGateway)
			return "MISS", 0, cl.err
		}
		return c.serveFile(w, r, file, "HIT")
	}
	cl := &call{done: make(chan struct{})}
	c.inflight[key] = cl
	c.mu.Unlock()

	n, err := c.fetchObject(w, r, host, p, file, sum)
	cl.err = err
	c.mu.Lock()
	delete(c.inflight, key)
	c.mu.Unlock()
	close(cl.done)
	return "MISS", n, err
}

// fetchObject baixa da origem, entrega ao apt enquanto grava e só guarda se o arquivo
// chegou inteiro e (by-hash) com o SHA-256 do nome. O download continua até o fim mesmo
// que o apt desista: o próximo pedido encontra o arquivo pronto.
func (c *Cache) fetchObject(w http.ResponseWriter, r *http.Request, host, p, file, sum string) (int64, error) {
	ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.origin(host, p), nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		w.Header().Set(cacheHeader, "MISS")
		http.Error(w, "origem indisponível", http.StatusBadGateway)
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		w.Header().Set(cacheHeader, "MISS")
		http.Error(w, "origem respondeu "+resp.Status, passStatus(resp.StatusCode))
		if resp.StatusCode == http.StatusNotFound {
			return 0, nil // não é falha do cache: o arquivo não existe na origem
		}
		return 0, fmt.Errorf("origem respondeu %s", resp.Status)
	}
	if resp.ContentLength > maxObjectSize {
		w.Header().Set(cacheHeader, "MISS")
		http.Error(w, "arquivo maior que o limite do cache", http.StatusBadGateway)
		return 0, fmt.Errorf("Content-Length %d acima do limite", resp.ContentLength)
	}

	tmp, err := os.CreateTemp(filepath.Join(c.Dir, "tmp"), "obj-*")
	if err != nil {
		return 0, err
	}
	defer os.Remove(tmp.Name()) // depois do Rename, não há mais o que apagar

	for _, h := range []string{"Content-Type", "Content-Length", "Last-Modified"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.Header().Set(cacheHeader, "MISS")
	w.WriteHeader(http.StatusOK)

	h := sha256.New()
	client := &bestEffort{w: w}
	body := newStallReader(resp.Body, c.idle(), cancel)
	n, cerr := io.Copy(io.MultiWriter(tmp, h, client), io.LimitReader(body, maxObjectSize+1))
	body.stop()
	if err := tmp.Close(); cerr == nil {
		cerr = err
	}
	switch {
	case body.stalled():
		return n, fmt.Errorf("origem sem enviar nada por %s", c.idle())
	case cerr != nil:
		return n, cerr
	case n > maxObjectSize:
		return n, errors.New("arquivo maior que o limite do cache")
	case resp.ContentLength >= 0 && n != resp.ContentLength:
		return n, fmt.Errorf("a origem mandou %d de %d bytes", n, resp.ContentLength)
	case sum != "" && hex.EncodeToString(h.Sum(nil)) != sum:
		// O apt também vai recusar (ele confere o hash); aqui só não guardamos.
		return n, fmt.Errorf("o conteúdo não tem o SHA-256 do nome (%s...)", sum[:16])
	}
	if t, err := http.ParseTime(resp.Header.Get("Last-Modified")); err == nil {
		_ = os.Chtimes(tmp.Name(), t, t)
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
		return n, err
	}
	return n, os.Rename(tmp.Name(), file)
}

// bestEffort escreve no apt até a primeira falha e depois descarta (o download segue).
type bestEffort struct {
	w      io.Writer
	failed bool
}

func (b *bestEffort) Write(p []byte) (int, error) {
	if !b.failed {
		if _, err := b.w.Write(p); err != nil {
			b.failed = true
		}
	}
	return len(p), nil
}

// ---- repasse ----

// pass busca na origem e repassa sem guardar (listas sem by-hash, Contents, ícones...).
func (c *Cache) pass(w http.ResponseWriter, r *http.Request, host, p string) (string, int64, error) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, r.Method, c.origin(host, p), nil)
	if err != nil {
		return "PASS", 0, err
	}
	for _, h := range []string{"If-Modified-Since", "If-None-Match", "Range", "If-Range"} {
		if v := r.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		w.Header().Set(cacheHeader, "PASS")
		http.Error(w, "origem indisponível", http.StatusBadGateway)
		return "PASS", 0, err
	}
	defer resp.Body.Close()
	for _, h := range []string{"Content-Type", "Content-Length", "Content-Range", "Last-Modified", "ETag", "Accept-Ranges"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.Header().Set(cacheHeader, "PASS")
	w.WriteHeader(passStatus(resp.StatusCode))
	body := newStallReader(resp.Body, c.idle(), cancel)
	n, err := io.Copy(w, body)
	body.stop()
	return "PASS", n, err
}

// passStatus: um redirecionamento da origem não chega ao apt (ele seguiria para fora do cache).
func passStatus(code int) int {
	if code >= 300 && code < 400 && code != http.StatusNotModified {
		return http.StatusBadGateway
	}
	return code
}

// ---- utilitários ----

func (c *Cache) origin(host, p string) string {
	if c.base != nil {
		return c.base(host) + p
	}
	return "http://" + host + p
}

// serveFile entrega um arquivo guardado (com Range e If-Modified-Since do apt).
func (c *Cache) serveFile(w http.ResponseWriter, r *http.Request, file, result string) (string, int64, error) {
	f, err := os.Open(file)
	if err != nil {
		http.Error(w, "erro interno do cache", http.StatusInternalServerError)
		return result, 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		http.Error(w, "erro interno do cache", http.StatusInternalServerError)
		return result, 0, err
	}
	w.Header().Set(cacheHeader, result)
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, "", fi.ModTime(), f)
	return result, fi.Size(), nil
}

// lock serializa o tratamento de um índice (dois apt pedindo o mesmo InRelease juntos
// fazem uma conversa só com a origem).
func (c *Cache) lock(key string) func() {
	c.mu.Lock()
	if c.inflight == nil {
		c.inflight = map[string]*call{}
	}
	for {
		cl, ok := c.inflight[key]
		if !ok {
			break
		}
		c.mu.Unlock()
		<-cl.done
		c.mu.Lock()
	}
	cl := &call{done: make(chan struct{})}
	c.inflight[key] = cl
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		delete(c.inflight, key)
		c.mu.Unlock()
		close(cl.done)
	}
}

func writeAtomic(file string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), file)
}

func writeJSON(file string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writeAtomic(file, b)
}

// stallReader cancela o pedido à origem quando passa idle sem nenhum byte.
type stallReader struct {
	r     io.Reader
	idle  time.Duration
	timer *time.Timer
	fired atomic.Bool
}

func newStallReader(r io.Reader, idle time.Duration, cancel context.CancelFunc) *stallReader {
	s := &stallReader{r: r, idle: idle}
	s.timer = time.AfterFunc(idle, func() { s.fired.Store(true); cancel() })
	return s
}

func (s *stallReader) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if n > 0 {
		s.timer.Reset(s.idle)
	}
	return n, err
}

func (s *stallReader) stop()         { s.timer.Stop() }
func (s *stallReader) stalled() bool { return s.fired.Load() }
