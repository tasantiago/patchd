package tpexec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
)

const chaveBundle = `HKLM\SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\{11111111-2222-3333-4444-555555555555}`

// registroFalso: a entrada Uninstall, que some depois de "voltas" consultas pós-remoção.
type registroFalso struct {
	entrada UninstallEntry
	existe  bool
	voltas  int
	rodou   bool
}

func (r *registroFalso) lookup(string) (UninstallEntry, bool, error) {
	if r.rodou {
		if r.voltas <= 0 {
			r.existe = false
		}
		r.voltas--
	}
	return r.entrada, r.existe, nil
}

// cmdRunner grava a linha e marca a remoção como iniciada.
type cmdRunner struct {
	reg   *registroFalso
	linha string
	code  int
}

func (c *cmdRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	c.linha = strings.TrimSpace(name + " " + strings.Join(args, " "))
	c.reg.rodou = true
	if c.code != 0 {
		return nil, fmt.Errorf("x: %w", saida(c.code))
	}
	return nil, nil
}

func desinstalador(t *testing.T, reg *registroFalso, run *cmdRunner) *Uninstaller {
	return &Uninstaller{Run: run, Msiexec: `C:\Windows\System32\msiexec.exe`, Lookup: reg.lookup,
		Split: func(l string) ([]string, error) { return strings.Fields(l), nil },
		Poll:  time.Millisecond, Wait: time.Second,
		Sleep: func(context.Context, time.Duration) error { return nil }}
}

// A remoção: o comando sai do registro da máquina (o silencioso, o do Inno com /VERYSILENT,
// o msiexec /x); o agente espera a entrada sumir; o nome tem de conferir.
func TestRemocaoDaInstalacaoAntiga(t *testing.T) {
	ctx := context.Background()
	exe := filepath.Join(t.TempDir(), "bundle.exe")
	if err := os.WriteFile(exe, []byte("MZ"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := jobs.ReplaceEntry{Name: "PowerShell 7.6.6.0-x64", Version: "7.6.6.0", Key: chaveBundle, Method: "quiet"}

	reg := &registroFalso{entrada: UninstallEntry{DisplayName: "PowerShell 7.6.6.0-x64", QuietUninstall: exe + " /uninstall /quiet"}, existe: true, voltas: 2}
	run := &cmdRunner{reg: reg}
	note, err := desinstalador(t, reg, run).Remove(ctx, r, "")
	if err != nil || run.linha != exe+" /uninstall /quiet" || !strings.Contains(note, "removida") {
		t.Fatalf("silencioso: %q %q %v", run.linha, note, err)
	}
	// Inno: o UninstallString com o silêncio do Inno.
	reg = &registroFalso{entrada: UninstallEntry{DisplayName: "Programa", UninstallLine: exe}, existe: true}
	run = &cmdRunner{reg: reg}
	if _, err := desinstalador(t, reg, run).Remove(ctx, jobs.ReplaceEntry{Name: "Programa", Key: chaveBundle, Method: "inno"}, ""); err != nil ||
		run.linha != exe+" /VERYSILENT /SUPPRESSMSGBOXES /NORESTART" {
		t.Errorf("inno: %q %v", run.linha, err)
	}
	// MSI: o msiexec do sistema com o ProductCode; 1605 (já não estava) passa.
	pc := "{22222222-3333-4444-5555-666666666666}"
	reg = &registroFalso{entrada: UninstallEntry{DisplayName: "Programa MSI"}, existe: true}
	run = &cmdRunner{reg: reg, code: 1605}
	if _, err := desinstalador(t, reg, run).Remove(ctx, jobs.ReplaceEntry{Name: "Programa MSI", Key: `HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\` + pc,
		Method: "msi", ProductCode: pc}, `C:\dados\logs\remover-job-1.log`); err != nil ||
		run.linha != `C:\Windows\System32\msiexec.exe /x `+pc+` /qn /norestart /l*v C:\dados\logs\remover-job-1.log` {
		t.Errorf("msi: %q %v", run.linha, err)
	}
	// A entrada já não existe: segue, sem rodar nada.
	reg = &registroFalso{}
	run = &cmdRunner{reg: reg}
	if note, err := desinstalador(t, reg, run).Remove(ctx, r, ""); err != nil || run.linha != "" || !strings.Contains(note, "já não estava") {
		t.Errorf("sem a entrada: %q %v", note, err)
	}
	// Recusas e falhas: outro programa na chave, desinstalador relativo ou fora do disco,
	// código de erro, e a entrada que não sai.
	for nome, c := range map[string]struct {
		entrada UninstallEntry
		code    int
		voltas  int
		quer    string
	}{
		"outro programa": {UninstallEntry{DisplayName: "Outro", QuietUninstall: exe}, 0, 0, "agora é de"},
		"relativo":       {UninstallEntry{DisplayName: r.Name, QuietUninstall: "bundle.exe /uninstall"}, 0, 0, "caminho absoluto"},
		"fora do disco":  {UninstallEntry{DisplayName: r.Name, QuietUninstall: filepath.Join(t.TempDir(), "x.exe")}, 0, 0, "não está no disco"},
		"sem comando":    {UninstallEntry{DisplayName: r.Name}, 0, 0, "não tem o comando"},
		"código":         {UninstallEntry{DisplayName: r.Name, QuietUninstall: exe}, 1603, 0, "código 1603"},
		"não sai":        {UninstallEntry{DisplayName: r.Name, QuietUninstall: exe}, 0, 1 << 30, "continua registrada"},
	} {
		reg := &registroFalso{entrada: c.entrada, existe: true, voltas: c.voltas}
		u := desinstalador(t, reg, &cmdRunner{reg: reg, code: c.code})
		u.Wait = 20 * time.Millisecond
		u.Sleep = func(context.Context, time.Duration) error { time.Sleep(time.Millisecond); return nil }
		if _, err := u.Remove(ctx, r, ""); err == nil || !strings.Contains(err.Error(), c.quer) {
			t.Errorf("%s: %v", nome, err)
		}
	}
}

// No job: o instalador novo é baixado e conferido antes; a remoção vem depois e, falhando,
// nada é instalado.
func TestJobComRegularizacao(t *testing.T) {
	inst := []byte("MSI do aprovado")
	run := &runnerFalso{}
	e, _ := executor(t, inst, run, google, nil)
	p := params(inst, "wix", "/quiet", "/norestart")
	p.Replace = &jobs.ReplaceEntry{Name: "PowerShell 7.6.6.0-x64", Version: "7.6.6.0", Key: chaveBundle, Method: "quiet"}

	if _, err := e.Install(context.Background(), 7, p); !errors.Is(err, jobs.ErrRefused) {
		t.Fatalf("agente sem a remoção: %v", err)
	}
	var ordem []string
	e.Remove = func(_ context.Context, r jobs.ReplaceEntry, log string) (string, error) {
		_, err := os.Stat(filepath.Join(e.Dir, p.SHA256+".msi"))
		ordem = append(ordem, fmt.Sprintf("remover (instalador baixado: %t, log %s)", err == nil, filepath.Base(log)))
		return "instalação anterior removida", nil
	}
	detail, err := e.Install(context.Background(), 7, p)
	if err != nil || run.rodadas != 1 || len(ordem) != 1 || ordem[0] != "remover (instalador baixado: true, log remover-job-7.log)" ||
		!strings.Contains(detail, "instalação anterior removida") {
		t.Fatalf("regularização: %q %v %v", detail, err, ordem)
	}
	e.Remove = func(context.Context, jobs.ReplaceEntry, string) (string, error) {
		return "", errors.New("código 1603")
	}
	if _, err := e.Install(context.Background(), 8, p); err == nil || !strings.Contains(err.Error(), "não foi instalado") || run.rodadas != 1 {
		t.Errorf("remoção falhou: %v (instalações: %d)", err, run.rodadas)
	}
}
