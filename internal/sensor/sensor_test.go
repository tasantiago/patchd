package sensor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
)

func TestSensores(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	arq := filepath.Join(dir, "lib.so")
	if err := os.WriteFile(arq, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := Sensors{
		Processes: func() ([]Process, error) {
			return []Process{{Name: "Firefox.exe", Session: 4}, {Name: "firefox.exe", Session: 6}, {Name: "explorer.exe", Session: 1}}, nil
		},
		Software: func(context.Context) ([]protocol.Software, error) {
			return []protocol.Software{{Name: "LibreOffice 25.2", Version: "25.2.1.1"}, {Name: "LibreOffice Help Pack", Version: "25.2.1.1"},
				{Name: "libreoffice (x86)", Version: "24.8.0"}, {Name: "7-Zip", Version: "24.09"},
				{Name: "BIOS_password", Version: "Exemplo@12345678"}, {Name: "HP BIOS Config", Version: "4.0"}}, nil
		},
		Registry: func(path, name string) (string, bool, bool, error) {
			switch {
			case path == `SOFTWARE\Nada`:
				return "", false, false, nil
			case name == "Falta":
				return "", true, false, nil
			}
			return "segredo-ou-valor", true, true, nil
		},
		FileVersion:     func(string) string { return "" },
		CaseInsensitive: true,
	}
	casos := []struct {
		p           jobs.QueryParams
		value, part string
	}{
		{jobs.QueryParams{Sensor: jobs.SensorFile, Arg: arq}, "existe", "sha256 ba7816bf"},
		{jobs.QueryParams{Sensor: jobs.SensorFile, Arg: filepath.Join(dir, "nao")}, "não existe", ""},
		{jobs.QueryParams{Sensor: jobs.SensorFile, Arg: dir}, "é uma pasta", ""},
		{jobs.QueryParams{Sensor: jobs.SensorProcess, Arg: "firefox.exe"}, "rodando", "2 processo(s); sessão 4, 6"},
		{jobs.QueryParams{Sensor: jobs.SensorProcess, Arg: "chrome.exe"}, "não está rodando", ""},
		{jobs.QueryParams{Sensor: jobs.SensorProgram, Arg: "libreoffice"}, "24.8.0, 25.2.1.1", "LibreOffice 25.2"},
		{jobs.QueryParams{Sensor: jobs.SensorProgram, Arg: "Acrobat"}, "não instalado", ""},
		// O marcador do .bat de senha da BIOS não é programa: fica fora da resposta.
		{jobs.QueryParams{Sensor: jobs.SensorProgram, Arg: "bios"}, "4.0", "HP BIOS Config 4.0"},
		{jobs.QueryParams{Sensor: jobs.SensorProgram, Arg: "bios_password"}, "não instalado", ""},
		{jobs.QueryParams{Sensor: jobs.SensorRegistry, Arg: `HKLM\SOFTWARE\Nada`}, "chave não existe", ""},
		{jobs.QueryParams{Sensor: jobs.SensorRegistry, Arg: `HKLM\SOFTWARE\X`, Name: "Falta"}, "valor não existe", ""},
		{jobs.QueryParams{Sensor: jobs.SensorRegistry, Arg: `HKLM\SOFTWARE\X`, Name: "DefaultPassword"}, "existe (conteúdo oculto: o nome parece de senha)", ""},
		{jobs.QueryParams{Sensor: jobs.SensorRegistry, Arg: `HKLM\SOFTWARE\X`, Name: "WUServer"}, "segredo-ou-valor", ""},
		{jobs.QueryParams{Sensor: jobs.SensorService, Arg: "Spooler"}, NotApplicable, ""},
	}
	for _, c := range casos {
		a := s.Answer(ctx, c.p)
		if a.Value != c.value || !strings.Contains(a.Extra, c.part) {
			t.Errorf("%s %s: %+v", c.p.Sensor, c.p.Arg, a)
		}
	}
	// Sem diferença de maiúsculas só no Windows.
	s.CaseInsensitive = false
	if a := s.Answer(ctx, jobs.QueryParams{Sensor: jobs.SensorProcess, Arg: "firefox.exe"}); a.Extra != "1 processo(s)" {
		t.Errorf("Linux: %+v", a)
	}
	// Os sensores deste sistema montam sem erro, e o processo do próprio teste aparece no Linux.
	sys := For(nil)
	if sys.Processes != nil {
		exe, _ := os.Executable()
		if a := sys.Answer(ctx, jobs.QueryParams{Sensor: jobs.SensorProcess, Arg: filepath.Base(exe)}); a.Value != "rodando" {
			t.Errorf("o próprio processo: %+v", a)
		}
	}
	if a := sys.Answer(ctx, jobs.QueryParams{Sensor: jobs.SensorRegistry, Arg: `HKLM\SOFTWARE\X`}); a.Value != NotApplicable {
		t.Errorf("registro fora do Windows: %+v", a)
	}
}
