package jobs

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/biospw"
	"github.com/tasantiago/patchd/internal/machinebox"
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

func TestCopiaNoJobDaSenha(t *testing.T) {
	priv, _ := machinebox.Generate()
	pub, _ := machinebox.Public(priv)
	envGuarda, _ := biospw.Seal(pub, "60ef97e4-0001", "abcd2345efgh")
	copia := func(guard int64) string {
		s, _ := biospw.Copy{MachineID: "60ef97e4-0001", Guard: guard, KeyID: machinebox.KeyID(pub), Escrow: envGuarda}.Format()
		return s
	}
	env := base64.StdEncoding.EncodeToString([]byte("envelope"))
	p := BIOSPasswordParams{Guard: 3, Vendor: "hp", BoxKey: "0123456789ab", New: env, Copy: copia(3)}
	raw, err := BIOSPasswordParamsJSON(p)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := ParseBIOSPasswordParams(raw); err != nil || got.Copy != p.Copy {
		t.Fatalf("%+v %v", got, err)
	}
	for nome, c := range map[string]string{"outra guarda": copia(4), "fora do formato": "patchd-bios-1:x", "senha em texto": "abcd2345efgh"} {
		q := p
		q.Copy = c
		if _, err := BIOSPasswordParamsJSON(q); err == nil {
			t.Errorf("%s: aceita", nome)
		}
	}
}
