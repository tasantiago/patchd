package jobs

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/biosconfig"
)

func TestParametrosDasConfiguracoesDaBIOS(t *testing.T) {
	env := base64.StdEncoding.EncodeToString([]byte("envelope"))
	wol := biosconfig.Change{Name: "Wake On LAN", Value: "Boot to Hard Drive"}
	ok := BIOSSettingsParams{Vendor: "hp", BoxKey: "0123456789ab", Password: env, Changes: []biosconfig.Change{wol}}
	raw, err := BIOSSettingsParamsJSON(ok)
	if err != nil {
		t.Fatal(err)
	}
	if p, err := ParseBIOSSettingsParams(raw); err != nil || p.Password != env || len(p.Changes) != 1 || p.Changes[0] != wol {
		t.Fatalf("%+v %v", p, err)
	}
	if !KnownType(TypeBIOSSettings) {
		t.Error("bios_settings fora dos tipos conhecidos")
	}
	semSenha := ok
	semSenha.Password = ""
	if _, err := BIOSSettingsParamsJSON(semSenha); err != nil {
		t.Errorf("BIOS sem senha: %v", err)
	}
	muitas := make([]biosconfig.Change, biosconfig.MaxChanges+1)
	for i := range muitas {
		muitas[i] = biosconfig.Change{Name: "Wake On LAN " + strings.Repeat("x", i), Value: "Disable"}
	}
	for nome, muda := range map[string]func(*BIOSSettingsParams){
		"dell (ainda não)": func(p *BIOSSettingsParams) { p.Vendor = "dell" },
		"chave curta":      func(p *BIOSSettingsParams) { p.BoxKey = "0123" },
		"senha em texto":   func(p *BIOSSettingsParams) { p.Password = "senha123!" },
		"nenhuma":          func(p *BIOSSettingsParams) { p.Changes = nil },
		"muitas":           func(p *BIOSSettingsParams) { p.Changes = muitas },
		"repetida":         func(p *BIOSSettingsParams) { p.Changes = []biosconfig.Change{wol, wol} },
		"fora da lista":    func(p *BIOSSettingsParams) { p.Changes = []biosconfig.Change{{Name: "TPM State", Value: "Disable"}} },
		"senha da BIOS":    func(p *BIOSSettingsParams) { p.Changes = []biosconfig.Change{{Name: "Setup Password", Value: "x"}} },
		"legado": func(p *BIOSSettingsParams) {
			p.Changes = []biosconfig.Change{{Name: "Configure Legacy Support and Secure Boot", Value: "Legacy Support Enable and Secure Boot Disable"}}
		},
		"valor vazio": func(p *BIOSSettingsParams) { p.Changes = []biosconfig.Change{{Name: "Fast Boot", Value: ""}} },
		"controle":    func(p *BIOSSettingsParams) { p.Changes = []biosconfig.Change{{Name: "Fast Boot", Value: "Enable\n"}} },
		"valor longo": func(p *BIOSSettingsParams) {
			p.Changes = []biosconfig.Change{{Name: "Fast Boot", Value: strings.Repeat("a", 300)}}
		},
	} {
		p := ok
		muda(&p)
		if _, err := BIOSSettingsParamsJSON(p); err == nil {
			t.Errorf("%s: aceito", nome)
		}
	}
	if _, err := ParseBIOSSettingsParams([]byte(`{"vendor":"hp","box_key":"0123456789ab","changes":[{"name":"Fast Boot","value":"Enable"}],"senha":"x"}`)); err == nil {
		t.Error("campo desconhecido aceito")
	}
}
