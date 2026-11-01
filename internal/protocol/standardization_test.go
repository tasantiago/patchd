package protocol

import (
	"strings"
	"testing"
)

func TestStandardShortcutValidation(t *testing.T) {
	valid := []StandardShortcut{
		{Name: "Portal RH", Kind: "site", Target: "https://rh.example/path?a=1&b=2"},
		{Name: "App", Kind: "aplicativo", Target: `C:\Program Files\App\app.exe`, OS: "windows"},
		{Name: "Arquivos", Kind: "pasta", Target: `\\server\share`},
		{Name: "App Linux", Kind: "aplicativo", Target: "/usr/bin/firefox", OS: "linux"},
		{Name: "App Mac", Kind: "aplicativo", Target: "/Applications/Firefox.app", OS: "darwin"},
		{Name: "Documentos", Kind: "pasta", Target: "~/Documents"},
	}
	for _, v := range valid {
		if err := v.Validate(); err != nil {
			t.Errorf("%+v: %v", v, err)
		}
	}
	for _, name := range []string{"../Portal", "CON", "NUL.txt", "nome\nnovo", ".", "Com1", "a/b", "nome.", " nome"} {
		v := valid[0]
		v.Name = name
		if v.Validate() == nil {
			t.Errorf("nome aceito: %q", name)
		}
	}
	for _, target := range []string{"javascript:alert(1)", "file:///etc/passwd", "https://u:p@example.com", "https://example.com\nIconFile=x", "data:text/plain,x"} {
		v := valid[0]
		v.Target = target
		if v.Validate() == nil {
			t.Errorf("URL aceita: %q", target)
		}
	}
	for _, target := range []string{`C:\app.exe --args`, `C:\app.cmd`, `\\?\C:\app.exe`, `C:\app.exe:stream.exe`, "/usr/bin/$(evil)", "~/../evil"} {
		v := valid[1]
		v.Target = target
		if strings.HasPrefix(target, "/") || strings.HasPrefix(target, "~/") {
			v.OS = "linux"
		}
		if v.Validate() == nil {
			t.Errorf("destino aceito: %q", target)
		}
	}
	if !valid[3].SupportsOS("linux") || valid[3].SupportsOS("windows") || valid[1].SupportsOS("darwin") {
		t.Fatal("filtro de plataforma incorreto")
	}
}

func TestStandardPlanLimits(t *testing.T) {
	link := StandardShortcut{Name: "Portal", Kind: "site", Target: "https://example.com"}
	p := StandardizationPlan{Revision: strings.Repeat("a", 64), Shortcuts: []StandardShortcut{link}}
	if err := p.Check(); err != nil {
		t.Fatal(err)
	}
	p.Shortcuts = append(p.Shortcuts, link)
	if p.Check() == nil {
		t.Fatal("aceitou duplicado")
	}
	p.Shortcuts = nil
	p.Revision = strings.Repeat("z", 64)
	if p.Check() == nil {
		t.Fatal("aceitou revisão inválida")
	}
}
