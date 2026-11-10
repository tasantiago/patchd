package jobs

import (
	"encoding/json"
	"strings"
	"testing"
)

// As perguntas ao parque: só o catálogo, com argumentos conferidos (nada de UNC, curinga,
// "..", SAM/SECURITY); a resposta volta encurtada.
func TestPerguntasAoParque(t *testing.T) {
	boas := []QueryParams{
		{Sensor: SensorFile, Arg: `C:\Windows\System32\drivers\x.sys`},
		{Sensor: SensorFile, Arg: `%ProgramFiles%\LibreOffice\program\soffice.exe`},
		{Sensor: SensorFile, Arg: "/usr/lib/x86_64-linux-gnu/libssl.so.3"},
		{Sensor: SensorProcess, Arg: "firefox.exe"},
		{Sensor: SensorService, Arg: "Spooler"},
		{Sensor: SensorRegistry, Arg: `HKLM\SOFTWARE\Policies\Microsoft\Windows\WindowsUpdate`, Name: "WUServer"},
		{Sensor: SensorRegistry, Arg: `HKEY_LOCAL_MACHINE\SYSTEM\CurrentControlSet\Control\Lsa`},
		{Sensor: SensorProgram, Arg: "LibreOffice"},
	}
	for _, p := range boas {
		if err := p.Validate(); err != nil {
			t.Errorf("%+v: %v", p, err)
		}
		raw, _ := json.Marshal(p)
		if _, err := ParseQueryParams(raw); err != nil {
			t.Errorf("parse %s: %v", raw, err)
		}
		if err := checkParams(Job{Type: TypeQuery, Params: raw}); err != nil {
			t.Errorf("checkParams: %v", err)
		}
	}
	ruins := []QueryParams{
		{Sensor: "comando", Arg: "whoami"},
		{Sensor: SensorFile, Arg: `\\servidor\c$\x`},
		{Sensor: SensorFile, Arg: `relativo\x.dll`},
		{Sensor: SensorFile, Arg: `C:\Windows\*.dll`},
		{Sensor: SensorFile, Arg: `C:\a\..\b`},
		{Sensor: SensorFile, Arg: " C:\\x"},
		{Sensor: SensorProcess, Arg: "a;b"},
		{Sensor: SensorProcess, Arg: "-x"},
		{Sensor: SensorService, Arg: "--help"},
		{Sensor: SensorRegistry, Arg: `HKCU\Software\x`},
		{Sensor: SensorRegistry, Arg: `HKLM\SAM\SAM\Domains`},
		{Sensor: SensorRegistry, Arg: `hklm\security`},
		{Sensor: SensorRegistry, Arg: `HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\BIOS_password`, Name: "DisplayVersion"},
		{Sensor: SensorRegistry, Arg: `HKLM\SOFTWARE\Exemplo\bios_password\Historico\x`},
		{Sensor: SensorRegistry, Arg: `HKLM\SOFTWARE\`},
		{Sensor: SensorProgram, Arg: "x"},
		{Sensor: SensorProcess, Arg: "x.exe", Name: "y"},
		{Sensor: SensorFile, Arg: "C:\\x\x00"},
	}
	for _, p := range ruins {
		if err := p.Validate(); err == nil {
			t.Errorf("deveria recusar %+v", p)
		}
	}
	if _, err := ParseQueryParams(json.RawMessage(`{"sensor":"processo","arg":"x.exe","cmd":"rm"}`)); err == nil {
		t.Error("campo desconhecido")
	}
	if !SecretName("DefaultPassword") || !SecretName("api_key") || SecretName("WUServer") {
		t.Error("nomes de segredo")
	}
	if RegistryPath(`hklm\SOFTWARE\X`) != `SOFTWARE\X` {
		t.Error("caminho do registro")
	}
	a := QueryAnswer{Value: strings.Repeat("v", 500), Extra: strings.Repeat("é", 2000)}.Clip()
	if len([]rune(a.Value)) != MaxAnswerValue || len([]rune(a.Extra)) != MaxAnswerExtra {
		t.Errorf("corte: %d %d", len([]rune(a.Value)), len([]rune(a.Extra)))
	}
	if ParseAnswer(`{"value":"2.1.3","extra":"x"}`) != (QueryAnswer{Value: "2.1.3", Extra: "x"}) || ParseAnswer("erro qualquer").Value != "erro qualquer" {
		t.Error("ParseAnswer")
	}
}
