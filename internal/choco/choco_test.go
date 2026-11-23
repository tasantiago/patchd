package choco

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const scriptChrome = `$toolsPath = Split-Path $MyInvocation.MyCommand.Definition
. $toolsPath\helpers.ps1

$version = '159.0.8154.13'
$packageArgs = @{
  packageName            = 'googlechrome'
  fileType               = 'MSI'
  url                    = 'https://dl.google.com/dl/chrome/install/googlechromestandaloneenterprise.msi'
  url64bit               = 'https://dl.google.com/dl/chrome/install/googlechromestandaloneenterprise64.msi'
  checksum               = '036a2ac57bfcf7f22b73f9c7936f15bf5eb467bae1ef00b93f538078521be744'
  checksum64             = '3a6e02f372f35dd9f97b4109440c5622e02db4c42406a7fd99d08145a5276ba0'
  checksumType           = 'sha256'
  checksumType64         = 'sha256'
  silentArgs             = "/quiet /norestart /l*v ` + "`" + `"$($env:TEMP)\x.log` + "`" + `""
}
Install-ChocolateyPackage @packageArgs
`

const scriptAdobe = `$ErrorActionPreference = 'Stop'
$DisplayName = 'Adobe Acrobat'
$allChecksumType = 'SHA256'
$MUIurl = 'https://ardownload3.adobe.com/pub/adobe/reader/win/AcrobatDC/2600322110/AcroRdrDC2600322110_MUI.exe'
$MUIchecksum = '30448284E0586A4A4C7F12FD877B9604C00548D8FD621B719DC756E1A1DD93C8'
$MUIurl64 = 'https://ardownload3.adobe.com/pub/adobe/acrobat/win/AcrobatDC/2600322110/AcroRdrDCx642600322110_MUI.exe'
$MUIchecksum64 = '345617BAF5EADB2BEDDDF6AB485085385FEF9E81F742957167B20F61E3A44F9F'
   $DownloadArgs = @{
      url                 = $MUIurl
      checksum            = $MUIChecksum
      url64bit            = $MUIurl64
      checksum64          = $MUIChecksum64
   }
`

func nupkg(t *testing.T, id, version, script string, extra ...string) []byte {
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
	add(id+".nuspec", `<?xml version="1.0"?><package xmlns="http://schemas.microsoft.com/packaging/2015/06/nuspec.xsd"><metadata><id>`+id+`</id><version>`+version+`</version></metadata></package>`)
	if script != "" {
		add("tools/chocolateyInstall.ps1", script)
	}
	for _, e := range extra {
		add(e, "MZ")
	}
	zw.Close()
	return b.Bytes()
}

func TestLerPacote(t *testing.T) {
	p, err := Read(nupkg(t, "googlechrome", "159.0.8154.13", scriptChrome))
	if err != nil || p.ID != "googlechrome" || p.Version != "159.0.8154.13" {
		t.Fatalf("pacote: %+v %v", p, err)
	}
	in, err := Extract(p.Script, "", "")
	if err != nil || in.URL != "https://dl.google.com/dl/chrome/install/googlechromestandaloneenterprise64.msi" ||
		in.SHA256 != "3a6e02f372f35dd9f97b4109440c5622e02db4c42406a7fd99d08145a5276ba0" || in.URLVar != "url64bit" {
		t.Errorf("chrome: %+v %v", in, err)
	}
	// O Adobe: a hashtable aponta para as variáveis $MUIurl64 e $MUIChecksum64.
	in, err = Extract(scriptAdobe, "", "")
	if err != nil || !strings.HasSuffix(in.URL, "AcroRdrDCx642600322110_MUI.exe") || in.SHA256 != strings.ToLower("345617BAF5EADB2BEDDDF6AB485085385FEF9E81F742957167B20F61E3A44F9F") {
		t.Errorf("adobe (padrão): %+v %v", in, err)
	}
	in, err = Extract(scriptAdobe, "$MUIurl", "MUIchecksum")
	if err != nil || !strings.HasSuffix(in.URL, "AcroRdrDC2600322110_MUI.exe") || !strings.HasPrefix(in.SHA256, "30448284") {
		t.Errorf("adobe (escolhido): %+v %v", in, err)
	}
	if _, err := Extract(scriptAdobe, "$NaoExiste", ""); err == nil || !strings.Contains(err.Error(), "NaoExiste") {
		t.Errorf("variável que não existe: %v", err)
	}
	// Sem SHA-256 (md5, ou soma de outro tamanho): o link vem sem a soma.
	in, _ = Extract("$url64 = 'https://x/a.exe'\n$checksum64 = 'd41d8cd98f00b204e9800998ecf8427e'\n$checksumType64 = 'md5'\n", "", "")
	if in.URL != "https://x/a.exe" || in.SHA256 != "" {
		t.Errorf("md5: %+v", in)
	}
	if _, err := Extract("$url = 'http://x/a.exe'\n", "", ""); err == nil || !strings.Contains(err.Error(), "https") {
		t.Errorf("http: %v", err)
	}
	if _, err := Extract("Write-Host 'oi'\n", "", ""); err == nil {
		t.Error("sem link")
	}
	// Embutido: o instalador dentro do pacote.
	p, _ = Read(nupkg(t, "7zip.install", "26.0", "Install-ChocolateyInstallPackage -File $toolsDir\\7z.exe", "tools/7z2600-x64.exe"))
	if len(p.Embedded) != 1 {
		t.Errorf("embutido: %+v", p)
	}
	if _, err := Read(nupkg(t, "x", "1.0", "")); err == nil {
		t.Error("sem o script")
	}
	if ProgramVersion("1.2.3.20251010") != "1.2.3" || ProgramVersion("159.0.8154.13") != "159.0.8154.13" {
		t.Error("ProgramVersion")
	}
}

func TestBaixarPacote(t *testing.T) {
	pk := nupkg(t, "googlechrome", "159.0.8154.13", scriptChrome)
	var pedidos []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pedidos = append(pedidos, r.URL.Path)
		switch r.URL.Path {
		case "/api/v2/package/googlechrome", "/api/v2/package/googlechrome/159.0.8154.13":
			w.Write(pk)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	if p, err := Fetch(ctx, srv.Client(), srv.URL+"/api/v2", "googlechrome", "", "patchd-teste"); err != nil || p.Version != "159.0.8154.13" {
		t.Fatalf("mais nova: %+v %v", p, err)
	}
	if _, err := Fetch(ctx, srv.Client(), srv.URL+"/api/v2", "googlechrome", "159.0.8154.13", ""); err != nil {
		t.Errorf("versão: %v", err)
	}
	if _, err := Fetch(ctx, srv.Client(), srv.URL+"/api/v2", "naoexiste", "", ""); err == nil || !strings.Contains(err.Error(), "não existe") {
		t.Errorf("404: %v", err)
	}
	if _, err := Fetch(ctx, srv.Client(), srv.URL+"/api/v2", "../x", "", ""); err == nil {
		t.Error("id inválido")
	}
}
