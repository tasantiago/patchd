package tpexec

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/platform"
	"github.com/tasantiago/patchd/internal/wintrust"
)

type saida int

func (s saida) Error() string { return fmt.Sprintf("exit status %d", int(s)) }
func (s saida) ExitCode() int { return int(s) }

// runnerFalso grava o comando e devolve o código pedido; confere que o arquivo existe na hora.
type runnerFalso struct {
	prog   string
	args   []string
	code   int
	err    error
	existe bool
}

func (r *runnerFalso) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.prog, r.args = name, args
	file := name
	if strings.HasSuffix(name, "msiexec.exe") {
		file = args[1]
	}
	_, err := os.Stat(file)
	r.existe = err == nil
	if r.err != nil {
		return nil, r.err
	}
	if r.code != 0 {
		return nil, fmt.Errorf("x: %w", saida(r.code))
	}
	return nil, nil
}

func executor(t *testing.T, conteudo []byte, run *runnerFalso, sig wintrust.Signature, sigErr error) (*Executor, *string) {
	t.Helper()
	var pedido string
	dir := t.TempDir()
	return &Executor{
		Download: func(_ context.Context, path string, w io.Writer, limit int64) (int64, error) {
			pedido = path
			n, err := io.Copy(w, io.LimitReader(bytes.NewReader(conteudo), limit+1))
			return n, err
		},
		Run:     run,
		Verify:  func(string) (wintrust.Signature, error) { return sig, sigErr },
		Dir:     filepath.Join(dir, "pacotes"),
		LogDir:  filepath.Join(dir, "logs"),
		Msiexec: `C:\Windows\System32\msiexec.exe`,
	}, &pedido
}

func params(conteudo []byte, inst string, args ...string) jobs.ThirdPartyParams {
	h := sha256.Sum256(conteudo)
	return jobs.ThirdPartyParams{App: "x", Version: "1.0", SHA256: hex.EncodeToString(h[:]), Size: int64(len(conteudo)), Installer: inst, Args: args,
		ReturnCodes: map[string]string{"5": "packageInUse"}}
}

var google = wintrust.Signature{Chain: []wintrust.Cert{{CommonName: "Google LLC", Organization: "Google LLC"}, {CommonName: "Raiz"}}}

func TestInstalarExe(t *testing.T) {
	inst := []byte("MZ instalador nsis")
	run := &runnerFalso{}
	e, pedido := executor(t, inst, run, google, nil)
	p := params(inst, "nullsoft", "/S")
	detail, err := e.Install(context.Background(), 41, p)
	if err != nil || !strings.HasPrefix(detail, "código 0 (ok) em ") || !strings.Contains(detail, "assinatura válida: Google LLC (O=Google LLC)") {
		t.Fatalf("exe: %q %v", detail, err)
	}
	// O próprio arquivo baixado, com os argumentos do job; existia na hora e sumiu depois.
	if run.prog != filepath.Join(e.Dir, p.SHA256+".exe") || strings.Join(run.args, " ") != "/S" || !run.existe {
		t.Errorf("comando: %s %v (existia: %t)", run.prog, run.args, run.existe)
	}
	if _, err := os.Stat(run.prog); !errors.Is(err, os.ErrNotExist) {
		t.Error("o instalador ficou na máquina")
	}
	if *pedido != ContentPath+p.SHA256+".exe" {
		t.Errorf("caminho pedido: %s", *pedido)
	}
}

func TestInstalarMSI(t *testing.T) {
	inst := []byte("MSI do chrome")
	run := &runnerFalso{code: 3010}
	e, pedido := executor(t, inst, run, google, nil)
	p := params(inst, "wix", "/quiet", "/norestart")
	p.Signer = "Google LLC"
	detail, err := e.Install(context.Background(), 42, p)
	if err != nil || !strings.Contains(detail, "código 3010 (rebootRequiredToFinish)") || !strings.HasSuffix(detail, "reinício pendente para concluir") ||
		!strings.Contains(detail, "log do msiexec: "+filepath.Join(e.LogDir, "instalar-job-42.log")) {
		t.Fatalf("msi: %q %v", detail, err)
	}
	want := []string{"/i", filepath.Join(e.Dir, p.SHA256+".msi"), "/quiet", "/norestart", "/l*v", filepath.Join(e.LogDir, "instalar-job-42.log")}
	if run.prog != e.Msiexec || strings.Join(run.args, "|") != strings.Join(want, "|") {
		t.Errorf("comando: %s %v", run.prog, run.args)
	}
	if *pedido != ContentPath+p.SHA256+".msi" {
		t.Errorf("caminho: %s", *pedido)
	}
}

func TestInstalarRecusas(t *testing.T) {
	inst := []byte("instalador")
	ctx := context.Background()
	// Arquivo diferente do job: nada é executado.
	run := &runnerFalso{}
	e, _ := executor(t, []byte("outro conteúdo"), run, google, nil)
	p := params(inst, "nullsoft", "/S")
	p.Size = int64(len("outro conteúdo"))
	if _, err := e.Install(ctx, 1, p); !errors.Is(err, jobs.ErrRefused) || !strings.Contains(err.Error(), "não o do job") || run.prog != "" {
		t.Errorf("hash: %v (executou %q)", err, run.prog)
	}
	// Tamanho diferente.
	e, _ = executor(t, inst, run, google, nil)
	p = params(inst, "nullsoft")
	p.Size++
	if _, err := e.Install(ctx, 1, p); !errors.Is(err, jobs.ErrRefused) || run.prog != "" {
		t.Errorf("tamanho: %v", err)
	}
	// Assinante exigido e outro assinou.
	e, _ = executor(t, inst, run, google, nil)
	p = params(inst, "nullsoft")
	p.Signer = "Mozilla Corporation"
	if _, err := e.Install(ctx, 1, p); !errors.Is(err, jobs.ErrRefused) || !strings.Contains(err.Error(), `não por "Mozilla Corporation"`) || run.prog != "" {
		t.Errorf("assinante: %v", err)
	}
	// Assinante exigido e sem assinatura.
	e, _ = executor(t, inst, run, wintrust.Signature{}, errors.New("o arquivo não tem assinatura"))
	if _, err := e.Install(ctx, 1, p); !errors.Is(err, jobs.ErrRefused) || run.prog != "" {
		t.Errorf("sem assinatura: %v", err)
	}
	// Sem assinante exigido, a falta de assinatura só é informada.
	e, _ = executor(t, inst, run, wintrust.Signature{}, errors.New("o arquivo não tem assinatura"))
	p.Signer = ""
	if d, err := e.Install(ctx, 1, p); err != nil || !strings.Contains(d, "assinatura: o arquivo não tem assinatura") {
		t.Errorf("informada: %q %v", d, err)
	}
}

func TestInstalarCodigos(t *testing.T) {
	inst := []byte("instalador")
	ctx := context.Background()
	// O código 5 do manifesto (Notepad++ aberto): tentar depois.
	e, _ := executor(t, inst, &runnerFalso{code: 5}, google, nil)
	d, err := e.Install(ctx, 1, params(inst, "nullsoft", "/S"))
	if !errors.Is(err, ErrRetry) || !strings.Contains(d, "código 5 (packageInUse)") {
		t.Errorf("5: %q %v", d, err)
	}
	// Código sem significado: falhou.
	e, _ = executor(t, inst, &runnerFalso{code: 2}, google, nil)
	if _, err := e.Install(ctx, 1, params(inst, "nullsoft", "/S")); err == nil || errors.Is(err, ErrRetry) || errors.Is(err, jobs.ErrRefused) {
		t.Errorf("2: %v", err)
	}
	// Download falhou: não rodou (ErrNotStarted), e nada foi executado.
	run := &runnerFalso{}
	e, _ = executor(t, inst, run, google, nil)
	e.Download = func(context.Context, string, io.Writer, int64) (int64, error) {
		return 0, errors.New("404 content_not_found")
	}
	if _, err := e.Install(ctx, 1, params(inst, "nullsoft")); !errors.Is(err, jobs.ErrNotStarted) || !strings.Contains(err.Error(), "404") || run.prog != "" {
		t.Errorf("download: %v", err)
	}
	// Prazo esgotado: falhou, sem código.
	e, _ = executor(t, inst, &runnerFalso{err: errors.New("prazo de 30m0s esgotado")}, google, nil)
	if _, err := e.Install(ctx, 1, params(inst, "inno")); err == nil || !strings.Contains(err.Error(), "o instalador não terminou") {
		t.Errorf("prazo: %v", err)
	}
	// Fora do Windows: a assinatura não é conferida, e o detalhe diz.
	e, _ = executor(t, inst, &runnerFalso{}, wintrust.Signature{}, wintrust.ErrUnsupported)
	if d, _ := e.Install(ctx, 1, params(inst, "inno")); !strings.Contains(d, "assinatura não conferida") {
		t.Errorf("sem WinVerifyTrust: %q", d)
	}
}

// Aula 14.6b: o Inno grava o log do agente, e o programa aberto impede o job antes do download.
func TestInstalarInnoEProgramaAberto(t *testing.T) {
	inst := []byte("MZ instalador inno do vscode")
	ctx := context.Background()
	run := &runnerFalso{code: 5}
	e, _ := executor(t, inst, run, google, nil)
	p := params(inst, "inno", "/VERYSILENT", "/mergetasks=!runcode")
	p.ReturnCodes = nil
	log := filepath.Join(e.LogDir, "instalar-job-33.log")
	d, err := e.Install(ctx, 33, p)
	if !errors.Is(err, ErrRetry) || errors.Is(err, jobs.ErrNotStarted) || !strings.Contains(d, "código 5 (fileInUse)") || !strings.Contains(d, "log do Inno: "+log) {
		t.Errorf("inno 5: %q %v", d, err)
	}
	if strings.Join(run.args, "|") != "/VERYSILENT|/mergetasks=!runcode|/LOG="+log || strings.Join(p.Args, "|") != "/VERYSILENT|/mergetasks=!runcode" {
		t.Errorf("argumentos: %v (job: %v)", run.args, p.Args)
	}
	// Pasta de log com espaço: sem /LOG (o argumento iria sem aspas).
	e.LogDir = `C:\Program Data\patchd\logs`
	if prog, args := e.command(1, "x.exe", p); prog != "x.exe" || len(args) != 2 {
		t.Errorf("com espaço: %v", args)
	}

	// Programa aberto: nem baixa nem executa; o erro diz quantos, quais e em que sessão.
	p.Processes = []string{"Code.exe"}
	run = &runnerFalso{}
	e, pedido := executor(t, inst, run, google, nil)
	e.Processes = func() ([]platform.Process, error) {
		return []platform.Process{{Name: "Code.exe", PID: 1, Session: 1}, {Name: "code.exe", PID: 2, Session: 1}, {Name: "explorer.exe", PID: 3, Session: 1}}, nil
	}
	_, err = e.Install(ctx, 34, p)
	if !errors.Is(err, jobs.ErrNotStarted) || !errors.Is(err, ErrRetry) || *pedido != "" || run.prog != "" ||
		!strings.Contains(err.Error(), "programa aberto (1 Code.exe, 1 code.exe na sessão 1)") {
		t.Errorf("aberto: %v (baixou %q, executou %q)", err, *pedido, run.prog)
	}
	// Lista indisponível: também não roda.
	e.Processes = func() ([]platform.Process, error) { return nil, platform.ErrNoProcessList }
	if _, err := e.Install(ctx, 35, p); !errors.Is(err, jobs.ErrNotStarted) || run.prog != "" {
		t.Errorf("sem lista: %v", err)
	}
	e.Processes = nil
	if _, err := e.Install(ctx, 36, p); !errors.Is(err, jobs.ErrNotStarted) || run.prog != "" {
		t.Errorf("sem função: %v", err)
	}
	// Fechado: segue normalmente.
	e.Processes = func() ([]platform.Process, error) { return []platform.Process{{Name: "explorer.exe", Session: 1}}, nil }
	if d, err := e.Install(ctx, 37, p); err != nil || !strings.HasPrefix(d, "código 0 (ok)") || run.prog == "" {
		t.Errorf("fechado: %q %v", d, err)
	}
}
