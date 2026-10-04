package agent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/release"
	"github.com/tasantiago/patchd/internal/version"
)

const (
	updateDir        = "update"       // dentro da pasta de dados: protegida, só SYSTEM/root
	attemptFile      = "attempt.json" // última tentativa: evita repetir uma atualização que falha
	updateRetryAfter = 6 * time.Hour
	selfTestTimeout  = 30 * time.Second
	maxSignatureSize = 4 << 10
)

// Updater baixa uma versão nova do agente, confere tudo e entrega o executável ao
// instalador. A ordem importa: a assinatura do manifesto é conferida antes de qualquer
// outra coisa; o executável só é executado depois de o SHA-256 dele bater com o manifesto
// assinado; e ele fica na pasta de dados, onde um usuário comum não consegue trocá-lo
// entre a conferência e a execução.
type Updater struct {
	Client    *Client
	DataDir   string
	Current   string            // versão deste executável
	PublicKey ed25519.PublicKey // a chave embutida no build
	// Install inicia a instalação da versão nova fora deste processo: ela vai parar este
	// serviço, trocar o executável e iniciar o novo.
	Install func(bin string) error
	Logger  *slog.Logger

	// Injetáveis nos testes.
	now          func() time.Time
	selfTest     func(ctx context.Context, bin string) (string, error)
	goos, goarch string
}

type updateAttempt struct {
	Version string    `json:"version"`
	At      time.Time `json:"at"`
	Error   string    `json:"error,omitempty"`
}

func (u *Updater) defaults() {
	if u.now == nil {
		u.now = time.Now
	}
	if u.selfTest == nil {
		u.selfTest = runVersion
	}
	if u.goos == "" {
		u.goos, u.goarch = runtime.GOOS, runtime.GOARCH
	}
}

// Offer trata a versão oferecida pelo servidor no check-in.
func (u *Updater) Offer(ctx context.Context, offered string) {
	u.defaults()
	c, err := version.CompareSemver(offered, u.Current)
	if err != nil {
		u.Logger.Warn("atualização oferecida ignorada: versão fora do formato", "offered", offered, "current", u.Current, "error", err)
		return
	}
	if c <= 0 {
		// Uma versão igual ou mais velha nunca é instalada: um manifesto antigo, assinado e
		// com uma falha conhecida, não pode ser reaproveitado para "voltar" a frota. Voltar
		// atrás é publicar uma versão nova com o código antigo.
		u.Logger.Warn("atualização oferecida recusada: não é mais nova que a instalada", "offered", offered, "current", u.Current)
		return
	}

	path := filepath.Join(u.DataDir, updateDir, attemptFile)
	var last updateAttempt
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &last)
	}
	if last.Version == offered && u.now().Sub(last.At) < updateRetryAfter {
		u.Logger.Debug("atualização já tentada há pouco; aguardando", "offered", offered, "last_attempt", last.At, "last_error", last.Error)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		u.Logger.Error("atualização: pasta de download", "error", err)
		return
	}
	// A tentativa é registrada antes: se o processo cair no meio, não tenta de novo a cada ciclo.
	attempt := updateAttempt{Version: offered, At: u.now().UTC()}
	_ = writeJSONAtomic(path, attempt)

	u.Logger.Info("atualização do agente iniciada", "from", u.Current, "to", offered)
	bin, err := u.fetch(ctx, offered)
	if err == nil {
		u.Logger.Info("versão nova baixada e conferida; iniciando o instalador", "target", offered, "file", bin)
		err = u.Install(bin)
	}
	if err != nil {
		attempt.Error = err.Error()
		_ = writeJSONAtomic(path, attempt)
		u.Logger.Error("atualização do agente falhou; nova tentativa em "+updateRetryAfter.String(), "target", offered, "error", err)
	}
}

// fetch baixa e confere o manifesto e o executável; devolve o caminho do executável.
func (u *Updater) fetch(ctx context.Context, v string) (string, error) {
	base := "/api/v1/agent/releases/" + v + "/"
	var mbuf, sbuf bytes.Buffer
	if _, err := u.Client.Download(ctx, base+release.ManifestFile, &mbuf, release.MaxManifestSize); err != nil {
		return "", fmt.Errorf("baixar o manifesto: %w", err)
	}
	if _, err := u.Client.Download(ctx, base+release.SignatureFile, &sbuf, maxSignatureSize); err != nil {
		return "", fmt.Errorf("baixar a assinatura: %w", err)
	}
	m, err := release.Verify(u.PublicKey, mbuf.Bytes(), sbuf.Bytes())
	if err != nil {
		return "", err
	}
	// O manifesto assinado precisa ser o da versão pedida: senão, um servidor poderia
	// entregar o manifesto (legítimo) de outra versão.
	if m.Version != v {
		return "", fmt.Errorf("o manifesto assinado é da versão %s, não da %s", m.Version, v)
	}
	f, ok := m.For(u.goos, u.goarch)
	if !ok {
		return "", fmt.Errorf("a versão %s não tem executável para %s/%s", v, u.goos, u.goarch)
	}

	final := filepath.Join(u.DataDir, updateDir, v+"-"+f.Name)
	part := final + ".part"
	out, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o700)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	n, err := u.Client.Download(ctx, base+f.Name, io.MultiWriter(out, h), f.Size)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && (n != f.Size || hex.EncodeToString(h.Sum(nil)) != f.SHA256) {
		err = fmt.Errorf("%s não confere com o manifesto assinado (tamanho ou SHA-256)", f.Name)
	}
	if err != nil {
		os.Remove(part)
		return "", err
	}
	if err := os.Rename(part, final); err != nil {
		os.Remove(part)
		return "", err
	}

	// Por último, o executável precisa rodar nesta máquina e dizer a versão certa.
	got, err := u.selfTest(ctx, final)
	if err != nil || !strings.HasPrefix(got, "patchd-agent "+v+" ") {
		os.Remove(final)
		return "", fmt.Errorf("o executável novo não se identificou como %s (%q, %v)", v, strings.TrimSpace(got), err)
	}
	return final, nil
}

// CleanUpdates apaga executáveis de atualizações anteriores (o registro da última
// tentativa fica). Um arquivo ainda em uso (o instalador terminando) fica para a próxima.
func (u *Updater) CleanUpdates() {
	entries, err := os.ReadDir(filepath.Join(u.DataDir, updateDir))
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	for _, e := range entries {
		if e.Name() != attemptFile {
			_ = os.Remove(filepath.Join(u.DataDir, updateDir, e.Name()))
		}
	}
}

// runVersion executa "<bin> -version" com prazo.
func runVersion(ctx context.Context, bin string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, selfTestTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "-version").Output()
	return string(out), err
}
