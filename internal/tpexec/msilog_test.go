package tpexec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

// Um trecho de log verboso do msiexec com 1603 (fictício, no formato do Windows Installer).
const logMSI = `=== Verbose logging started: 19/11/2026  17:23:01  Build type: SHIP UNICODE 5.00.10011.00  Calling process: C:\Windows\System32\msiexec.exe ===
MSI (s) (A0:B4) [17:23:01:123]: Product: GlobalProtect -- Configuration completed successfully.
Action start 17:23:02: InstallInitialize.
Action ended 17:23:02: InstallInitialize. Return value 1.
CustomAction CheckPanGPS returned actual error code 1603 (note this may not be 100% accurate if translation happened inside sandbox)
MSI (s) (A0:B4) [17:23:03:001]: Product: GlobalProtect -- Error 1722. There is a problem with this Windows Installer package. A program run as part of the setup did not finish as expected.
Action ended 17:23:03: InstallFinalize. Return value 3.
MSI (s) (A0:B4) [17:23:03:500]: Product: GlobalProtect -- Installation failed.
Error 9999. Depois do fim: não entra.
`

func utf16LE(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := []byte{0xFF, 0xFE}
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	return b
}

func TestMotivoNoLogDoMsiexec(t *testing.T) {
	dir := t.TempDir()
	want := "CustomAction CheckPanGPS returned actual error code 1603 (note this may not be 100% accurate if translation happened inside sandbox) | " +
		"Product: GlobalProtect -- Error 1722. There is a problem with this Windows Installer package. A program run as part of the setup did not finish as expected. | " +
		"Action ended 17:23:03: InstallFinalize. Return value 3."
	for nome, conteudo := range map[string][]byte{
		"utf16": utf16LE(strings.ReplaceAll(logMSI, "\n", "\r\n")),
		"ansi":  []byte(logMSI),
	} {
		p := filepath.Join(dir, nome+".log")
		os.WriteFile(p, conteudo, 0o600)
		if got := msiCause(p); got != want {
			t.Errorf("%s:\n%q\nquer\n%q", nome, got, want)
		}
	}
	// Em português, ANSI (Windows-1252), sem "Return value 3": as linhas de erro.
	pt := []byte("Produto: X -- Erro 1925. Voc\xea n\xe3o tem privil\xe9gios suficientes.\r\nOutra linha\r\n")
	p := filepath.Join(dir, "pt.log")
	os.WriteFile(p, pt, 0o600)
	if got := msiCause(p); got != "Produto: X -- Erro 1925. Você não tem privilégios suficientes." {
		t.Errorf("pt: %q", got)
	}
	// Sem log, ou sem nada reconhecível: vazio.
	if msiCause(filepath.Join(dir, "nao-existe.log")) != "" {
		t.Error("sem log")
	}
	os.WriteFile(p, []byte("tudo certo\n"), 0o600)
	if msiCause(p) != "" {
		t.Error("nada reconhecível")
	}
	// O fim de um log grande em UTF-16, lido a partir de uma posição ímpar.
	big := utf16LE(strings.Repeat("Action ended: X. Return value 1.\r\n", 140000) + logMSI)
	p = filepath.Join(dir, "grande.log")
	os.WriteFile(p, append(big, 0), 0o600) // um byte a mais: o início do pedaço fica ímpar
	if got := msiCause(p); !strings.Contains(got, "Error 1722") {
		t.Errorf("grande: %q", got)
	}
}

func TestFalhaDoMSIComMotivo(t *testing.T) {
	inst := []byte("instalador msi")
	e, _ := executor(t, inst, &runnerFalso{code: 1603}, google, nil)
	os.MkdirAll(e.LogDir, 0o700)
	os.WriteFile(e.logPath(7), []byte(logMSI), 0o600)
	d, err := e.Install(context.Background(), 7, params(inst, "msi", "/qn"))
	if err == nil || !strings.Contains(d, "; no log: CustomAction CheckPanGPS returned actual error code 1603") || !strings.Contains(d, "InstallFinalize. Return value 3.") {
		t.Errorf("1603: %q %v", d, err)
	}
}
