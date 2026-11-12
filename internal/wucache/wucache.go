// Package wucache é o cache das atualizações do Windows no servidor (docs/cache-windows.md, o
// RF-33): o substituto do papel de distribuição do WSUS. O agente encontra a atualização pelo
// Windows Update da própria máquina (o catálogo offline que o patchd já distribui, ou a
// Microsoft), pede ao servidor os arquivos dela, e o servidor os baixa da Microsoft uma vez e
// os entrega a todas as máquinas da rede interna.
//
// O que garante que o arquivo é o certo:
//
//   - só endereços de download da Microsoft (download.windowsupdate.com e os da entrega da
//     Microsoft), com as extensões de pacote de atualização: o servidor não vira proxy;
//   - quando o nome traz o SHA-1 do arquivo (o padrão do Windows Update: ..._<sha1>.cab), o
//     servidor confere e apaga o que não bate;
//   - no agente, o Windows Update confere cada arquivo contra a metadado assinado da
//     atualização ao recebê-lo (IUpdate2.CopyToCache), e o agente confere o SHA-256 que o
//     servidor anunciou.
//
// Os arquivos ficam em <dir>/<chave>, com <dir>/<chave>.json ao lado (de onde veio, o
// tamanho, o SHA-256, quando foi baixado e a última vez que uma máquina o pediu).
package wucache

import (
	"context"
	"crypto/sha1" // #nosec G505 -- o SHA-1 é o que a Microsoft põe no nome do arquivo; conferência de integridade, não de segurança
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Estados de um arquivo.
const (
	StateReady    = "pronto"
	StateFetching = "baixando"
	StateFailed   = "falhou"
	StateRefused  = "recusado" // o endereço não é de um pacote da Microsoft
)

const (
	DefaultMaxSize = 8 << 30 // a maior atualização cumulativa tem alguns GB
	defaultWorkers = 2
	idleTimeout    = 2 * time.Minute
	headerTimeout  = time.Minute
	// retryAfter: um arquivo que falhou só é tentado de novo depois disto (as máquinas que
	// perguntam no meio recebem a falha).
	retryAfter = 10 * time.Minute
	// MaxURLs: arquivos por pedido (uma atualização cumulativa tem poucos).
	MaxURLs = 64
)

// allowedExt: as extensões dos pacotes de atualização.
var allowedExt = []string{".cab", ".msu", ".psf", ".wim", ".esd", ".exe"}

// sha1InName: o SHA-1 que o Windows Update põe no fim do nome do arquivo.
var sha1InName = regexp.MustCompile(`_([0-9a-f]{40})\.[a-z0-9]+$`)

// keyPattern: o formato da chave (para quem a recebe de fora).
var keyPattern = regexp.MustCompile(`^[0-9a-f]{16}-[a-z0-9._-]{1,180}$`)

// ValidKey diz se a chave tem o formato das que o cache cria.
func ValidKey(k string) bool { return keyPattern.MatchString(k) && !strings.Contains(k, "..") }

// allowedHost: os hosts de download da Microsoft.
func allowedHost(h string) bool {
	h = strings.ToLower(h)
	return h == "download.windowsupdate.com" || strings.HasSuffix(h, ".download.windowsupdate.com") ||
		h == "download.microsoft.com" || strings.HasSuffix(h, ".delivery.mp.microsoft.com")
}

// Parse confere o endereço e devolve a chave do arquivo no cache. A chave sai do caminho (não
// do host: o mesmo arquivo vem de mais de um host da Microsoft).
func Parse(raw string) (*url.URL, string, error) {
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return nil, "", fmt.Errorf("endereço ilegível")
	case u.Scheme != "http" && u.Scheme != "https":
		return nil, "", fmt.Errorf("só http ou https")
	case u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Port() != "":
		return nil, "", fmt.Errorf("endereço com usuário, porta ou parâmetros: não é o de um pacote da Microsoft")
	case !allowedHost(u.Hostname()):
		return nil, "", fmt.Errorf("o host %q não é de download da Microsoft", u.Hostname())
	}
	p := path.Clean(u.EscapedPath())
	base := strings.ToLower(path.Base(p))
	if !slices.Contains(allowedExt, path.Ext(base)) {
		return nil, "", fmt.Errorf("%q não é um pacote de atualização (%s)", base, strings.Join(allowedExt, ", "))
	}
	clean := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			return r
		}
		return '_'
	}, base)
	if len(clean) > 180 {
		clean = clean[len(clean)-180:]
	}
	sum := sha256.Sum256([]byte(strings.ToLower(p)))
	return u, hex.EncodeToString(sum[:8]) + "-" + clean, nil
}

// Meta é o que o cache sabe de um arquivo.
type Meta struct {
	Key     string    `json:"key"`
	URL     string    `json:"url"`
	Size    int64     `json:"size"`
	SHA256  string    `json:"sha256"`
	Fetched time.Time `json:"fetched"`
	Used    time.Time `json:"used"`
}

// Status é a resposta para um endereço.
type Status struct {
	URL    string
	Key    string
	State  string
	Size   int64 // pronto: o tamanho; baixando: o total, se a origem disse (0: não se sabe)
	Done   int64 // baixando: quanto já veio
	SHA256 string
	Detail string
}

type progress struct {
	url        string
	done, size int64
}

type failure struct {
	err string
	at  time.Time
}

// Cache baixa e guarda.
type Cache struct {
	Dir       string
	HTTP      *http.Client // sem Timeout global: vale o de inatividade
	UserAgent string
	Logger    *slog.Logger
	MaxSize   int64 // por arquivo; 0: DefaultMaxSize
	Workers   int   // downloads ao mesmo tempo; 0: 2
	Idle      time.Duration
	// Origin troca os endereços de origem (testes: um servidor local). nil: a Microsoft.
	Origin func(u *url.URL) []string

	now func() time.Time

	mu      sync.Mutex
	running map[string]*progress
	failed  map[string]failure
	slots   chan struct{}
	once    sync.Once
}

// New prepara o cache na pasta.
func New(dir, userAgent string, logger *slog.Logger) (*Cache, error) {
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("a pasta do cache do Windows precisa ser absoluta (recebido %q)", dir)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	return &Cache{Dir: dir, UserAgent: userAgent, Logger: logger, HTTP: &http.Client{}}, nil
}

func (c *Cache) init() {
	c.once.Do(func() {
		c.running, c.failed = map[string]*progress{}, map[string]failure{}
		n := c.Workers
		if n <= 0 {
			n = defaultWorkers
		}
		c.slots = make(chan struct{}, n)
		if c.now == nil {
			c.now = time.Now
		}
		if c.Logger == nil {
			c.Logger = slog.Default()
		}
	})
}

func (c *Cache) file(key string) string { return filepath.Join(c.Dir, key) }

// Ensure diz o estado de cada endereço e começa a baixar o que falta. Não espera.
func (c *Cache) Ensure(ctx context.Context, urls []string) []Status {
	c.init()
	out := make([]Status, 0, len(urls))
	for _, raw := range urls {
		u, key, err := Parse(raw)
		if err != nil {
			out = append(out, Status{URL: raw, State: StateRefused, Detail: err.Error()})
			continue
		}
		out = append(out, c.ensureOne(u, key))
	}
	return out
}

func (c *Cache) ensureOne(u *url.URL, key string) Status {
	st := Status{URL: u.String(), Key: key}
	if m, ok := c.Ready(key); ok {
		st.State, st.Size, st.SHA256 = StateReady, m.Size, m.SHA256
		return st
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if p, ok := c.running[key]; ok {
		st.State, st.Size, st.Done = StateFetching, p.size, p.done
		return st
	}
	if f, ok := c.failed[key]; ok && c.now().Sub(f.at) < retryAfter {
		st.State, st.Detail = StateFailed, f.err
		return st
	}
	delete(c.failed, key)
	c.running[key] = &progress{url: u.String()}
	go c.fetch(u, key)
	st.State = StateFetching
	return st
}

// Ready devolve o arquivo guardado, se estiver completo.
func (c *Cache) Ready(key string) (Meta, bool) {
	if !ValidKey(key) {
		return Meta{}, false
	}
	b, err := os.ReadFile(c.file(key) + ".json")
	if err != nil {
		return Meta{}, false
	}
	var m Meta
	if json.Unmarshal(b, &m) != nil || m.Key != key {
		return Meta{}, false
	}
	fi, err := os.Stat(c.file(key))
	if err != nil || fi.Size() != m.Size {
		return Meta{}, false
	}
	return m, true
}

func (c *Cache) fetch(u *url.URL, key string) {
	c.slots <- struct{}{}
	defer func() { <-c.slots }()
	err := c.download(u, key)
	c.mu.Lock()
	delete(c.running, key)
	if err != nil {
		c.failed[key] = failure{err: err.Error(), at: c.now()}
	}
	c.mu.Unlock()
	if err != nil {
		c.Logger.Warn("cache do Windows: download falhou", "arquivo", key, "url", u.String(), "error", err)
		return
	}
	c.Logger.Info("cache do Windows: arquivo guardado", "arquivo", key)
}

// origins: os endereços a tentar, https primeiro (o http fica de reserva: a conferência do
// arquivo não depende do transporte).
func (c *Cache) origins(u *url.URL) []string {
	if c.Origin != nil {
		return c.Origin(u)
	}
	s, h := *u, *u
	s.Scheme, h.Scheme = "https", "http"
	if u.Scheme == "https" {
		return []string{s.String()}
	}
	return []string{s.String(), h.String()}
}

func (c *Cache) download(u *url.URL, key string) error {
	var last error
	for _, origin := range c.origins(u) {
		for try := 0; try < 3; try++ {
			if try > 0 {
				time.Sleep(time.Duration(try) * 5 * time.Second)
			}
			err := c.downloadFrom(origin, u.String(), key)
			if err == nil {
				return nil
			}
			last = err
			var p permanent
			if errors.As(err, &p) {
				break
			}
		}
	}
	return last
}

type permanent struct{ error }

func (p permanent) Unwrap() error { return p.error }

func (c *Cache) maxSize() int64 {
	if c.MaxSize > 0 {
		return c.MaxSize
	}
	return DefaultMaxSize
}

func (c *Cache) idle() time.Duration {
	if c.Idle > 0 {
		return c.Idle
	}
	return idleTimeout
}

func (c *Cache) downloadFrom(origin, original, key string) error {
	part := c.file(key) + ".part"
	var have int64
	if fi, err := os.Stat(part); err == nil {
		have = fi.Size()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin, nil)
	if err != nil {
		return permanent{err}
	}
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	if have > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(have, 10)+"-")
	}
	hdr := time.AfterFunc(headerTimeout, cancel)
	resp, err := c.HTTP.Do(req)
	hdr.Stop()
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	flag := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	switch {
	case resp.StatusCode == http.StatusPartialContent && have > 0:
	case resp.StatusCode == http.StatusOK:
		have, flag = 0, os.O_CREATE|os.O_WRONLY|os.O_TRUNC
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusForbidden:
		return permanent{fmt.Errorf("a Microsoft respondeu %s", resp.Status)}
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		_ = os.Remove(part)
		return fmt.Errorf("retomada recusada; recomeça do zero")
	default:
		return fmt.Errorf("a Microsoft respondeu %s", resp.Status)
	}
	total := int64(-1)
	if resp.ContentLength >= 0 {
		total = have + resp.ContentLength
		if total > c.maxSize() {
			return permanent{fmt.Errorf("arquivo de %d bytes passa do limite de %d", total, c.maxSize())}
		}
	}
	c.mu.Lock()
	if p := c.running[key]; p != nil {
		p.done, p.size = have, max(total, 0)
	}
	c.mu.Unlock()
	f, err := os.OpenFile(part, flag, 0o600) // #nosec G304 -- caminho do próprio cache, chave validada
	if err != nil {
		return permanent{err}
	}
	stall := time.AfterFunc(c.idle(), cancel)
	buf := make([]byte, 256<<10)
	n := have
	for {
		k, rerr := resp.Body.Read(buf)
		if k > 0 {
			stall.Reset(c.idle())
			if _, werr := f.Write(buf[:k]); werr != nil {
				stall.Stop()
				_ = f.Close()
				return permanent{werr}
			}
			n += int64(k)
			if n > c.maxSize() {
				stall.Stop()
				_ = f.Close()
				_ = os.Remove(part)
				return permanent{fmt.Errorf("arquivo passa do limite de %d bytes", c.maxSize())}
			}
			c.mu.Lock()
			if p := c.running[key]; p != nil {
				p.done = n
			}
			c.mu.Unlock()
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			stall.Stop()
			_ = f.Close()
			return fmt.Errorf("download interrompido em %d bytes: %w", n, rerr)
		}
	}
	stall.Stop()
	if err := f.Close(); err != nil {
		return permanent{err}
	}
	if total >= 0 && n != total {
		return fmt.Errorf("download incompleto: %d de %d bytes", n, total)
	}
	return c.finish(part, original, key, n)
}

// finish confere o arquivo baixado e o publica.
func (c *Cache) finish(part, original, key string, n int64) error {
	f, err := os.Open(part) // #nosec G304 -- caminho do próprio cache
	if err != nil {
		return permanent{err}
	}
	h256, h1 := sha256.New(), sha1.New() // #nosec G401 -- o SHA-1 do nome do arquivo (ver o pacote)
	_, err = io.Copy(io.MultiWriter(h256, h1), f)
	_ = f.Close()
	if err != nil {
		return permanent{err}
	}
	if m := sha1InName.FindStringSubmatch(key); m != nil && hex.EncodeToString(h1.Sum(nil)) != m[1] {
		_ = os.Remove(part)
		return permanent{fmt.Errorf("o SHA-1 do arquivo não é o do nome (%s): apagado", m[1])}
	}
	if err := os.Rename(part, c.file(key)); err != nil {
		return permanent{err}
	}
	now := c.now().UTC()
	m := Meta{Key: key, URL: original, Size: n, SHA256: hex.EncodeToString(h256.Sum(nil)), Fetched: now, Used: now}
	return c.writeMeta(m)
}

func (c *Cache) writeMeta(m Meta) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp := c.file(m.Key) + ".json.tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.file(m.Key)+".json")
}

// Open abre um arquivo pronto para servir e anota o uso.
func (c *Cache) Open(key string) (*os.File, Meta, error) {
	c.init()
	m, ok := c.Ready(key)
	if !ok {
		return nil, Meta{}, os.ErrNotExist
	}
	f, err := os.Open(c.file(key)) // #nosec G304 -- chave validada em Ready
	if err != nil {
		return nil, Meta{}, err
	}
	if now := c.now().UTC(); now.Sub(m.Used) > time.Hour {
		m.Used = now
		if err := c.writeMeta(m); err != nil {
			c.Logger.Warn("cache do Windows: uso não anotado", "arquivo", key, "error", err)
		}
	}
	return f, m, nil
}

// Stats é o resumo para o painel.
type Stats struct {
	Files    int
	Bytes    int64
	Fetching []Status
	Recent   []Meta // os mais usados por último primeiro
}

// List devolve o que está guardado.
func (c *Cache) List() ([]Meta, error) {
	entries, err := os.ReadDir(c.Dir)
	if err != nil {
		return nil, err
	}
	var out []Meta
	for _, e := range entries {
		key, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok {
			continue
		}
		if m, ok := c.Ready(key); ok {
			out = append(out, m)
		}
	}
	slices.SortFunc(out, func(a, b Meta) int { return b.Used.Compare(a.Used) })
	return out, nil
}

// Stats resume o cache.
func (c *Cache) Stats() (Stats, error) {
	c.init()
	list, err := c.List()
	if err != nil {
		return Stats{}, err
	}
	s := Stats{Files: len(list), Recent: list}
	for _, m := range list {
		s.Bytes += m.Size
	}
	c.mu.Lock()
	for key, p := range c.running {
		s.Fetching = append(s.Fetching, Status{URL: p.url, Key: key, State: StateFetching, Size: p.size, Done: p.done})
	}
	c.mu.Unlock()
	slices.SortFunc(s.Fetching, func(a, b Status) int { return strings.Compare(a.Key, b.Key) })
	return s, nil
}

// Prune apaga o que nenhuma máquina pediu desde before, e os downloads parados há mais de
// dois dias. Devolve quantos arquivos e bytes saíram.
func (c *Cache) Prune(before time.Time) (int, int64, error) {
	c.init()
	entries, err := os.ReadDir(c.Dir)
	if err != nil {
		return 0, 0, err
	}
	n, bytes := 0, int64(0)
	c.mu.Lock()
	running := map[string]bool{}
	for k := range c.running {
		running[k] = true
	}
	c.mu.Unlock()
	for _, e := range entries {
		name := e.Name()
		if key, ok := strings.CutSuffix(name, ".part"); ok && !running[key] {
			if fi, err := e.Info(); err == nil && c.now().Sub(fi.ModTime()) > 48*time.Hour {
				_ = os.Remove(filepath.Join(c.Dir, name))
			}
			continue
		}
		key, ok := strings.CutSuffix(name, ".json")
		if !ok {
			continue
		}
		m, ok := c.Ready(key)
		if !ok || !m.Used.Before(before) {
			continue
		}
		if err := os.Remove(c.file(key)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return n, bytes, err
		}
		_ = os.Remove(c.file(key) + ".json")
		n++
		bytes += m.Size
	}
	return n, bytes, nil
}
