package winget

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

func manifesto(t *testing.T, id string) Manifest {
	t.Helper()
	data, err := os.ReadFile("testdata/" + id + ".installer.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m, err := ParseInstaller(data)
	if err != nil {
		t.Fatalf("%s: %v", id, err)
	}
	return m
}

// Os manifestos reais: o instalador escolhido para uma máquina x64, a linha de comando e o
// motivo de cada descarte.
func TestEscolhaNosManifestosReais(t *testing.T) {
	casos := []struct {
		id, tipo           string // tipo exigido pela regra
		escolhido, comando string // "#n tipo arq escopo"
		descartes          []string
	}{
		{"7zip.7zip", "exe", "#2 exe x64 machine", `"<f>" /S`,
			[]string{"#1 serve, mas outro vem antes", "#3 arquitetura arm não roda em x64", "#5 tipo wix; a regra exige exe"}},
		// O mesmo 7-Zip, se a regra exigir o MSI: o x64 do WiX.
		{"7zip.7zip", "wix", "#6 wix x64 machine", `msiexec.exe /i "<f>" /quiet /norestart`, []string{"#2 tipo exe; a regra exige wix"}},
		// Tipo e escopo na raiz, herdados por todos.
		{"Google.Chrome", "", "#2 wix x64 machine", `msiexec.exe /i "<f>" /quiet /norestart`, nil},
		// O Silent da raiz substitui o padrão do nullsoft.
		{"Mozilla.Firefox.pt-BR", "", "#2 nullsoft x64 machine", `"<f>" /S /PreventRebootRequired=true`, nil},
		// O de usuário fica de fora; o Custom da raiz vai junto do padrão do Inno.
		{"Microsoft.VisualStudioCode", "", "#2 inno x64 machine", `"<f>" /SP- /VERYSILENT /SUPPRESSMSGBOXES /NORESTART /mergetasks=!runcode`,
			[]string{"#1 escopo user; o agente instala para a máquina"}},
		// O Custom é do instalador de máquina (InstallAllUsers=1), não o do de usuário.
		{"Python.Python.3.14", "", "#4 burn x64 machine", `"<f>" /quiet /norestart InstallAllUsers=1 PrependPath=1`,
			[]string{"#3 escopo user", "#8 tipo zip não é executado"}},
		{"Notepad++.Notepad++", "nullsoft", "#1 nullsoft x64 machine", `"<f>" /S`, []string{"#4 tipo wix; a regra exige nullsoft"}},
		{"Adobe.Acrobat.Reader.64-bit", "", "#1 exe x64 machine", `"<f>" -sfx_nu /sAll /rs /msi`, nil},
		{"Microsoft.PowerShell", "wix", "#4 wix x64 machine", `msiexec.exe /i "<f>" /quiet /norestart`,
			[]string{"#1 tipo msix; a regra exige wix"}},
	}
	for _, c := range casos {
		m := manifesto(t, c.id)
		in, skipped, err := m.Select(Want{Arch: "x64", Type: c.tipo})
		if err != nil {
			t.Errorf("%s: %v", c.id, err)
			continue
		}
		if got := "#" + strconv.Itoa(in.Index+1) + " " + in.Type + " " + in.Architecture + " " + in.Scope; got != c.escolhido {
			t.Errorf("%s (%s): escolheu %s; esperava %s", c.id, c.tipo, got, c.escolhido)
		}
		if got := in.CommandLine("<f>"); got != c.comando {
			t.Errorf("%s: comando %s", c.id, got)
		}
		if len(in.SHA256) != 64 || !strings.HasPrefix(in.URL, "https://") {
			t.Errorf("%s: %s %s", c.id, in.URL, in.SHA256)
		}
		todos := ""
		for _, s := range skipped {
			todos += "#" + strconv.Itoa(s.Installer.Index+1) + " " + s.Reason + "\n"
		}
		for _, d := range c.descartes {
			if !strings.Contains(todos, d) {
				t.Errorf("%s: faltou o descarte %q em\n%s", c.id, d, todos)
			}
		}
	}
}

func TestEscolhaPorArquitetura(t *testing.T) {
	m := manifesto(t, "Google.Chrome")
	for arch, want := range map[string]string{"x64": "x64", "x86": "x86", "arm64": "arm64"} {
		in, _, err := m.Select(Want{Arch: arch})
		if err != nil || in.Architecture != want {
			t.Errorf("%s: %s %v", arch, in.Architecture, err)
		}
	}
	// Numa x86, o instalador de 64 bits não serve.
	if _, _, err := manifesto(t, "Adobe.Acrobat.Reader.64-bit").Select(Want{Arch: "x86"}); err == nil ||
		!strings.Contains(err.Error(), "nenhum instalador serve (x86, escopo machine)") {
		t.Errorf("acrobat 64 bits numa x86: %v", err)
	}
	if _, _, err := m.Select(Want{Arch: "amd64"}); err == nil {
		t.Error("arquitetura com o nome do Go devia ser recusada")
	}
}

func TestCodigosDeSaida(t *testing.T) {
	acrobat := manifesto(t, "Adobe.Acrobat.Reader.64-bit").Installers[0]
	npp, _, _ := manifesto(t, "Notepad++.Notepad++").Select(Want{Arch: "x64", Type: "nullsoft"})
	chrome := manifesto(t, "Google.Chrome").Installers[1]
	firefox := manifesto(t, "Mozilla.Firefox.pt-BR").Installers[1]
	for _, c := range []struct {
		nome   string
		in     Installer
		code   int
		result string
	}{
		{"acrobat ok", acrobat, 0, OutcomeOK},
		{"acrobat 3010 (da raiz)", acrobat, 3010, OutcomeReboot},
		{"acrobat 1618", acrobat, 1618, OutcomeRetry},
		{"acrobat 1638", acrobat, 1638, OutcomeFailed},
		{"notepad++ aberto (do instalador)", npp, 5, OutcomeRetry},
		{"chrome: 3010 do Windows Installer, sem declarar", chrome, 3010, OutcomeReboot},
		{"chrome 1603", chrome, 1603, OutcomeFailed},
		{"firefox: 3010 não é do NSIS", firefox, 3010, OutcomeFailed},
	} {
		if got, _ := c.in.Outcome(c.code); got != c.result {
			t.Errorf("%s: %s; esperava %s", c.nome, got, c.result)
		}
	}
}

func TestManifestoRecusado(t *testing.T) {
	data, _ := os.ReadFile("testdata/Python.Python.3.8.installer.yaml")
	if _, err := ParseInstaller(data); err == nil || !strings.Contains(err.Error(), "âncoras") {
		t.Errorf("âncora: %v", err)
	}
	for nome, c := range map[string]struct{ in, msg string }{
		"outro tipo":   {"PackageIdentifier: a.b\nPackageVersion: 1\nManifestType: version\n", `tipo "version"`},
		"sem versão":   {"PackageIdentifier: a.b\nManifestType: installer\n", "sem PackageIdentifier ou PackageVersion"},
		"sem lista":    {"PackageIdentifier: a.b\nPackageVersion: 1\nManifestType: installer\n", "sem Installers"},
		"sha inválido": {"PackageIdentifier: a.b\nPackageVersion: 1\nManifestType: installer\nInstallers:\n- Architecture: x64\n  InstallerUrl: https://x\n  InstallerSha256: abc\n", "64 hex"},
		"código inválido": {"PackageIdentifier: a.b\nPackageVersion: 1\nManifestType: installer\nExpectedReturnCodes:\n- InstallerReturnCode: x\nInstallers:\n- Architecture: x64\n  InstallerUrl: https://x\n  InstallerSha256: " +
			strings.Repeat("a", 64) + "\n", `código "x"`},
	} {
		if _, err := ParseInstaller([]byte(c.in)); err == nil || !strings.Contains(err.Error(), c.msg) {
			t.Errorf("%s: %v", nome, err)
		}
	}
}

// Descartes que os manifestos reais escolhidos não exercitam.
func TestDescartes(t *testing.T) {
	base := "PackageIdentifier: a.b\nPackageVersion: 1\nManifestType: installer\nInstallers:\n"
	inst := func(extra string) string {
		return "- Architecture: x64\n  InstallerUrl: https://x.example/a\n  InstallerSha256: " + strings.Repeat("a", 64) + "\n" + extra
	}
	for nome, c := range map[string]struct{ in, motivo string }{
		"exe sem Silent":    {inst("  InstallerType: exe\n"), "exe sem o Silent"},
		"http":              {strings.Replace(inst("  InstallerType: msi\n"), "https", "http", 1), "sem https"},
		"uninstallPrevious": {inst("  InstallerType: msi\n  UpgradeBehavior: uninstallPrevious\n"), "exige desinstalar antes"},
		"deny":              {inst("  InstallerType: msi\n  UpgradeBehavior: deny\n"), "não quer atualização por fora"},
		"portable":          {inst("  InstallerType: portable\n"), "portable não é executado"},
		"plataforma":        {inst("  InstallerType: msi\n  Platform:\n  - Windows.Universal\n"), "plataforma Windows.Universal"},
	} {
		m, err := ParseInstaller([]byte(base + c.in))
		if err != nil {
			t.Fatalf("%s: %v", nome, err)
		}
		_, sk, err := m.Select(Want{Arch: "x64"})
		if err == nil || len(sk) != 1 || !strings.Contains(sk[0].Reason, c.motivo) {
			t.Errorf("%s: %v %+v", nome, err, sk)
		}
	}
	// Escopo declarado igual ao pedido vence o não declarado, mesmo vindo depois.
	m, _ := ParseInstaller([]byte(base + inst("  InstallerType: nullsoft\n") + inst("  InstallerType: msi\n  Scope: machine\n")))
	if in, _, _ := m.Select(Want{Arch: "x64"}); in.Type != "msi" {
		t.Errorf("escopo declarado: %s", in.Type)
	}
}
