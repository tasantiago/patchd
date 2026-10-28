package tpinstall

import (
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
)

func maquina(name, ver, pub string, extra ...func(*protocol.Software)) protocol.Software {
	s := protocol.Software{Name: name, Version: ver, Publisher: pub, Scope: "machine"}
	for _, f := range extra {
		f(&s)
	}
	return s
}

func msi(s *protocol.Software)     { s.ProductCode = "{23170F69-40C1-2702-2602-000001000000}" }
func bundle(s *protocol.Software)  { s.BundleUpgradeCode = "{AAAAAAAA-1111-2222-3333-BBBBBBBBBBBB}" }
func oculto(s *protocol.Software)  { s.SystemComponent = true }
func usuario(s *protocol.Software) { s.Scope = "user"; s.User = "S-1-5-21-1" }

func pacote(app, ver, typ, arch string) store.ThirdPartyPackage {
	return store.ThirdPartyPackage{AppID: app, Version: ver, Type: typ, Architecture: arch, SHA256: strings.Repeat("a", 64)}
}

// A máquina do lab da 14.1 (as versões e os tipos medidos), com os pacotes da 14.2.
func TestPlanoDaMaquinaDoLab(t *testing.T) {
	m, err := thirdparty.Default()
	if err != nil {
		t.Fatal(err)
	}
	sw := []protocol.Software{
		maquina("7-Zip 26.02 (x64 edition)", "26.02.00.0", "Igor Pavlov", msi),
		maquina("7-Zip 26.02 (x64)", "26.02", "Igor Pavlov"),
		maquina("Google Chrome", "155.0.8059.40", "Google LLC", msi),
		maquina("LibreOffice 25.2.1.1", "25.2.1.1", "The Document Foundation", msi),
		maquina("Microsoft Visual Studio Code", "1.123.0", "Microsoft Corporation"),
		maquina("Mozilla Firefox (x64 pt-BR)", "156.0", "Mozilla"),
		maquina("Notepad++ (64-bit x64)", "8.9.7", "Notepad++ Team"),
		maquina("PowerShell 7-x64", "7.6.6.0", "Microsoft Corporation", msi, oculto),
		maquina("PowerShell 7.6.6.0-x64", "7.6.6.0", "Microsoft Corporation", bundle),
		maquina("Adobe Acrobat (64-bit)", "25.001.20000", "Adobe"), // regra sem install: não aparece
	}
	pkgs := []store.ThirdPartyPackage{
		pacote("7zip", "26.04", "exe", "x64"),
		pacote("google-chrome", "155.0.8059.40", "wix", "x64"),
		pacote("notepad-plus-plus", "8.9.8.1", "nullsoft", "x64"),
		pacote("notepad-plus-plus", "8.9.8.1", "nullsoft", "x86"), // a mesma versão, arquitetura pior
		pacote("notepad-plus-plus", "8.9.9", "nullsoft", "arm64"), // mais nova, mas não roda
		pacote("mozilla-firefox", "157.0.1", "nullsoft", "x64"),
		pacote("libreoffice", "26.8.1.1", "exe", "x64"), // tipo errado para um MSI
	}
	got := map[string]Decision{}
	for _, d := range Plan(sw, m, pkgs, "x64") {
		got[d.App.ID] = d
	}
	casos := map[string]struct{ estado, motivo string }{
		"notepad-plus-plus": {StateUpdate, "8.9.7 → 8.9.8.1 · nullsoft · x64"},
		"mozilla-firefox":   {StateUpdate, "156.0 → 157.0.1 · nullsoft · x64"},
		"libreoffice": {StateNoPackage, "nenhum instalador msi guardado para x64; use Baixar na página do programa com o tipo MSI, ou " +
			"package fetch -app libreoffice -type msi ou wix (o que o manifesto tiver: package show -app libreoffice)"},
		"vscode": {StateNoPackage, "nenhum instalador exe guardado para x64; use Baixar na página do programa com o tipo EXE, ou " +
			"package fetch -app vscode -type nullsoft ou inno ou exe"},
		"7zip":          {StateDuplicate, "2 instalações visíveis (msi 26.02.00.0, exe 26.02): limpar antes"},
		"powershell-7":  {StateBundle, "bundle (WiX Burn)"},
		"google-chrome": {StateCurrent, "155.0.8059.40 instalado"},
	}
	if len(got) != len(casos) {
		t.Errorf("decisões: %d, esperava %d (%v)", len(got), len(casos), got)
	}
	for app, c := range casos {
		d, ok := got[app]
		if !ok || d.State != c.estado || !strings.Contains(d.Reason, c.motivo) {
			t.Errorf("%s: %s %q; esperava %s %q", app, d.State, d.Reason, c.estado, c.motivo)
		}
	}
	if p := got["notepad-plus-plus"].Package; p == nil || p.Architecture != "x64" {
		t.Errorf("notepad++: pacote %+v", p)
	}
	// O PowerShell conta uma instalação (o bundle); o MSI escondido não entra.
	if len(got["powershell-7"].Entries) != 1 || got["powershell-7"].Entries[0].Kind != KindBundle {
		t.Errorf("powershell: %+v", got["powershell-7"].Entries)
	}
}

func TestPlanoOutrosCasos(t *testing.T) {
	m, _ := thirdparty.Default()
	vs := func(extra ...func(*protocol.Software)) protocol.Software {
		return maquina("Microsoft Visual Studio Code (User)", "1.100.0", "Microsoft Corporation", extra...)
	}
	// Só por usuário: fora.
	ds := Plan([]protocol.Software{vs(usuario)}, m, nil, "x64")
	if len(ds) != 1 || ds[0].State != StateUserOnly || len(ds[0].User) != 1 {
		t.Errorf("usuário: %+v", ds)
	}
	// Usuário e máquina: decide pela de máquina e mostra a de usuário à parte.
	ds = Plan([]protocol.Software{vs(usuario), maquina("Microsoft Visual Studio Code", "1.140.0", "Microsoft Corporation")}, m,
		[]store.ThirdPartyPackage{pacote("vscode", "1.141.0", "inno", "x64")}, "x64")
	if len(ds) != 1 || ds[0].State != StateUpdate || len(ds[0].User) != 1 {
		t.Errorf("máquina e usuário: %+v", ds)
	}
	// Só o x86 guardado serve numa x64; o arm64 não serve.
	ds = Plan([]protocol.Software{maquina("7-Zip 26.02 (x64)", "26.02", "Igor Pavlov")}, m,
		[]store.ThirdPartyPackage{pacote("7zip", "26.04", "exe", "x86")}, "x64")
	if ds[0].State != StateUpdate || ds[0].Package.Architecture != "x86" {
		t.Errorf("x86 numa x64: %+v", ds[0])
	}
	ds = Plan([]protocol.Software{maquina("7-Zip 26.02 (x64)", "26.02", "Igor Pavlov")}, m,
		[]store.ThirdPartyPackage{pacote("7zip", "26.04", "exe", "arm64")}, "x64")
	if ds[0].State != StateNoPackage {
		t.Errorf("arm64 numa x64: %+v", ds[0])
	}
	// Mais novo que o guardado: em dia.
	ds = Plan([]protocol.Software{maquina("Notepad++ (64-bit x64)", "8.9.9", "Notepad++ Team")}, m,
		[]store.ThirdPartyPackage{pacote("notepad-plus-plus", "8.9.8.1", "nullsoft", "x64")}, "x64")
	if ds[0].State != StateCurrent {
		t.Errorf("mais novo: %+v", ds[0])
	}
	// Versão que não se compara.
	ds = Plan([]protocol.Software{maquina("Notepad++ (64-bit x64)", "Auto Updatable", "Notepad++ Team")}, m, nil, "x64")
	if ds[0].State != StateBadVersion {
		t.Errorf("versão ilegível: %+v", ds[0])
	}
	if n := Counts(ds); n[StateBadVersion] != 1 {
		t.Errorf("contagem: %v", n)
	}
}

func TestMachineArch(t *testing.T) {
	for in, want := range map[string]string{"amd64": "x64", "arm64": "arm64", "386": "x86"} {
		if got, ok := MachineArch(in); !ok || got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
	if _, ok := MachineArch("riscv64"); ok {
		t.Error("riscv64 não é arquitetura do Windows")
	}
}
