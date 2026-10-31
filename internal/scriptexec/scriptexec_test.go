package scriptexec

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/bundle"
	"github.com/tasantiago/patchd/internal/jobs"
)

// zipa monta um ZIP e devolve os bytes, o SHA-256 e o tamanho.
func zipa(t *testing.T, arquivos map[string]string) ([]byte, string, int64) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for nome, conteudo := range arquivos {
		w, err := zw.Create(nome)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(conteudo)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	sum := sha256.Sum256(b)
	return b, hex.EncodeToString(sum[:]), int64(len(b))
}

// executor devolve um Executor que "baixa" os bytes dados.
func executor(t *testing.T, zipBytes []byte) *Executor {
	t.Helper()
	return &Executor{
		Dir: t.TempDir(),
		Download: func(_ context.Context, _ string, w io.Writer, _ int64) (int64, error) {
			n, err := w.Write(zipBytes)
			return int64(n), err
		},
	}
}

func params(sha string, size int64, entry, runner string) jobs.ScriptParams {
	return jobs.ScriptParams{Bundle: "exemplo", SHA256: sha, Size: size, Entry: entry, Runner: runner, Timeout: 60}
}

// O caminho feliz: baixa, confere, extrai e roda o ponto de entrada na pasta extraída, com
// os outros arquivos do bundle ao lado.
func TestExecute(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("este teste usa um script .sh")
	}
	z, sha, size := zipa(t, map[string]string{
		"rodar.sh":   "#!/bin/sh\ncat config.ini\necho \"arg=$1\"\nexit 0\n",
		"config.ini": "[geral]\nchave=valor\n",
	})
	e := executor(t, z)
	e.Shell = map[string]string{bundle.RunnerBash: "/bin/sh"}
	p := params(sha, size, "rodar.sh", bundle.RunnerBash)
	p.Args = []string{"abc"}

	detalhe, err := e.Execute(context.Background(), 42, p)
	if err != nil {
		t.Fatalf("%v\n%s", err, detalhe)
	}
	// A saída do script vem no detalhe: ele leu o .ini ao lado e recebeu o argumento.
	for _, quer := range []string{"chave=valor", "arg=abc", "código 0", "2 arquivo(s)", "bundle exemplo", "rodar.sh"} {
		if !strings.Contains(detalhe, quer) {
			t.Errorf("detalhe sem %q:\n%s", quer, detalhe)
		}
	}
	// Nem o ZIP nem a pasta extraída ficam na máquina.
	restos, _ := filepath.Glob(filepath.Join(e.Dir, "*"))
	if len(restos) != 0 {
		t.Errorf("sobrou na máquina: %v", restos)
	}
}

// Código de saída diferente de zero vira falha, mas a saída ainda volta.
func TestExecuteCodigoDeSaida(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("este teste usa um script .sh")
	}
	z, sha, size := zipa(t, map[string]string{"rodar.sh": "#!/bin/sh\necho 'deu ruim' >&2\nexit 3\n"})
	e := executor(t, z)
	e.Shell = map[string]string{bundle.RunnerBash: "/bin/sh"}
	detalhe, err := e.Execute(context.Background(), 1, params(sha, size, "rodar.sh", bundle.RunnerBash))
	if err == nil || !strings.Contains(err.Error(), "código 3") {
		t.Fatalf("erro: %v", err)
	}
	if !strings.Contains(detalhe, "deu ruim") { // a saída de erro entra no detalhe
		t.Errorf("detalhe: %s", detalhe)
	}
}

// SHA-256 diferente do que o job diz: nada é extraído nem executado.
func TestExecuteRecusaHashErrado(t *testing.T) {
	entry, runner := nativeEntry()
	z, _, size := zipa(t, map[string]string{entry: "fixture: nunca deve executar"})
	e := executor(t, z)
	e.Run = func(context.Context, string, string, []string) ([]byte, int, error) {
		t.Fatal("não devia executar nada")
		return nil, 0, nil
	}
	p := params(strings.Repeat("0", 64), size, entry, runner)
	_, err := e.Execute(context.Background(), 1, p)
	if !errors.Is(err, jobs.ErrRefused) || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("erro: %v", err)
	}
}

// Tamanho diferente do que o job diz: recusado antes de extrair.
func TestExecuteRecusaTamanho(t *testing.T) {
	entry, runner := nativeEntry()
	z, sha, size := zipa(t, map[string]string{entry: "fixture: nunca deve executar"})
	e := executor(t, z)
	if _, err := e.Execute(context.Background(), 1, params(sha, size+10, entry, runner)); !errors.Is(err, jobs.ErrRefused) || !strings.Contains(err.Error(), "servidor entregou") {
		t.Fatalf("erro: %v", err)
	}
}

func nativeEntry() (string, string) {
	if runtime.GOOS == "windows" {
		return "rodar.ps1", bundle.RunnerPowerShell
	}
	return "rodar.sh", bundle.RunnerBash
}

// Bundle de outro sistema é recusado, não executado pela metade.
func TestExecuteRecusaOutroSO(t *testing.T) {
	z, sha, size := zipa(t, map[string]string{"rodar.ps1": "Write-Host oi\n"})
	e := executor(t, z)
	outro := bundle.RunnerPowerShell
	if runtime.GOOS == "windows" {
		outro = bundle.RunnerBash
		z, sha, size = zipa(t, map[string]string{"rodar.sh": "#!/bin/sh\n"})
		_ = z
	}
	entry := "rodar.ps1"
	if outro == bundle.RunnerBash {
		entry = "rodar.sh"
	}
	_, err := e.Execute(context.Background(), 1, params(sha, size, entry, outro))
	if !errors.Is(err, jobs.ErrRefused) {
		t.Fatalf("erro: %v", err)
	}
}

// Ponto de entrada que não está no ZIP: recusado depois de extrair, sem executar.
func TestExecuteRecusaEntradaAusente(t *testing.T) {
	z, sha, size := zipa(t, map[string]string{"outro.sh": "#!/bin/sh\n"})
	e := executor(t, z)
	e.Shell = map[string]string{bundle.RunnerBash: "/bin/sh"}
	e.Run = func(context.Context, string, string, []string) ([]byte, int, error) {
		t.Fatal("não devia executar nada")
		return nil, 0, nil
	}
	if runtime.GOOS == "windows" {
		t.Skip("runner bash")
	}
	_, err := e.Execute(context.Background(), 1, params(sha, size, "rodar.sh", bundle.RunnerBash))
	if !errors.Is(err, jobs.ErrRefused) || !strings.Contains(err.Error(), "ponto de entrada") {
		t.Fatalf("erro: %v", err)
	}
}

// Prazo esgotado: o script é interrompido e o job falha dizendo isso — e volta logo, mesmo
// que o script tenha deixado um processo rodando em segundo plano segurando a saída.
func TestExecutePrazo(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("este teste usa um script .sh")
	}
	z, sha, size := zipa(t, map[string]string{"rodar.sh": "#!/bin/sh\nsleep 120 &\nsleep 120\n"})
	e := executor(t, z)
	e.Shell = map[string]string{bundle.RunnerBash: "/bin/sh"}
	p := params(sha, size, "rodar.sh", bundle.RunnerBash)
	p.Timeout = jobs.MinScriptTimeout // o Validate exige 30 s; o contexto abaixo corta antes
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	inicio := time.Now()
	if _, err := e.Execute(ctx, 1, p); err == nil || !strings.Contains(err.Error(), "prazo") {
		t.Fatalf("devia informar timeout: %v", err)
	}
	// O WaitDelay fecha a saída logo depois de matar o processo; sem ele, isto levaria os
	// 120 s do processo em segundo plano.
	if d := time.Since(inicio); d > 30*time.Second {
		t.Errorf("o job demorou %s para desistir", d.Round(time.Second))
	}
}

func TestCommand(t *testing.T) {
	casos := []struct {
		runner, prog, entry string
		quer                []string
	}{
		{bundle.RunnerPowerShell, `C:\ps.exe`, `C:\w\a.ps1`, []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", `C:\w\a.ps1`, "x"}},
		{bundle.RunnerCmd, `C:\cmd.exe`, `C:\w\a.bat`, []string{"/d", "/v:off", "/s", "/c", `""C:\w\a.bat" "x""`}},
		{bundle.RunnerVBScript, `C:\cscript.exe`, `C:\w\a.vbs`, []string{"//NoLogo", "//B", `C:\w\a.vbs`, "x"}},
		{bundle.RunnerBash, "/bin/sh", "/w/a.sh", []string{"/w/a.sh", "x"}},
	}
	for _, c := range casos {
		name, args := command(c.prog, c.runner, c.entry, []string{"x"})
		if name != c.prog || strings.Join(args, "|") != strings.Join(c.quer, "|") {
			t.Errorf("%s: %s %v", c.runner, name, args)
		}
	}
	// O runner exe executa o próprio arquivo, sem interpretador.
	name, args := command("", bundle.RunnerExe, `C:\w\a.exe`, []string{"/S"})
	if name != `C:\w\a.exe` || strings.Join(args, "") != "/S" {
		t.Errorf("exe: %s %v", name, args)
	}
}

func TestClip(t *testing.T) {
	if s, cortou := clip([]byte("linha\n")); s != "linha" || cortou {
		t.Errorf("curta: %q %v", s, cortou)
	}
	grande := strings.Repeat("a", MaxOutput+500)
	s, cortou := clip([]byte(grande))
	if !cortou || len(s) > MaxOutput {
		t.Errorf("grande: %d %v", len(s), cortou)
	}
	// O que sobra é o FIM da saída, onde costuma estar o erro.
	s, _ = clip([]byte(strings.Repeat("x", MaxOutput) + "ERRO FINAL"))
	if !strings.HasSuffix(s, "ERRO FINAL") {
		t.Errorf("devia guardar o fim: ...%q", s[max(0, len(s)-20):])
	}
}

func TestFileName(t *testing.T) {
	p := params(strings.Repeat("a", 64), 1, "x.sh", bundle.RunnerBash)
	if FileName(p) != strings.Repeat("a", 64)+".zip" {
		t.Error(FileName(p))
	}
	if !strings.HasPrefix(ContentPath, "/api/v1/agent/") {
		t.Error(ContentPath)
	}
	_ = os.TempDir
}
