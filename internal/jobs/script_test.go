package jobs

import (
	"encoding/json"
	"strings"
	"testing"
)

func paramsValidos() ScriptParams {
	return ScriptParams{Bundle: "pacote-exemplo", SHA256: strings.Repeat("ab", 32), Size: 2048,
		Entry: "instalar.ps1", Runner: "powershell", Args: []string{"/quiet", "MODO=1"}, Timeout: 600}
}

func TestScriptParams(t *testing.T) {
	p := paramsValidos()
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if p.OS() != "windows" {
		t.Errorf("SO: %q", p.OS())
	}
	raw, _ := json.Marshal(p)
	lido, err := ParseScriptParams(raw)
	if err != nil || lido.Entry != "instalar.ps1" || len(lido.Args) != 2 {
		t.Fatalf("ida e volta: %+v %v", lido, err)
	}
	// Campo desconhecido é recusado (um agente antigo não inventa semântica).
	if _, err := ParseScriptParams(json.RawMessage(`{"bundle":"x","comando":"rm -rf /"}`)); err == nil {
		t.Error("campo desconhecido devia recusar")
	}
}

func TestScriptParamsRecusa(t *testing.T) {
	casos := map[string]func(*ScriptParams){
		"sem bundle":            func(p *ScriptParams) { p.Bundle = "" },
		"sha curto":             func(p *ScriptParams) { p.SHA256 = "abc" },
		"sha maiúsculo":         func(p *ScriptParams) { p.SHA256 = strings.ToUpper(strings.Repeat("ab", 32)) },
		"tamanho zero":          func(p *ScriptParams) { p.Size = 0 },
		"tamanho absurdo":       func(p *ScriptParams) { p.Size = 1 << 40 },
		"entrada para fora":     func(p *ScriptParams) { p.Entry = "../../Windows/System32/cmd.exe" },
		"entrada absoluta":      func(p *ScriptParams) { p.Entry = "/bin/sh" },
		"entrada com unidade":   func(p *ScriptParams) { p.Entry = "C:/Windows/System32/cmd.exe" },
		"entrada não executa":   func(p *ScriptParams) { p.Entry = "config.ini"; p.Runner = "" },
		"runner trocado":        func(p *ScriptParams) { p.Runner = "bash" }, // entrada é .ps1
		"argumento com espaço":  func(p *ScriptParams) { p.Args = []string{"a b"} },
		"argumento com aspas":   func(p *ScriptParams) { p.Args = []string{`"x"`} },
		"argumento com caminho": func(p *ScriptParams) { p.Args = []string{`C:\x`} },
		"prazo curto":           func(p *ScriptParams) { p.Timeout = 5 },
		"prazo longo":           func(p *ScriptParams) { p.Timeout = 99999 },
	}
	for nome, muda := range casos {
		p := paramsValidos()
		muda(&p)
		if err := p.Validate(); err == nil {
			t.Errorf("%s: devia recusar", nome)
		}
	}
}

func TestScriptRunnerPorExtensao(t *testing.T) {
	casos := map[string]string{"a.ps1": "powershell", "a.bat": "cmd", "a.cmd": "cmd", "a.exe": "exe", "sub/a.sh": "bash"}
	for entry, runner := range casos {
		p := paramsValidos()
		p.Entry, p.Runner = entry, runner
		if err := p.Validate(); err != nil {
			t.Errorf("%s/%s: %v", entry, runner, err)
		}
	}
}

// O job script entra na lista fechada de tipos e os parâmetros são conferidos na assinatura.
func TestScriptNoKnownType(t *testing.T) {
	if !KnownType(TypeScript) {
		t.Fatal("o tipo script devia ser conhecido")
	}
	raw, _ := json.Marshal(paramsValidos())
	if err := checkParams(Job{Type: TypeScript, Params: raw}); err != nil {
		t.Errorf("params válidos: %v", err)
	}
	if err := checkParams(Job{Type: TypeScript, Params: json.RawMessage(`{"bundle":"x"}`)}); err == nil {
		t.Error("params inválidos deviam recusar na assinatura")
	}
}
