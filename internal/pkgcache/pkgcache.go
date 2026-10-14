// Package pkgcache baixa os instaladores de programas de terceiros para o servidor e só
// os guarda se o SHA-256 for o do manifesto do winget (Aula 14.2).
//
// O arquivo guardado tem o SHA-256 no nome (<sha256>.msi ou <sha256>.exe): quem pede pelo
// nome recebe sempre o mesmo conteúdo, e o agente confere de novo antes de executar (Aula
// 14.3). O que não confere é apagado: o servidor não guarda binário que ninguém atestou.
package pkgcache

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

const (
	DefaultMaxSize = 2 << 30 // 2 GiB: o maior da lista padrão (LibreOffice) tem algumas centenas de MB
	defaultTries   = 3
	defaultWait    = 5 * time.Second
	idleTimeout    = 2 * time.Minute
)

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// FileName é o nome do instalador no cache: o SHA-256 e a extensão que o Windows espera
// (o msiexec abre .msi; os demais são executáveis).
func FileName(sha256hex string, msi bool) string {
	if msi {
		return sha256hex + ".msi"
	}
	return sha256hex + ".exe"
}

// Fetcher baixa para o cache.
type Fetcher struct {
	HTTP      *http.Client // sem timeout global: o limite é o de inatividade
	UserAgent string
	MaxSize   int64         // 0: DefaultMaxSize
	Tries     int           // 0: 3
	Wait      time.Duration // espera antes da tentativa n+1: n×Wait (0: 5 s)
	Idle      time.Duration // sem nenhum byte por esse tempo, a tentativa cai (0: 2 min)
}

// NewFetcher devolve um Fetcher que só segue redirecionamentos para https.
func NewFetcher(userAgent string) *Fetcher {
	return &Fetcher{HTTP: &http.Client{CheckRedirect: HTTPSOnly}, UserAgent: userAgent}
}

// ErrNotHTTPS: um redirecionamento levou para fora do https.
var ErrNotHTTPS = errors.New("redirecionamento para fora do https")

// HTTPSOnly recusa redirecionamento para fora do https (o GitHub manda para outro host; a
// Mozilla, para a CDN): o hash protege o conteúdo, mas um http no meio é sinal de erro.
func HTTPSOnly(req *http.Request, via []*http.Request) error {
	if req.URL.Scheme != "https" {
		return fmt.Errorf("%w: %s", ErrNotHTTPS, req.URL.Redacted())
	}
	if len(via) >= 10 {
		return errors.New("mais de 10 redirecionamentos")
	}
	return nil
}

// Result é o instalador guardado.
type Result struct {
	Path     string
	Size     int64
	Cached   bool // já estava no cache, conferido
	Duration time.Duration
}

// HashMismatchError: o endereço entregou um arquivo diferente do que o manifesto atesta.
type HashMismatchError struct {
	URL       string
	Want, Got string
	Size      int64
}

func (e *HashMismatchError) Error() string {
	return fmt.Sprintf("o SHA-256 do arquivo baixado (%s…, %s) não é o do manifesto (%s…)", e.Got[:12], Bytes(e.Size), e.Want[:12])
}

// permanent: não melhora repetindo (404, arquivo grande demais, hash diferente).
type permanent struct{ error }

func (p permanent) Unwrap() error { return p.error }

// Fetch garante no dir o instalador com esse SHA-256: confere o que já está lá ou baixa de
// url. progress recebe linhas de andamento (pode ser nil).
func (f *Fetcher) Fetch(ctx context.Context, dir, url, want string, msi bool, progress func(string)) (Result, error) {
	if progress == nil {
		progress = func(string) {}
	}
	want = strings.ToLower(want)
	if !sha256Hex.MatchString(want) {
		return Result{}, fmt.Errorf("SHA-256 esperado %q inválido", want)
	}
	if !strings.HasPrefix(url, "https://") {
		return Result{}, fmt.Errorf("endereço %q: só https", url)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return Result{}, err
	}
	final := filepath.Join(dir, FileName(want, msi))
	start := time.Now()

	// No cache: confere o conteúdo, não só o nome (disco, cópia manual, restauração).
	if got, size, err := hashFile(final); err == nil {
		if got == want {
			return Result{Path: final, Size: size, Cached: true, Duration: time.Since(start)}, nil
		}
		progress(fmt.Sprintf("o arquivo do cache não confere (SHA-256 %s…): baixando de novo", got[:12]))
		if err := os.Remove(final); err != nil {
			return Result{}, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Result{}, err
	}

	var lastErr error
	for try := 1; try <= f.tries(); try++ {
		if try > 1 {
			progress(fmt.Sprintf("tentativa %d: %v", try, lastErr))
			if err := sleep(ctx, time.Duration(try-1)*f.wait()); err != nil {
				return Result{}, err
			}
		}
		size, err := f.download(ctx, dir, final, url, want, progress)
		if err == nil {
			return Result{Path: final, Size: size, Duration: time.Since(start)}, nil
		}
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		var perm permanent
		if errors.As(err, &perm) {
			return Result{}, perm.error
		}
		lastErr = err
	}
	return Result{}, fmt.Errorf("download falhou depois de %d tentativa(s): %w", f.tries(), lastErr)
}

// download baixa para um arquivo temporário na mesma pasta, calculando o SHA-256 no
// caminho, e só renomeia para o nome final se conferir.
func (f *Fetcher) download(ctx context.Context, dir, final, url, want string, progress func(string)) (int64, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, permanent{err}
	}
	req.Header.Set("User-Agent", f.UserAgent)
	resp, err := f.HTTP.Do(req)
	if err != nil {
		if errors.Is(err, ErrNotHTTPS) {
			return 0, permanent{err}
		}
		return 0, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return 0, fmt.Errorf("GET %s: %s", url, resp.Status)
	default:
		return 0, permanent{fmt.Errorf("GET %s: %s", url, resp.Status)}
	}
	max := f.maxSize()
	if resp.ContentLength > max {
		return 0, permanent{fmt.Errorf("o arquivo tem %s; o limite é %s", Bytes(resp.ContentLength), Bytes(max))}
	}

	tmp := filepath.Join(dir, ".baixando-"+randomHex())
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return 0, permanent{err}
	}
	keep := false
	defer func() {
		if !keep {
			out.Close()
			os.Remove(tmp)
		}
	}()
	h := sha256.New()
	pw := &progressWriter{total: resp.ContentLength, report: progress}
	body := &idleReader{r: io.LimitReader(resp.Body, max+1), idle: f.idle(), cancel: cancel}
	n, err := io.Copy(io.MultiWriter(out, h, pw), body)
	body.stop()
	switch {
	case body.timedOut():
		return n, fmt.Errorf("nenhum byte em %s", f.idle())
	case err != nil:
		return n, err
	case n > max:
		return n, permanent{fmt.Errorf("o arquivo passou de %s", Bytes(max))}
	case resp.ContentLength >= 0 && n != resp.ContentLength:
		return n, fmt.Errorf("o corpo terminou em %s de %s", Bytes(n), Bytes(resp.ContentLength))
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return n, permanent{&HashMismatchError{URL: url, Want: want, Got: got, Size: n}}
	}
	if err := out.Sync(); err != nil {
		return n, err
	}
	if err := out.Close(); err != nil {
		return n, err
	}
	if err := os.Rename(tmp, final); err != nil {
		return n, err
	}
	keep = true
	return n, nil
}

// hashFile calcula o SHA-256 de um arquivo.
func hashFile(path string) (string, int64, error) {
	fh, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer fh.Close()
	h := sha256.New()
	n, err := io.Copy(h, fh)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// Check confere um instalador do cache: existe e tem o SHA-256 do nome.
func Check(dir, sha256hex string, msi bool) (int64, error) {
	got, size, err := hashFile(filepath.Join(dir, FileName(sha256hex, msi)))
	if err != nil {
		return 0, err
	}
	if got != sha256hex {
		return size, fmt.Errorf("conteúdo com SHA-256 %s…", got[:12])
	}
	return size, nil
}

// ErrMissing: o instalador não está no cache.
var ErrMissing = errors.New("o instalador não está no cache")

// Present confere, sem ler o arquivo, que o instalador está no cache com o tamanho
// guardado (Aula 14.6): é o que o painel usa para montar a página. Criar o job continua
// exigindo Check, que confere o SHA-256.
func Present(dir, sha256hex string, msi bool, size int64) error {
	st, err := os.Stat(filepath.Join(dir, FileName(sha256hex, msi)))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return ErrMissing
	case err != nil:
		return err
	case !st.Mode().IsRegular():
		return fmt.Errorf("%s não é um arquivo comum", FileName(sha256hex, msi))
	case st.Size() != size:
		return fmt.Errorf("o arquivo no cache tem %d bytes; o guardado diz %d", st.Size(), size)
	}
	return nil
}

// Bytes formata um tamanho: 1,6 MiB.
func Bytes(n int64) string {
	switch {
	case n >= 1<<30:
		return strings.Replace(fmt.Sprintf("%.1f GiB", float64(n)/(1<<30)), ".", ",", 1)
	case n >= 1<<20:
		return strings.Replace(fmt.Sprintf("%.1f MiB", float64(n)/(1<<20)), ".", ",", 1)
	case n >= 1<<10:
		return strings.Replace(fmt.Sprintf("%.1f KiB", float64(n)/(1<<10)), ".", ",", 1)
	}
	return fmt.Sprintf("%d bytes", n)
}

func (f *Fetcher) maxSize() int64 {
	if f.MaxSize > 0 {
		return f.MaxSize
	}
	return DefaultMaxSize
}

func (f *Fetcher) tries() int {
	if f.Tries > 0 {
		return f.Tries
	}
	return defaultTries
}

func (f *Fetcher) wait() time.Duration {
	if f.Wait > 0 {
		return f.Wait
	}
	return defaultWait
}

func (f *Fetcher) idle() time.Duration {
	if f.Idle > 0 {
		return f.Idle
	}
	return idleTimeout
}

func randomHex() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// progressWriter avisa a cada 25% do total, quando o total é conhecido e passa de 20 MiB.
type progressWriter struct {
	total, done int64
	next        int64
	report      func(string)
}

func (p *progressWriter) Write(b []byte) (int, error) {
	p.done += int64(len(b))
	if p.total < 20<<20 {
		return len(b), nil
	}
	if p.next == 0 {
		p.next = 25
	}
	for p.next < 100 && p.done*100/p.total >= p.next {
		p.report(fmt.Sprintf("%3d%% (%s de %s)", p.next, Bytes(p.done), Bytes(p.total)))
		p.next += 25
	}
	return len(b), nil
}

// idleReader cancela o pedido quando passa idle sem nenhum byte.
type idleReader struct {
	r      io.Reader
	idle   time.Duration
	cancel context.CancelFunc
	timer  *time.Timer
	fired  atomic.Bool
}

func (r *idleReader) Read(p []byte) (int, error) {
	if r.timer == nil {
		r.timer = time.AfterFunc(r.idle, func() { r.fired.Store(true); r.cancel() })
	}
	n, err := r.r.Read(p)
	if n > 0 {
		r.timer.Reset(r.idle)
	}
	return n, err
}

func (r *idleReader) stop() {
	if r.timer != nil {
		r.timer.Stop()
	}
}

func (r *idleReader) timedOut() bool { return r.fired.Load() }

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
