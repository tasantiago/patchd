// Package wsusscan baixa o wsusscn2.cab, o catálogo offline do Windows Update (Aula 6.6).
//
// O arquivo tem centenas de MB (674 424 266 bytes em 03/10/2026) e não cabe no modelo das
// outras fontes (ler a resposta inteira na memória). Aqui o download vai direto para o
// disco, retoma de onde parou (Range com If-Range: o servidor da Microsoft aceita, responde
// 206) e só é refeito quando o ETag muda. O arquivo publicado tem o SHA-256 no nome: quem
// baixar pelo nome recebe sempre o mesmo conteúdo.
//
// Conferências no servidor: tamanho igual ao Content-Length, começo "MSCF" e o tamanho do
// gabinete (cbCabinet, no cabeçalho) menor ou igual ao arquivo. O que sobra depois do
// gabinete é a assinatura Authenticode da Microsoft, anexada (10 096 bytes no arquivo de
// 03/10/2026). A assinatura em si é conferida no Windows, pelo agente (Aula 6.6, parte 3c).
package wsusscan

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// DefaultURL é o endereço para onde o link oficial (go.microsoft.com/fwlink/?LinkID=74689)
// redireciona, por HTTPS.
const DefaultURL = "https://catalog.s.download.windowsupdate.com/microsoftupdate/v6/wsusscan/wsusscn2.cab"

const (
	partName       = "wsusscn2.cab.part"
	maxSignature   = 4 << 20 // folga para a assinatura anexada depois do gabinete
	defaultTries   = 5
	defaultWait    = 10 * time.Second
	idleTimeout    = 2 * time.Minute // sem nenhum byte por esse tempo, a tentativa é abandonada
	headerTimeout  = time.Minute
	progressEveryN = 10 // percentuais entre as linhas de progresso
)

// File é o catálogo publicado no diretório de conteúdo.
type File struct {
	Name         string // nome no diretório: wsusscn2-<16 primeiros do SHA-256>.cab
	Size         int64
	SHA256       string
	ETag         string    // como a Microsoft mandou, com as aspas
	LastModified time.Time // publicação do arquivo pela Microsoft
	Cabinet      int64     // cbCabinet: o gabinete sem a assinatura anexada
}

// Remote é o que o HEAD diz do arquivo da Microsoft.
type Remote struct {
	Size         int64
	ETag         string
	LastModified time.Time
	// ifRange é o validador do If-Range: o ETag de verdade, ou a data quando a origem não
	// manda ETag. Vazio: sem retomada (Range sem If-Range poderia juntar dois arquivos).
	ifRange string
}

// Client baixa o catálogo. Os campos são exportados para os testes.
type Client struct {
	URL       string
	UserAgent string
	HTTP      *http.Client // sem Timeout global: o download leva minutos; vale o idleTimeout
	Tries     int
	Wait      time.Duration
	Idle      time.Duration
}

// NewClient devolve o cliente para o endereço da Microsoft.
func NewClient(userAgent string) *Client {
	t := http.DefaultTransport.(*http.Transport).Clone() // mantém o proxy do ambiente
	t.ResponseHeaderTimeout = headerTimeout
	return &Client{URL: DefaultURL, UserAgent: userAgent, HTTP: &http.Client{Transport: t},
		Tries: defaultTries, Wait: defaultWait, Idle: idleTimeout}
}

// Head consulta o arquivo da Microsoft sem baixá-lo.
func (c *Client) Head(ctx context.Context) (Remote, error) {
	var r Remote
	var err error
	for try := 1; try <= c.tries(); try++ {
		r, err = c.head(ctx)
		if err == nil || ctx.Err() != nil {
			return r, err
		}
		if try < c.tries() {
			if werr := sleep(ctx, time.Duration(try)*c.Wait); werr != nil {
				return r, werr
			}
		}
	}
	return r, fmt.Errorf("%w (depois de %d tentativa(s))", err, c.tries())
}

func (c *Client) head(ctx context.Context) (Remote, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, c.URL, nil)
	if err != nil {
		return Remote{}, err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Remote{}, err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Remote{}, fmt.Errorf("HEAD %s: %s", c.URL, resp.Status)
	}
	r := Remote{Size: resp.ContentLength, ETag: resp.Header.Get("ETag")}
	if t, err := http.ParseTime(resp.Header.Get("Last-Modified")); err == nil {
		r.LastModified = t.UTC()
	}
	if r.Size <= 0 {
		return r, fmt.Errorf("HEAD %s: sem Content-Length", c.URL)
	}
	r.ifRange = r.ETag
	if r.ETag == "" {
		// Sem ETag, a identidade do arquivo é o tamanho com a data, e o If-Range vai com a data.
		r.ETag = fmt.Sprintf("%d-%d", r.Size, r.LastModified.Unix())
		if !r.LastModified.IsZero() {
			r.ifRange = r.LastModified.Format(http.TimeFormat)
		}
	}
	return r, nil
}

// Sync deixa o catálogo atual em dir. Com prev igual ao arquivo remoto (mesmo ETag e
// tamanho, e o arquivo no disco), nada é baixado. Senão, baixa (retomando um download
// interrompido do mesmo ETag), confere e publica com o hash no nome. O arquivo anterior
// fica até a próxima publicação: um agente no meio do download dele não é cortado.
// progress recebe linhas de andamento (pode ser nil).
func (c *Client) Sync(ctx context.Context, dir string, prev *File, progress func(string)) (File, bool, error) {
	if progress == nil {
		progress = func(string) {}
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return File{}, false, err
	}
	remote, err := c.Head(ctx)
	if err != nil {
		return File{}, false, err
	}
	if prev != nil && prev.ETag == remote.ETag && prev.Size == remote.Size {
		if st, err := os.Stat(filepath.Join(dir, prev.Name)); err == nil && st.Size() == prev.Size {
			return *prev, false, nil
		}
		progress("o arquivo guardado sumiu do disco: baixando de novo")
	}

	part := filepath.Join(dir, partName)
	if err := c.download(ctx, part, remote, progress); err != nil {
		return File{}, false, err
	}
	f, err := inspect(part, remote)
	if err != nil {
		os.Remove(part) // conteúdo errado: a próxima execução começa do zero
		os.Remove(part + ".etag")
		return File{}, false, err
	}
	f.Name = "wsusscn2-" + f.SHA256[:16] + ".cab"
	if err := os.Rename(part, filepath.Join(dir, f.Name)); err != nil {
		return File{}, false, err
	}
	os.Remove(part + ".etag")
	keep := map[string]bool{f.Name: true}
	if prev != nil {
		keep[prev.Name] = true
	}
	removeOld(dir, keep)
	return f, true, nil
}

// download baixa para part, retomando quando o .etag ao lado diz que o pedaço já baixado é
// do mesmo arquivo. Cada tentativa continua de onde a anterior parou.
func (c *Client) download(ctx context.Context, part string, remote Remote, progress func(string)) error {
	var offset int64
	if b, err := os.ReadFile(part + ".etag"); err == nil && string(b) == remote.ETag && remote.ifRange != "" {
		if st, err := os.Stat(part); err == nil && st.Size() <= remote.Size {
			offset = st.Size()
		}
	}
	if offset == 0 {
		if err := os.WriteFile(part+".etag", []byte(remote.ETag), 0o640); err != nil {
			return err
		}
		if err := os.WriteFile(part, nil, 0o640); err != nil {
			return err
		}
	} else {
		progress(fmt.Sprintf("retomando o download em %s de %s", mib(offset), mib(remote.Size)))
	}

	var lastErr error
	for try := 1; offset < remote.Size; try++ {
		if try > c.tries() {
			return fmt.Errorf("download do wsusscn2.cab parou em %s de %s: %w (depois de %d tentativa(s); a próxima execução continua daí)",
				mib(offset), mib(remote.Size), lastErr, c.tries())
		}
		if try > 1 {
			progress(fmt.Sprintf("tentativa %d, a partir de %s: %v", try, mib(offset), lastErr))
			if err := sleep(ctx, time.Duration(try-1)*c.Wait); err != nil {
				return err
			}
		}
		n, err := c.fetch(ctx, part, offset, remote, progress)
		offset = n
		if err == nil {
			continue
		}
		if ctx.Err() != nil {
			return fmt.Errorf("download interrompido em %s de %s (a próxima execução continua daí): %w", mib(offset), mib(remote.Size), ctx.Err())
		}
		var perm permanent
		if errors.As(err, &perm) {
			return err
		}
		lastErr = err
	}
	return nil
}

// permanent é um erro que não melhora repetindo (o arquivo mudou no meio, 404...).
type permanent struct{ error }

// fetch faz um pedido a partir de offset e escreve em part. Devolve o novo tamanho do
// arquivo local, mesmo em caso de erro (o que chegou fica).
func (c *Client) fetch(ctx context.Context, part string, offset int64, remote Remote, progress func(string)) (int64, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL, nil)
	if err != nil {
		return offset, err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	if offset > 0 && remote.ifRange != "" {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		req.Header.Set("If-Range", remote.ifRange) // se o arquivo mudou, o servidor manda tudo (200)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return offset, err
	}
	defer resp.Body.Close()

	flags := os.O_WRONLY | os.O_APPEND
	switch resp.StatusCode {
	case http.StatusPartialContent:
		if start := rangeStart(resp.Header.Get("Content-Range")); start != offset {
			return offset, fmt.Errorf("Content-Range começa em %d, esperado %d", start, offset)
		}
	case http.StatusOK:
		if resp.Header.Get("ETag") != "" && remote.ifRange == remote.ETag && resp.Header.Get("ETag") != remote.ETag {
			return offset, permanent{fmt.Errorf("o arquivo da Microsoft mudou durante o download (ETag %s, era %s); a próxima execução recomeça",
				resp.Header.Get("ETag"), remote.ETag)}
		}
		if offset > 0 {
			progress("o servidor mandou o arquivo inteiro (200): recomeçando do zero")
		}
		offset, flags = 0, os.O_WRONLY|os.O_TRUNC
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return offset, fmt.Errorf("GET %s: %s", c.URL, resp.Status)
	default:
		return offset, permanent{fmt.Errorf("GET %s: %s", c.URL, resp.Status)}
	}

	f, err := os.OpenFile(part, flags, 0o640)
	if err != nil {
		return offset, permanent{err}
	}
	w := &progressWriter{total: remote.Size, done: offset, report: progress}
	w.next = (w.done*100/w.total/progressEveryN + 1) * progressEveryN
	body := &idleReader{r: resp.Body, idle: c.idle(), cancel: cancel}
	n, cerr := io.Copy(io.MultiWriter(f, w), body)
	body.stop()
	if err := f.Close(); err != nil && cerr == nil {
		cerr = err
	}
	offset += n
	if cerr == nil && offset < remote.Size {
		cerr = fmt.Errorf("o corpo terminou em %s de %s", mib(offset), mib(remote.Size))
	}
	if body.timedOut() {
		cerr = fmt.Errorf("nenhum byte em %s", c.idle())
	}
	return offset, cerr
}

// inspect confere o arquivo baixado e calcula o SHA-256.
func inspect(path string, remote Remote) (File, error) {
	f, err := os.Open(path)
	if err != nil {
		return File{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return File{}, err
	}
	if st.Size() != remote.Size {
		return File{}, fmt.Errorf("wsusscn2.cab com %d bytes, o servidor anunciou %d", st.Size(), remote.Size)
	}
	var hdr [12]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil {
		return File{}, fmt.Errorf("wsusscn2.cab ilegível: %w", err)
	}
	if string(hdr[:4]) != "MSCF" {
		return File{}, fmt.Errorf("wsusscn2.cab não é um gabinete (começa com %q, esperado \"MSCF\")", hdr[:4])
	}
	cabinet := int64(binary.LittleEndian.Uint32(hdr[8:12]))
	if cabinet > st.Size() || st.Size()-cabinet > maxSignature {
		return File{}, fmt.Errorf("wsusscn2.cab: o gabinete declara %d bytes num arquivo de %d", cabinet, st.Size())
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return File{}, err
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return File{}, err
	}
	return File{Size: st.Size(), SHA256: hex.EncodeToString(h.Sum(nil)), ETag: remote.ETag,
		LastModified: remote.LastModified, Cabinet: cabinet}, nil
}

// removeOld apaga os catálogos antigos do diretório, menos os de keep.
func removeOld(dir string, keep map[string]bool) {
	old, _ := filepath.Glob(filepath.Join(dir, "wsusscn2-*.cab"))
	for _, p := range old {
		if !keep[filepath.Base(p)] {
			os.Remove(p)
		}
	}
}

func (c *Client) tries() int {
	if c.Tries <= 0 {
		return defaultTries
	}
	return c.Tries
}

func (c *Client) idle() time.Duration {
	if c.Idle <= 0 {
		return idleTimeout
	}
	return c.Idle
}

// rangeStart lê o início de "bytes 100-199/200".
func rangeStart(v string) int64 {
	v = strings.TrimPrefix(v, "bytes ")
	start, _, ok := strings.Cut(v, "-")
	if !ok {
		return -1
	}
	n, err := strconv.ParseInt(start, 10, 64)
	if err != nil {
		return -1
	}
	return n
}

func mib(n int64) string { return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20)) }

// progressWriter avisa a cada 10% do total.
type progressWriter struct {
	total, done, next int64
	report            func(string)
}

func (p *progressWriter) Write(b []byte) (int, error) {
	p.done += int64(len(b))
	for p.total > 0 && p.next <= 100 && p.done*100/p.total >= p.next {
		p.report(fmt.Sprintf("%3d%% (%s de %s)", p.next, mib(p.done), mib(p.total)))
		p.next += progressEveryN
	}
	return len(b), nil
}

// idleReader cancela o pedido quando passa idle sem nenhum byte: uma conexão parada não
// prende a sincronização para sempre (o cliente não tem timeout global).
type idleReader struct {
	r      io.Reader
	idle   time.Duration
	cancel context.CancelFunc
	timer  *time.Timer
	fired  atomic.Bool // escrito pelo timer, lido por quem lê o corpo
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
