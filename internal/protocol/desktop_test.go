package protocol

import "testing"

// O atalho da área de trabalho pública: só um .exe por caminho absoluto ou variável de
// sistema, sem argumentos, sem .., sem caminho de rede nem de perfil.
func TestAtalhoNaAreaDeTrabalhoPublica(t *testing.T) {
	bons := []DesktopShortcut{
		{App: "irpf-2026", Name: "IRPF 2026", Target: `C:\Arquivos de Programas RFB\IRPF2026\irpf.exe`},
		{App: "app", Name: "Sistema (Teste)", Target: `%ProgramFiles%\Empresa\App\app.exe`, Icon: `%ProgramFiles%\Empresa\App\app.ico`},
		{App: "app", Name: "App", Target: `%ProgramFiles(x86)%\App\APP.EXE`, Icon: `C:\Windows\System32\shell32.dll,13`},
	}
	for _, s := range bons {
		if err := s.Validate(); err != nil {
			t.Errorf("%+v: %v", s, err)
		}
	}
	ruins := map[string]DesktopShortcut{
		"rede":        {App: "a", Name: "A", Target: `\\servidor\share\a.exe`},
		"relativo":    {App: "a", Name: "A", Target: `a.exe`},
		"perfil":      {App: "a", Name: "A", Target: `%UserProfile%\a.exe`},
		"ponto-ponto": {App: "a", Name: "A", Target: `C:\Programa\..\Windows\System32\cmd.exe`},
		"argumento":   {App: "a", Name: "A", Target: `C:\a.exe -x`},
		"aspas":       {App: "a", Name: "A", Target: `"C:\a.exe"`},
		"script":      {App: "a", Name: "A", Target: `C:\a.bat`},
		"fluxo":       {App: "a", Name: "A", Target: `C:\a.exe:b.exe`},
		"barra":       {App: "a", Name: "A", Target: `C:/a.exe`},
		"nome":        {App: "a", Name: `A\B`, Target: `C:\a.exe`},
		"reservado":   {App: "a", Name: "CON", Target: `C:\a.exe`},
		"icone":       {App: "a", Name: "A", Target: `C:\a.exe`, Icon: `C:\a.png`},
		"app":         {App: "A B", Name: "A", Target: `C:\a.exe`},
		"variavel":    {App: "a", Name: "A", Target: `%ProgramFiles%\%Outra%\a.exe`},
	}
	for nome, s := range ruins {
		if err := s.Validate(); err == nil {
			t.Errorf("%s aceito: %+v", nome, s)
		}
	}
	if f, n := SplitIcon(`C:\x\app.exe,12`); f != `C:\x\app.exe` || n != 12 {
		t.Errorf("SplitIcon: %q %d", f, n)
	}
	if v := DesktopEnvVar(`%programfiles(x86)%\a.exe`); v != "programfiles(x86)" {
		t.Errorf("DesktopEnvVar: %q", v)
	}
	dup := DesktopShortcuts{Items: []DesktopShortcut{bons[0], {App: "outro", Name: "irpf 2026", Target: `C:\b.exe`}}}
	if dup.Check() == nil {
		t.Error("dois atalhos com o mesmo nome")
	}
	if bons[0].Fingerprint() == bons[1].Fingerprint() {
		t.Error("fingerprint")
	}
}
