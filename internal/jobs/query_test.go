package jobs

import (
	"encoding/json"
	"github.com/tasantiago/patchd/internal/protocol"
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
	if got := ParseAnswer(`{"value":"2.1.3","extra":"x"}`); got.Value != "2.1.3" || got.Extra != "x" || ParseAnswer("erro qualquer").Value != "erro qualquer" {
		t.Error("ParseAnswer")
	}
}

func TestPerguntasPorUsuario(t *testing.T) {
	ok := []QueryParams{
		{Sensor: SensorExtensions},
		{Sensor: SensorExtensions, Arg: "ublock"},
		{Sensor: SensorExtensions, Arg: "cjpalhdlnbpafiamejdnhcphjbkeiagm"},
		{Sensor: SensorUserFile, Arg: `~/Downloads/x.exe`},
		{Sensor: SensorUserFile, Arg: `%USERPROFILE%\Desktop\a b.lnk`},
		{Sensor: SensorUserFile, Arg: `%appdata%\Programa\cfg.ini`},
		{Sensor: SensorUserFile, Arg: `%LOCALAPPDATA%\Programs\App`},
	}
	for _, p := range ok {
		if err := p.Validate(); err != nil {
			t.Errorf("%+v: %v", p, err)
		}
		if !ListSensor(p.Sensor) {
			t.Errorf("%s responde em lista", p.Sensor)
		}
	}
	bad := []QueryParams{
		{Sensor: SensorExtensions, Arg: strings.Repeat("x", 101)},
		{Sensor: SensorExtensions, Arg: " ublock"},
		{Sensor: SensorExtensions, Arg: "a", Name: "x"},
		{Sensor: SensorUserFile},
		{Sensor: SensorUserFile, Arg: `C:\Users\joao\x.exe`},           // caminho fixo é do sensor arquivo
		{Sensor: SensorUserFile, Arg: `%USERPROFILE%\`},                // só a pasta do usuário
		{Sensor: SensorUserFile, Arg: `~/../outro/x`},                  // sai do perfil
		{Sensor: SensorUserFile, Arg: `~/a/..`},                        //
		{Sensor: SensorUserFile, Arg: `%USERPROFILE%\..\Public\x`},     //
		{Sensor: SensorUserFile, Arg: `~//servidor/pasta/x`},           // rede
		{Sensor: SensorUserFile, Arg: `~/*.exe`},                       // curinga
		{Sensor: SensorUserFile, Arg: `~/C:/x`},                        // outra unidade
		{Sensor: SensorUserFile, Arg: `%USERPROFILE%\%TEMP%\x`},        // outra variável
		{Sensor: SensorUserFile, Arg: `%TEMP%\x`},                      // variável fora da lista
		{Sensor: SensorUserFile, Arg: `~/` + strings.Repeat("a", 260)}, // comprido
	}
	for _, p := range bad {
		if err := p.Validate(); err == nil {
			t.Errorf("%+v devia ser recusada", p)
		}
	}
	if ListSensor(SensorFile) || ListSensor(SensorProgram) {
		t.Error("os sensores da máquina não respondem em lista")
	}
	// O corte: a lista não passa do teto, e a resposta avisa.
	items := make([]protocol.AnswerItem, protocol.MaxAnswerItems+5)
	for i := range items {
		items[i] = protocol.AnswerItem{Key: "k", Name: "n\x00ome\n " + strings.Repeat("x", 400)}
	}
	a := QueryAnswer{Value: "com extensões", Items: items}.Clip()
	if len(a.Items) != protocol.MaxAnswerItems || !strings.Contains(a.Extra, "lista cortada") {
		t.Errorf("corte da lista: %d itens, extra %q", len(a.Items), a.Extra)
	}
	if n := a.Items[0].Name; strings.ContainsAny(n, "\x00\n") || len([]rune(n)) > 150 {
		t.Errorf("campo sem limpeza: %q", n)
	}
	// Item sem chave nem nome não entra; sem chave, vale o nome.
	got, _ := protocol.ClipAnswerItems([]protocol.AnswerItem{{User: "x"}, {Name: "existe"}})
	if len(got) != 1 || got[0].Key != "existe" {
		t.Errorf("chave do item: %+v", got)
	}
}
