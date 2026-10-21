package jobs

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestParametrosDaSenhaDaBIOS(t *testing.T) {
	env := base64.StdEncoding.EncodeToString([]byte("envelope"))
	ok := BIOSPasswordParams{Guard: 3, Vendor: "hp", BoxKey: "0123456789ab", New: env, Current: env}
	raw, err := BIOSPasswordParamsJSON(ok)
	if err != nil {
		t.Fatal(err)
	}
	if p, err := ParseBIOSPasswordParams(raw); err != nil || p != ok {
		t.Fatalf("%+v %v", p, err)
	}
	semAtual := ok
	semAtual.Current = ""
	if _, err := BIOSPasswordParamsJSON(semAtual); err != nil {
		t.Errorf("sem a atual (BIOS sem senha): %v", err)
	}
	for nome, muda := range map[string]func(*BIOSPasswordParams){
		"sem guarda":       func(p *BIOSPasswordParams) { p.Guard = 0 },
		"dell (ainda não)": func(p *BIOSPasswordParams) { p.Vendor = "dell" },
		"chave curta":      func(p *BIOSPasswordParams) { p.BoxKey = "0123" },
		"sem a nova":       func(p *BIOSPasswordParams) { p.New = "" },
		"nova sem base64":  func(p *BIOSPasswordParams) { p.New = "senha123!" },
		"atual sem base64": func(p *BIOSPasswordParams) { p.Current = "!!" },
		"envelope enorme":  func(p *BIOSPasswordParams) { p.New = strings.Repeat("A", maxSealed+4) },
	} {
		p := ok
		muda(&p)
		if _, err := BIOSPasswordParamsJSON(p); err == nil {
			t.Errorf("%s: aceito", nome)
		}
	}
	// Campo desconhecido (uma senha em texto, por exemplo) é recusado.
	if _, err := ParseBIOSPasswordParams([]byte(`{"guard":1,"vendor":"hp","box_key":"0123456789ab","new":"` + env + `","senha":"x"}`)); err == nil {
		t.Error("campo desconhecido aceito")
	}
	if !KnownType(TypeBIOSPassword) {
		t.Error("tipo desconhecido")
	}
}
