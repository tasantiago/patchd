// Package tpexec executa, no agente do Windows, o job thirdparty (Aula 14.4): baixa o
// instalador do servidor, confere o tamanho e o SHA-256, confere a assinatura Authenticode,
// executa em silêncio e classifica o código de saída.
//
// A ordem importa: nada é executado antes de o arquivo conferir com o SHA-256 que veio no
// job assinado. O comando é montado aqui, a partir do tipo do instalador: o msiexec do
// sistema, por caminho absoluto, para msi e wix; o próprio arquivo baixado para os outros.
// Os argumentos são os do job, já conferidos pelo pacote jobs dos dois lados.
package tpexec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/platform"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/winget"
	"github.com/tasantiago/patchd/internal/wintrust"
)

// ContentPath é o caminho do download no servidor: ContentPath + FileName.
const ContentPath = protocol.PackagesPath

// FileName: o nome do instalador no servidor e no disco da máquina.
func FileName(p jobs.ThirdPartyParams) string {
	if jobs.MSIInstaller(p.Installer) {
		return p.SHA256 + ".msi"
	}
	return p.SHA256 + ".exe"
}

// ErrRetry: o instalador pediu para tentar depois (outro instalador rodando, programa
// aberto). Vira "falhou", com o motivo; quem decide repetir é o operador ou a campanha.
var ErrRetry = errors.New("tentar depois")

// Executor executa os jobs thirdparty.
type Executor struct {
	// Download baixa um caminho do servidor (a credencial da máquina vai junto).
	Download func(ctx context.Context, path string, w io.Writer, limit int64) (int64, error)
	Run      platform.Runner // com prazo (30 min no agente)
	// Verify confere a assinatura Authenticode (wintrust.VerifyChain). Fora do Windows
	// devolve wintrust.ErrUnsupported.
	Verify  func(path string) (wintrust.Signature, error)
	Dir     string // onde o instalador fica enquanto roda (dentro da pasta de dados do agente)
	LogDir  string // log do msiexec
	Msiexec string // caminho absoluto do msiexec.exe
}

// Install executa o job e devolve o detalhe para o servidor. Erros com jobs.ErrRefused viram
// "rejeitado" (nada foi executado); com ErrRetry ou outros, "falhou".
func (e *Executor) Install(ctx context.Context, jobID int64, p jobs.ThirdPartyParams) (string, error) {
	if err := os.MkdirAll(e.Dir, 0o700); err != nil {
		return "", fmt.Errorf("%w: %v", jobs.ErrNotStarted, err)
	}
	file := filepath.Join(e.Dir, FileName(p))
	defer os.Remove(file) // o instalador não fica na máquina depois do job

	if err := e.fetch(ctx, file, p); err != nil {
		return "", err
	}

	sigNote, err := e.checkSignature(file, p.Signer)
	if err != nil {
		return sigNote, err
	}

	prog, args := e.command(jobID, file, p)
	start := time.Now()
	_, runErr := e.Run.Run(ctx, prog, args...)
	took := time.Since(start).Round(time.Second)
	code := 0
	var exitErr interface{ ExitCode() int } // *exec.ExitError; nos testes, um falso
	switch {
	case runErr == nil:
	case errors.As(runErr, &exitErr):
		code = exitErr.ExitCode()
	default:
		// Prazo esgotado ou o programa nem abriu: o estado da máquina é desconhecido; o
		// servidor confere pelo inventário de qualquer forma.
		return sigNote, fmt.Errorf("o instalador não terminou: %w", runErr)
	}

	inst := winget.Installer{Type: p.Installer, ReturnCodes: map[int]string{}}
	for k, v := range p.ReturnCodes {
		if n, err := strconv.Atoi(k); err == nil {
			inst.ReturnCodes[n] = v
		}
	}
	outcome, resp := inst.Outcome(code)
	detail := fmt.Sprintf("código %d (%s) em %s; %s", code, cmpOr(resp, outcome), took, sigNote)
	if jobs.MSIInstaller(p.Installer) {
		detail += "; log do msiexec: " + e.logPath(jobID)
	}
	switch outcome {
	case winget.OutcomeOK:
		return detail, nil
	case winget.OutcomeReboot:
		return detail + "; reinício pendente para concluir", nil
	case winget.OutcomeRetry:
		return detail, fmt.Errorf("%w: %s", ErrRetry, cmpOr(resp, "código "+strconv.Itoa(code)))
	}
	return detail, fmt.Errorf("o instalador terminou com o código %d", code)
}

// fetch baixa para um temporário, confere o tamanho e o SHA-256 e só então dá o nome final.
func (e *Executor) fetch(ctx context.Context, file string, p jobs.ThirdPartyParams) error {
	tmp, err := os.CreateTemp(e.Dir, ".baixando-*")
	if err != nil {
		return fmt.Errorf("%w: %v", jobs.ErrNotStarted, err)
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	n, err := e.Download(ctx, ContentPath+FileName(p), io.MultiWriter(tmp, h), p.Size)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("%w (download do instalador: %v)", jobs.ErrNotStarted, err)
	}
	if n != p.Size {
		return fmt.Errorf("%w: o servidor entregou %d bytes; o job diz %d", jobs.ErrRefused, n, p.Size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != p.SHA256 {
		return fmt.Errorf("%w: o arquivo recebido tem SHA-256 %s…, não o do job (%s…)", jobs.ErrRefused, got[:12], p.SHA256[:12])
	}
	return os.Rename(tmp.Name(), file)
}

// checkSignature confere a assinatura. Com Signer no job, exige a organização; sem ele,
// só informa (o SHA-256 já conferiu o arquivo; a assinatura vira dado para a Aula 14.5).
func (e *Executor) checkSignature(file, signer string) (string, error) {
	sig, err := e.Verify(file)
	note := ""
	switch {
	case errors.Is(err, wintrust.ErrUnsupported):
		note = "assinatura não conferida (sistema sem WinVerifyTrust)"
	case err != nil:
		note = "assinatura: " + err.Error()
	default:
		s := sig.Signer()
		note = fmt.Sprintf("assinatura válida: %s (O=%s)", s.CommonName, s.Organization)
	}
	if signer == "" {
		return note, nil
	}
	if err != nil {
		return note, fmt.Errorf("%w: o job exige assinatura de %q e a conferência falhou: %v", jobs.ErrRefused, signer, err)
	}
	if err := wintrust.CheckOrganization(sig, signer); err != nil {
		return note, fmt.Errorf("%w: %v", jobs.ErrRefused, err)
	}
	return note, nil
}

// command monta o programa e os argumentos: o msiexec com /i e o log, ou o próprio arquivo.
func (e *Executor) command(jobID int64, file string, p jobs.ThirdPartyParams) (string, []string) {
	if jobs.MSIInstaller(p.Installer) {
		args := []string{"/i", file}
		args = append(args, p.Args...)
		return e.Msiexec, append(args, "/l*v", e.logPath(jobID))
	}
	return file, p.Args
}

func (e *Executor) logPath(jobID int64) string {
	return filepath.Join(e.LogDir, "instalar-job-"+strconv.FormatInt(jobID, 10)+".log")
}

func cmpOr(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
