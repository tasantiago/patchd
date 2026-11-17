package jobs

import (
	"strings"
	"testing"
)

// A entrada que a regularização remove: só das chaves Uninstall de máquina, com o método que
// a entrada tem; o msi pede o ProductCode da própria chave.
func TestReplaceEntry(t *testing.T) {
	const pc = "{0CD7CB36-56F0-49FF-8BE2-1250C8660C0D}"
	raiz64 := `HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\`
	raiz32 := `HKLM\SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\`
	boas := []ReplaceEntry{
		{Name: "PowerShell 7.6.6.0-x64", Version: "7.6.6.0", Key: raiz32 + pc, Method: "quiet"},
		{Name: "Programa", Key: raiz64 + "Programa_is1", Method: "inno"},
		{Name: "Programa MSI", Version: "1.0", Key: raiz64 + pc, Method: "msi", ProductCode: strings.ToLower(pc)},
	}
	for _, r := range boas {
		if err := r.Validate(); err != nil {
			t.Errorf("%+v: %v", r, err)
		}
		p := tpValido()
		p.Replace = &r
		if err := p.Validate(); err != nil {
			t.Errorf("job com %+v: %v", r, err)
		}
	}
	if got := boas[0].ReplaceSubkey(); got != pc {
		t.Errorf("subchave: %q", got)
	}
	ruins := map[string]ReplaceEntry{
		"sem nome":             {Key: raiz64 + "X", Method: "quiet"},
		"nome com controle":    {Name: "X\n", Key: raiz64 + "X", Method: "quiet"},
		"chave do usuário":     {Name: "X", Key: `HKCU\Software\Microsoft\Windows\CurrentVersion\Uninstall\X`, Method: "quiet"},
		"chave de outro lugar": {Name: "X", Key: `HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Run\X`, Method: "quiet"},
		"subchave aninhada":    {Name: "X", Key: raiz64 + `X\Y`, Method: "quiet"},
		"sem subchave":         {Name: "X", Key: raiz64, Method: "quiet"},
		"método":               {Name: "X", Key: raiz64 + "X", Method: "cmd"},
		"msi sem código":       {Name: "X", Key: raiz64 + pc, Method: "msi"},
		"msi de outra chave":   {Name: "X", Key: raiz64 + "{11111111-2222-3333-4444-555555555555}", Method: "msi", ProductCode: pc},
		"código fora do msi":   {Name: "X", Key: raiz64 + pc, Method: "quiet", ProductCode: pc},
	}
	for nome, r := range ruins {
		if err := r.Validate(); err == nil {
			t.Errorf("%s: aceitou", nome)
		}
		p := tpValido()
		p.Replace = &r
		if p.Validate() == nil {
			t.Errorf("%s: o job aceitou", nome)
		}
	}
	p := ThirdPartyParams{App: "googlechrome", Version: "1.0", SHA256: strings.Repeat("26", 32), Size: 1,
		Installer: InstallerInstallomator, Args: []string{"googlechromepkg"}, Replace: &boas[0]}
	if p.Validate() == nil {
		t.Error("installomator com replace")
	}
}
