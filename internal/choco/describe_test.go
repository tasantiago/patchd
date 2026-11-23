package choco

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

func nupkgTitle(t *testing.T, id, version, title, authors, script string) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	add := func(name, body string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	add(id+".nuspec", `<?xml version="1.0"?><package><metadata><id>`+id+`</id><version>`+version+`</version><title>`+title+`</title><authors>`+authors+`</authors></metadata></package>`)
	add("tools/chocolateyInstall.ps1", script)
	zw.Close()
	return b.Bytes()
}

// O que a busca "Do Chocolatey" tira do pacote: o nome, a versão sem a data da correção, o tipo
// e os argumentos pelo silentArgs, e o softwareName sem o curinga.
func TestDescrever(t *testing.T) {
	npp := `$packageArgs = @{
  packageName   = 'notepadplusplus.install'
  fileType      = 'exe'
  url64bit      = 'https://github.com/notepad-plus-plus/notepad-plus-plus/releases/download/v8.9.8/npp.8.9.8.Installer.x64.exe'
  checksum64    = '1111111111111111111111111111111111111111111111111111111111111111'
  checksumType64= 'sha256'
  softwareName  = 'Notepad++*'
  silentArgs    = '/S'
}
`
	p, err := Read(nupkgTitle(t, "notepadplusplus.install", "8.9.8.20261101", "Notepad++ (Install)", "Don Ho", npp))
	if err != nil {
		t.Fatal(err)
	}
	d, err := Describe(p)
	if err != nil {
		t.Fatal(err)
	}
	if d.Title != "Notepad++ (Install)" || d.Authors != "Don Ho" || d.Version != "8.9.8" || d.Type != "nullsoft" || d.Arguments != "" ||
		d.DisplayName != "Notepad++" || !d.Prefix || d.SHA256 == "" || d.URLVar != "url64bit" {
		t.Errorf("notepad++: %+v", d)
	}
	// O MSI do Chrome: o log com caminho fica de fora; o resto é o padrão (vazio).
	p, _ = Read(nupkg(t, "googlechrome", "159.0.8154.13", scriptChrome))
	if d, err := Describe(p); err != nil || d.Type != "msi" || d.Arguments != "" || len(d.Dropped) != 2 || d.DisplayName != "" || !strings.HasPrefix(d.SilentArgs, "/quiet /norestart /l*v") {
		t.Errorf("chrome: %+v %v", d, err)
	}
	// O embutido (sem link): erro.
	p, _ = Read(nupkg(t, "x", "1.0", "Install-ChocolateyInstallPackage -File $f\n", "tools/x.exe"))
	if _, err := Describe(p); err == nil {
		t.Error("o pacote sem link passou")
	}
}

func TestAdivinharTipo(t *testing.T) {
	for _, tc := range []struct {
		fileType, link, silent string
		typ, args              string
	}{
		{"", "https://x/a.msi?b=1", "/qn /norestart ALLUSERS=1", "msi", "/qn /norestart ALLUSERS=1"},
		{"msi", "https://x/a", "/quiet", "msi", ""},
		{"exe", "https://x/a.exe", "/VERYSILENT /SUPPRESSMSGBOXES /NORESTART /SP-", "inno", ""},
		{"exe", "https://x/a.exe", "/VERYSILENT /MERGETASKS=!desktopicon", "inno", "/VERYSILENT /MERGETASKS=!desktopicon"},
		{"exe", "https://x/a.exe", "/S /allusers", "nullsoft", "/S /allusers"},
		{"exe", "https://x/a.exe", "-q -norestart", "exe", "-q -norestart"},
		{"exe", "https://x/a.exe", "", "exe", ""},
	} {
		typ, args, _ := guessType(tc.fileType, tc.link, tc.silent)
		if typ != tc.typ || args != tc.args {
			t.Errorf("%q %q: %s %q (queria %s %q)", tc.fileType, tc.silent, typ, args, tc.typ, tc.args)
		}
	}
}
