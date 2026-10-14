package jobs

import (
	"encoding/json"
	"strings"
	"testing"
)

func tpValido() ThirdPartyParams {
	return ThirdPartyParams{App: "notepad-plus-plus", Version: "8.9.8.1", SHA256: strings.Repeat("26", 32), Size: 6963432,
		Installer: "nullsoft", Args: []string{"/S"}, ReturnCodes: map[string]string{"5": "packageInUse"}}
}

func TestThirdPartyParams(t *testing.T) {
	raw, _ := json.Marshal(tpValido())
	if _, err := ParseThirdPartyParams(raw); err != nil {
		t.Fatalf("válido: %v", err)
	}
	if !KnownType(TypeThirdParty) || checkParams(Job{Type: TypeThirdParty, Params: raw}) != nil {
		t.Error("tipo thirdparty não reconhecido")
	}
	// Os argumentos medidos nos manifestos da lista padrão passam.
	for inst, line := range map[string]string{
		"nullsoft": "/S /PreventRebootRequired=true",
		"inno":     "/SP- /VERYSILENT /SUPPRESSMSGBOXES /NORESTART /mergetasks=!runcode",
		"wix":      "/quiet /norestart",
		"burn":     "/quiet /norestart InstallAllUsers=1 PrependPath=1",
		"msi":      "/quiet /norestart ALLUSERS=1",
		"exe":      "-sfx_nu /sAll /rs /msi",
	} {
		args, err := SplitArgs(inst, line)
		if err != nil || len(args) == 0 {
			t.Errorf("%s %q: %v", inst, line, err)
		}
	}
	casos := map[string]func(*ThirdPartyParams){
		"programa":           func(p *ThirdPartyParams) { p.App = "../x" },
		"versão":             func(p *ThirdPartyParams) { p.Version = "x y" },
		"sha":                func(p *ThirdPartyParams) { p.SHA256 = strings.Repeat("A", 64) },
		"tamanho":            func(p *ThirdPartyParams) { p.Size = 3 << 30 },
		"tipo":               func(p *ThirdPartyParams) { p.Installer = "msix" },
		"aspas":              func(p *ThirdPartyParams) { p.Args = []string{`/D="C:\x"`} },
		"caminho":            func(p *ThirdPartyParams) { p.Args = []string{`/LOG=C:\x.log`} },
		"espaço":             func(p *ThirdPartyParams) { p.Args = []string{"/S /x"} },
		"msiexec /x":         func(p *ThirdPartyParams) { p.Installer, p.Args = "msi", []string{"/x"} },
		"msiexec /a":         func(p *ThirdPartyParams) { p.Installer, p.Args = "wix", []string{"/a"} },
		"TRANSFORMS":         func(p *ThirdPartyParams) { p.Installer, p.Args = "msi", []string{"TRANSFORMS=x.mst"} },
		"patch minúsculo":    func(p *ThirdPartyParams) { p.Installer, p.Args = "msi", []string{"patch=x.msp"} },
		"código":             func(p *ThirdPartyParams) { p.ReturnCodes = map[string]string{"x": "ok"} },
		"resposta":           func(p *ThirdPartyParams) { p.ReturnCodes = map[string]string{"1": "a b"} },
		"assinante":          func(p *ThirdPartyParams) { p.Signer = "Google\nLLC" },
		"argumentos demais":  func(p *ThirdPartyParams) { p.Args = make([]string, 33) },
		"processo com pasta": func(p *ThirdPartyParams) { p.Processes = []string{`C:\x\Code.exe`} },
		"processo relativo":  func(p *ThirdPartyParams) { p.Processes = []string{"../Code.exe"} },
		"processo sem .exe":  func(p *ThirdPartyParams) { p.Processes = []string{"Code"} },
		"processos demais":   func(p *ThirdPartyParams) { p.Processes = strings.Fields(strings.Repeat("a.exe ", 9)) },
	}
	for nome, muda := range casos {
		p := tpValido()
		muda(&p)
		if p.Validate() == nil {
			t.Errorf("%s: devia ser recusado (%+v)", nome, p)
		}
	}
	if _, err := SplitArgs("nullsoft", `/D="C:\Program Files\x"`); err == nil || !strings.Contains(err.Error(), "aspas") {
		t.Errorf("aspas na linha: %v", err)
	}
	// Processos: o nome do executável, com espaço e caixa quaisquer.
	p := tpValido()
	p.Processes = []string{"Code.exe", "Microsoft Teams.EXE", "notepad++.exe"}
	if err := p.Validate(); err != nil {
		t.Errorf("processos: %v", err)
	}
	// Campo desconhecido: recusado (um agente não aceita o que não entende).
	if _, err := ParseThirdPartyParams(json.RawMessage(`{"app":"7zip","command":"cmd /c"}`)); err == nil {
		t.Error("campo desconhecido devia ser recusado")
	}
}
