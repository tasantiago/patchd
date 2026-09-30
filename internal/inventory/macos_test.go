package inventory

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Saída real do Mac Studio do laboratório (Aula 2.4), com serial e UUIDs mascarados.
const spHardwareReal = `{
  "SPHardwareDataType" : [
    {
      "_name" : "hardware_overview",
      "activation_lock_status" : "activation_lock_enabled",
      "boot_rom_version" : "18000.121.3",
      "chip_type" : "Apple M2 Ultra",
      "machine_model" : "Mac14,14",
      "machine_name" : "Mac Studio",
      "model_number" : "Z180000AEBZ/A",
      "number_processors" : "proc 24:16:8:0",
      "os_loader_version" : "18000.121.3",
      "physical_memory" : "64 GB",
      "platform_UUID" : "00000000-1111-2222-3333-444444444444",
      "provisioning_UDID" : "00000000-0000000000000000",
      "serial_number" : "XXXXXXXXXX"
    }
  ]
}`

// Saída real do sysctl no mesmo Mac.
const sysctlReal = "68719476736\n24\nApple M2 Ultra\n"

const (
	comandoSPHardware = "/usr/sbin/system_profiler SPHardwareDataType -json"
	comandoSysctl     = "/usr/sbin/sysctl -n hw.memsize hw.ncpu machdep.cpu.brand_string"
	comandoSPApps     = "/usr/sbin/system_profiler SPApplicationsDataType -json"
)

func TestDarwinHardwareReal(t *testing.T) {
	run := runnerFalso{
		comandoSPHardware: {saida: spHardwareReal},
		comandoSysctl:     {saida: sysctlReal},
	}
	hw, err := collectDarwinHardware(context.Background(), run)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if hw.Manufacturer != "Apple" || hw.Model != "Mac14,14" || hw.BIOSVersion != "18000.121.3" {
		t.Errorf("identificação errada: %+v", hw)
	}
	if hw.SerialNumber != "XXXXXXXXXX" || hw.UUID != "00000000-1111-2222-3333-444444444444" {
		t.Errorf("serial ou UUID errados: %+v", hw)
	}
	if hw.MemoryBytes != 68719476736 || hw.CPUThreads != 24 || hw.CPUModel != "Apple M2 Ultra" {
		t.Errorf("memória ou CPU errados (devem vir do sysctl, não do texto \"64 GB\"): %+v", hw)
	}
	if len(hw.Sources) != 2 {
		t.Errorf("esperadas 2 fontes: %v", hw.Sources)
	}
}

func TestDarwinHardwareParcial(t *testing.T) {
	run := runnerFalso{
		comandoSPHardware: {err: errors.New("system_profiler: tempo esgotado")},
		comandoSysctl:     {saida: sysctlReal},
	}
	hw, err := collectDarwinHardware(context.Background(), run)
	if err == nil || !strings.Contains(err.Error(), "system_profiler") {
		t.Errorf("esperado erro do system_profiler: %v", err)
	}
	if hw.MemoryBytes != 68719476736 || hw.Model != "" {
		t.Errorf("o sysctl deveria ter sido lido mesmo assim: %+v", hw)
	}
}

func TestParseSysctlHardwareInvalido(t *testing.T) {
	if _, _, _, err := parseSysctlHardware([]byte("68719476736\n24\n")); err == nil {
		t.Error("esperado erro com linhas faltando")
	}
	if _, _, _, err := parseSysctlHardware([]byte("muito\n24\nApple M2 Ultra\n")); err == nil {
		t.Error("esperado erro com memória não numérica")
	}
}

// Dois apps reais do laboratório (App Store e Automator) e casos sintéticos.
const spAppsAmostra = `{
  "SPApplicationsDataType" : [
    {
      "_name" : "App Store",
      "arch_kind" : "arch_arm_i64",
      "lastModified" : "2026-06-25T02:29:03Z",
      "obtained_from" : "apple",
      "path" : "/System/Applications/App Store.app",
      "signed_by" : ["Software Signing", "Apple Code Signing Certification Authority", "Apple Root CA"],
      "version" : "3.0"
    },
    {
      "_name" : "Automator",
      "arch_kind" : "arch_arm_i64",
      "lastModified" : "2026-06-25T02:29:03Z",
      "obtained_from" : "apple",
      "path" : "/System/Applications/Automator.app",
      "signed_by" : ["Software Signing", "Apple Code Signing Certification Authority", "Apple Root CA"],
      "version" : "2.10"
    },
    {
      "_name" : "Navegador Exemplo",
      "arch_kind" : "arch_arm",
      "obtained_from" : "identified_developer",
      "path" : "/Applications/Navegador Exemplo.app",
      "signed_by" : ["Developer ID Application: Empresa Exemplo LLC (ABCDE12345)", "Developer ID Certification Authority", "Apple Root CA"],
      "version" : "140.0.1"
    },
    {
      "_name" : "Ferramenta Pessoal",
      "arch_kind" : "arch_i64",
      "obtained_from" : "unknown",
      "path" : "/Users/sid/Applications/Ferramenta Pessoal.app"
    }
  ]
}`

func TestParseSPApplications(t *testing.T) {
	list, err := parseSPApplications([]byte(spAppsAmostra))
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if len(list) != 4 {
		t.Fatalf("esperados 4 apps, vieram %d", len(list))
	}
	porNome := map[string]Software{}
	for _, s := range list {
		porNome[s.Name] = s
	}

	a := porNome["App Store"]
	if a.Version != "3.0" || a.Publisher != "Apple" || !a.SystemComponent || a.Arch != "universal" || a.Scope != "machine" {
		t.Errorf("App Store errado: %+v", a)
	}
	if a.Source != "/System/Applications/App Store.app" {
		t.Errorf("a fonte deveria ser o caminho do app: %q", a.Source)
	}

	n := porNome["Navegador Exemplo"]
	if n.Publisher != "Empresa Exemplo LLC" || n.SystemComponent || n.Arch != "arm64" {
		t.Errorf("app de terceiro errado (fornecedor sem Team ID, fora de /System): %+v", n)
	}

	f := porNome["Ferramenta Pessoal"]
	if f.Scope != "user" || f.User != "sid" || f.Version != "" || f.Publisher != "" || f.Arch != "x86_64" {
		t.Errorf("app do usuário, sem versão e sem assinatura, errado: %+v", f)
	}
}

func TestPublisherFromSigningAppStore(t *testing.T) {
	got := publisherFromSigning("mac_app_store", []string{"Apple Mac OS Application Signing", "Apple Worldwide Developer Relations Certification Authority", "Apple Root CA"})
	if got != "" {
		t.Errorf("app da Mac App Store não expõe o desenvolvedor na assinatura: %q", got)
	}
}

func TestUserFromPath(t *testing.T) {
	casos := []struct {
		caminho string
		usuario string
		ok      bool
	}{
		{"/Users/sid/Applications/App.app", "sid", true},
		{"/Users/Shared/App.app", "", false},
		{"/Applications/App.app", "", false},
		{"/Users/", "", false},
	}
	for _, c := range casos {
		u, ok := userFromPath(c.caminho)
		if u != c.usuario || ok != c.ok {
			t.Errorf("userFromPath(%q) = (%q, %t), esperado (%q, %t)", c.caminho, u, ok, c.usuario, c.ok)
		}
	}
}

func TestCollectDarwinAppsComando(t *testing.T) {
	run := runnerFalso{comandoSPApps: {saida: spAppsAmostra}}
	list, err := collectDarwinApps(context.Background(), run)
	if err != nil || len(list) != 4 {
		t.Errorf("esperados 4 apps sem erro: %d, %v", len(list), err)
	}
}
