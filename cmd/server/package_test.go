package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/thirdparty"
	"github.com/tasantiago/patchd/internal/winget"
)

const testdataWinget = "../../internal/winget/testdata/"

func wingetFalso(t *testing.T) *winget.Client {
	t.Helper()
	zip, err := os.ReadFile(testdataWinget + "7zip.7zip.installer.yaml")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/repos/microsoft/winget-pkgs/contents/manifests/7/7zip/7zip":
			w.Write([]byte(`[{"name":"25.01","type":"dir"},{"name":"26.04","type":"dir"}]`))
		case "/raw/manifests/7/7zip/7zip/26.04/7zip.7zip.installer.yaml":
			w.Write(zip)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c := winget.NewClient("patchd-teste")
	c.APIURL, c.RawURL = srv.URL+"/api", srv.URL+"/raw"
	return c
}

func TestPackageShow(t *testing.T) {
	c := wingetFalso(t)
	m, err := thirdparty.Parse([]byte(`{"version": 1, "apps": [
		{"id": "7zip", "os": "windows", "name": "7-Zip", "category": "feed", "match": "^7-Zip", "source": {"type": "winget", "id": "7zip.7zip"},
		 "install": {"winget": "7zip.7zip", "type": "exe"}},
		{"id": "sumiu", "os": "windows", "name": "Sumiu", "category": "minimum", "match": "^Sumiu", "min_version": "1",
		 "install": {"winget": "Empresa.Sumiu"}},
		{"id": "so-avalia", "os": "windows", "name": "Só avalia", "category": "minimum", "match": "^X", "min_version": "1"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	show := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := packageShow(context.Background(), c, m, args, &out, &errb)
		return code, out.String(), errb.String()
	}

	// A versão mais nova; o exe x64, como a regra pede; o MSI descartado pelo tipo.
	code, out, _ := show("-app", "7zip")
	for _, tr := range []string{
		"7-Zip (7zip) ← winget 7zip.7zip 26.04\n",
		"  instalador  #2 exe · x64 · escopo machine\n",
		"  baixar de   https://www.7-zip.org/a/7z2604-x64.exe\n",
		`  comando     "<arquivo>" /S` + "\n",
		"  saída       0 ok · os demais: falhou\n",
		"  descartado  #6 wix · x64: tipo wix; a regra exige exe\n",
	} {
		if code != exitOK || !strings.Contains(out, tr) {
			t.Errorf("-app 7zip: %d, faltou %q em\n%s", code, tr, out)
		}
	}
	// -type troca o tipo da regra: o MSI, pelo msiexec, com os códigos do Windows Installer.
	if _, out, e := show("-app", "7zip", "-type", "wix", "-version", "26.04"); !strings.Contains(out, `comando     msiexec.exe /i "<arquivo>" /quiet /norestart`) ||
		!strings.Contains(out, "0 ok · 1618 tentar_depois (installInProgress) · 1641 reiniciar (rebootInitiated) · 3010 reiniciar (rebootRequiredToFinish) · os demais: falhou") {
		t.Errorf("-type wix:\n%s|%s", out, e)
	}
	// -all: um programa com erro não esconde os outros, e o código de saída diz que falhou.
	code, out, _ = show("-all")
	if code != exitRuntime || !strings.Contains(out, "instalador  #2 exe") || !strings.Contains(out, "Sumiu (sumiu) ← winget Empresa.Sumiu\n  ERRO: ") {
		t.Errorf("-all: %d\n%s", code, out)
	}
	// -file: sem rede.
	code, out, _ = show("-file", testdataWinget+"Microsoft.VisualStudioCode.installer.yaml")
	if code != exitOK || !strings.Contains(out, "Microsoft.VisualStudioCode 1.141.0 (arquivo local)") || !strings.Contains(out, "descartado  #1 inno · x64: escopo user") {
		t.Errorf("-file: %d\n%s", code, out)
	}
	if code, out, _ := show("-file", testdataWinget+"Adobe.Acrobat.Reader.64-bit.installer.yaml", "-arch", "x86"); code != exitRuntime ||
		!strings.Contains(out, "ERRO: Adobe.Acrobat.Reader.64-bit 26.002.21996: nenhum instalador serve") {
		t.Errorf("acrobat x86: %d\n%s", code, out)
	}

	for _, c := range []struct {
		args []string
		msg  string
	}{
		{nil, "use um (e só um)"},
		{[]string{"-app", "7zip", "-all"}, "use um (e só um)"},
		{[]string{"-app", "so-avalia"}, `a regra "so-avalia" do Windows não existe ou não tem "install"`},
		{[]string{"-all", "-version", "1"}, "-version vale para um programa só"},
		{[]string{"-app", "7zip", "-type", "msix"}, `-type "msix"`},
	} {
		if code, _, e := show(c.args...); code != exitConfig || !strings.Contains(e, c.msg) {
			t.Errorf("%v: %d %s", c.args, code, e)
		}
	}
}

func TestPackageUso(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"package"}, func(string) (string, bool) { return "", false }, &out, &errb); code != exitConfig ||
		!strings.Contains(errb.String(), "uso: patchd-server package <comando>") {
		t.Errorf("%d %s", code, errb.String())
	}
}
