package agent

import (
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
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

const (
	offlineDir     = "offline"           // dentro da pasta de dados: só SYSTEM e administradores
	offlinePart    = "wsusscn2.cab.part" // download em andamento
	offlinePartSHA = offlinePart + ".sha256"
	offlineState   = "wsusscn2.json"
	// maxOfflineSize: o arquivo tem ~650 MB; um anúncio acima disso é recusado (um servidor
	// comprometido não enche o disco da máquina).
	maxOfflineSize     = 2 << 30
	offlineIdleTimeout = 2 * time.Minute // sem nenhum byte por esse tempo, o download para
	offlineHeaderWait  = time.Minute
)

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// OfflineCab mantém na pasta de dados o wsusscn2.cab distribuído pelo servidor (Aula 6.6).
//
// Duas conferências, com papéis diferentes. O SHA-256 anunciado no check-in garante que o
// arquivo chegou inteiro e é o que o servidor quis entregar. A assinatura Authenticode da
// Microsoft, conferida pelo próprio Windows, garante que o conteúdo é da Microsoft: o
// servidor não tem chave nenhuma para o catálogo, então nem um servidor comprometido
// consegue fazer o agente usar um catálogo falso.
type OfflineCab struct {
	Client  *Client
	DataDir string
	// Verify confere a assinatura e devolve quem assinou. Obrigatória: sem ela, nada é baixado.
	Verify func(path string) (signer string, err error)
	Logger *slog.Logger

	// Injetáveis nos testes.
	now  func() time.Time
	idle time.Duration
}

// CabState é o catálogo em uso e o último recusado (wsusscn2.json na pasta offline).
type CabState struct {
	File        string    `json:"file,omitempty"`
	SHA256      string    `json:"sha256,omitempty"`
	Size        int64     `json:"size,omitempty"`
	PublishedAt time.Time `json:"published_at,omitzero"`
	Signer      string    `json:"signer,omitempty"`
	VerifiedAt  time.Time `json:"verified_at,omitzero"`
	// Rejected: SHA-256 de um arquivo baixado e recusado (hash ou assinatura). O mesmo anúncio
	// só é baixado de novo depois de rejectRetry (não a cada check-in); um arquivo novo no
	// servidor tem outro hash e é baixado na hora.
	Rejected      string    `json:"rejected,omitempty"`
	RejectedAt    time.Time `json:"rejected_at,omitzero"`
	RejectedError string    `json:"rejected_error,omitempty"`
}

// rejectRetry: quando um catálogo recusado volta a ser tentado.
const rejectRetry = 24 * time.Hour

func (o *OfflineCab) dir() string { return filepath.Join(o.DataDir, offlineDir) }

// State devolve o que está gravado sobre o catálogo (em uso e recusado).
func (o *OfflineCab) State() CabState { return o.load() }

func (o *OfflineCab) load() CabState {
	var st CabState
	if data, err := os.ReadFile(filepath.Join(o.dir(), offlineState)); err == nil {
		_ = json.Unmarshal(data, &st)
	}
	return st
}

// Path devolve o catálogo baixado e conferido; "" quando não há.
func (o *OfflineCab) Path() string {
	st := o.load()
	if st.File == "" || filepath.Base(st.File) != st.File {
		return ""
	}
	p := filepath.Join(o.dir(), st.File)
	if fi, err := os.Stat(p); err != nil || fi.Size() != st.Size {
		return ""
	}
	return p
}

// cabName é o nome do arquivo conferido, igual ao do servidor.
func cabName(sum string) string { return "wsusscn2-" + sum[:16] + ".cab" }

// Sync deixa na pasta de dados o catálogo anunciado. Já sendo o atual, não faz nada. Um
// download interrompido (rede, serviço parado) fica no disco e continua no próximo check-in.
func (o *OfflineCab) Sync(ctx context.Context, adv protocol.OfflineCatalog) error {
	if o.now == nil {
		o.now = time.Now
	}
	if o.Verify == nil {
		return errors.New("catálogo offline: sem conferência de assinatura neste sistema")
	}
	if !sha256Hex.MatchString(adv.SHA256) || adv.Size <= 0 || adv.Size > maxOfflineSize {
		return fmt.Errorf("catálogo offline: anúncio inválido (sha256 %q, %d bytes)", adv.SHA256, adv.Size)
	}
	st := o.load()
	if st.SHA256 == adv.SHA256 && o.Path() != "" {
		return nil
	}
	if st.Rejected == adv.SHA256 && o.now().Sub(st.RejectedAt) < rejectRetry {
		o.Logger.Debug("catálogo offline anunciado já foi recusado; ignorado", "sha256", adv.SHA256[:16], "error", st.RejectedError)
		return nil
	}
	if err := os.MkdirAll(o.dir(), 0o700); err != nil {
		return err
	}

	part := filepath.Join(o.dir(), offlinePart)
	offset := o.resumeOffset(part, adv)
	start := o.now()
	o.Logger.Info("catálogo offline: download iniciado", "sha256", adv.SHA256[:16], "size_mib", mibs(adv.Size),
		"resume_from_mib", mibs(offset), "published_at", adv.PublishedAt)
	n, err := o.fetch(ctx, part, offset, adv)
	if err != nil {
		return fmt.Errorf("catálogo offline: download parou em %s MiB de %s MiB (continua no próximo check-in): %w",
			mibs(n), mibs(adv.Size), err)
	}

	sum, err := fileSHA256(part)
	if err != nil {
		return err
	}
	if sum != adv.SHA256 {
		os.Remove(part)
		os.Remove(part + ".sha256")
		return o.reject(st, adv, fmt.Errorf("o arquivo baixado tem SHA-256 %s, o anunciado é %s", sum[:16], adv.SHA256[:16]))
	}
	final := filepath.Join(o.dir(), cabName(adv.SHA256))
	if err := os.Rename(part, final); err != nil {
		return err
	}
	os.Remove(filepath.Join(o.dir(), offlinePartSHA))
	// A assinatura é conferida no arquivo com o nome final (.cab): o Windows reconhece o
	// formato pelo conteúdo, mas o nome certo evita qualquer dúvida.
	signer, err := o.Verify(final)
	if err != nil {
		os.Remove(final)
		if signer != "" {
			err = fmt.Errorf("%w; cadeia: %s", err, signer)
		}
		return o.reject(st, adv, fmt.Errorf("assinatura recusada: %w", err))
	}

	old := st.File
	st = CabState{File: cabName(adv.SHA256), SHA256: adv.SHA256, Size: adv.Size, PublishedAt: adv.PublishedAt,
		Signer: signer, VerifiedAt: o.now().UTC()}
	if err := writeJSONAtomic(filepath.Join(o.dir(), offlineState), st); err != nil {
		return err
	}
	if old != "" && old != st.File && filepath.Base(old) == old {
		os.Remove(filepath.Join(o.dir(), old))
	}
	o.Logger.Info("catálogo offline: pronto para a busca", "file", st.File, "signer", signer,
		"published_at", adv.PublishedAt, "duration", o.now().Sub(start).Round(time.Second).String())
	return nil
}

// reject registra o anúncio recusado e devolve o erro.
func (o *OfflineCab) reject(st CabState, adv protocol.OfflineCatalog, err error) error {
	st.Rejected, st.RejectedAt, st.RejectedError = adv.SHA256, o.now().UTC(), err.Error()
	_ = writeJSONAtomic(filepath.Join(o.dir(), offlineState), st)
	return fmt.Errorf("catálogo offline recusado (nova tentativa só em %s ou com outro arquivo no servidor): %w", rejectRetry, err)
}

// resumeOffset devolve de onde continuar: o tamanho do .part quando ele é do mesmo
// arquivo (o .sha256 ao lado diz qual); senão, recomeça do zero.
func (o *OfflineCab) resumeOffset(part string, adv protocol.OfflineCatalog) int64 {
	if b, err := os.ReadFile(part + ".sha256"); err == nil && string(b) == adv.SHA256 {
		if fi, err := os.Stat(part); err == nil && fi.Size() <= adv.Size {
			return fi.Size()
		}
	}
	_ = os.WriteFile(part+".sha256", []byte(adv.SHA256), 0o600)
	_ = os.WriteFile(part, nil, 0o600)
	return 0
}

// fetch baixa a partir de offset e devolve até onde chegou. Range com If-Range: se o
// servidor não tiver mais esse arquivo (ETag diferente), ele manda o inteiro (200) e o
// download recomeça; dois arquivos nunca são emendados.
func (o *OfflineCab) fetch(ctx context.Context, part string, offset int64, adv protocol.OfflineCatalog) (int64, error) {
	if offset == adv.Size {
		return offset, nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.Client.BaseURL+protocol.OfflineCatalogPath+adv.SHA256, nil)
	if err != nil {
		return offset, err
	}
	requestID := newRequestID()
	req.Header.Set("Authorization", "Bearer "+o.Client.Credential)
	req.Header.Set("X-Request-ID", requestID)
	req.Header.Set("User-Agent", "patchd-agent/"+o.Client.AgentVersion)
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		req.Header.Set("If-Range", `"`+adv.SHA256+`"`)
	}
	// Sem o prazo total do cliente (2 min): o download leva minutos. Vale o tempo sem bytes.
	hc := &http.Client{Transport: o.Client.HTTP.Transport, CheckRedirect: o.Client.HTTP.CheckRedirect}
	if hc.Transport == nil {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.ResponseHeaderTimeout = offlineHeaderWait
		hc.Transport = t
	}
	resp, err := hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return offset, ctx.Err()
		}
		return offset, &TransientError{Err: err, RequestID: requestID}
	}
	defer resp.Body.Close()

	flags := os.O_WRONLY | os.O_APPEND
	switch resp.StatusCode {
	case http.StatusPartialContent:
		if start := contentRangeStart(resp.Header.Get("Content-Range")); start != offset {
			return offset, fmt.Errorf("Content-Range começa em %d, esperado %d (request_id %s)", start, offset, requestID)
		}
	case http.StatusOK:
		offset, flags = 0, os.O_WRONLY|os.O_TRUNC
	case http.StatusUnauthorized:
		return offset, ErrUnauthorized
	default:
		var e protocol.ErrorResponse
		data, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
		_ = json.Unmarshal(data, &e)
		return offset, &RejectedError{Status: resp.StatusCode, Code: e.Error.Code, Message: e.Error.Message, RequestID: requestID}
	}

	f, err := os.OpenFile(part, flags, 0o600)
	if err != nil {
		return offset, err
	}
	idle := o.idle
	if idle <= 0 {
		idle = offlineIdleTimeout
	}
	body := newStallReader(resp.Body, idle, cancel)
	n, cerr := io.Copy(f, io.LimitReader(body, adv.Size-offset+1))
	body.stop()
	if err := f.Close(); err != nil && cerr == nil {
		cerr = err
	}
	offset += n
	switch {
	case body.stalled():
		cerr = fmt.Errorf("nenhum byte em %s", idle)
	case cerr == nil && offset > adv.Size:
		cerr = fmt.Errorf("o servidor mandou mais que os %d bytes anunciados", adv.Size)
	case cerr == nil && offset < adv.Size:
		cerr = errors.New("a resposta terminou antes do fim do arquivo")
	}
	if cerr != nil && ctx.Err() != nil && !body.stalled() {
		cerr = ctx.Err()
	}
	return offset, cerr
}

// contentRangeStart lê o início de "bytes 100-199/200"; -1 se ilegível.
func contentRangeStart(v string) int64 {
	start, _, ok := strings.Cut(strings.TrimPrefix(v, "bytes "), "-")
	if !ok {
		return -1
	}
	n, err := strconv.ParseInt(start, 10, 64)
	if err != nil {
		return -1
	}
	return n
}

// fileSHA256 calcula o hash em fluxo, sem carregar o arquivo na memória.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func mibs(n int64) string { return fmt.Sprintf("%.1f", float64(n)/(1<<20)) }

// stallReader cancela o pedido quando passa idle sem nenhum byte.
type stallReader struct {
	r     io.Reader
	timer *time.Timer
	idle  time.Duration
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
