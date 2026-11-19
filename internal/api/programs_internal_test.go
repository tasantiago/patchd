package api

import (
	"testing"

	"github.com/tasantiago/patchd/internal/store"
)

// O nome do programa sem a versão e a arquitetura.
func TestNomeDoPrograma(t *testing.T) {
	for in, quer := range map[string]string{
		"LibreOffice 25.2.1.1":        "LibreOffice",
		"7-Zip 24.09 (x64)":           "7-Zip",
		"7-Zip 26.02 (x64 edition)":   "7-Zip",
		"Mozilla Firefox (x64 pt-BR)": "Mozilla Firefox",
		"Python 3.13.9 (64-bit)":      "Python",
		"Notepad++ (64-bit x64)":      "Notepad++",
		"Google Chrome":               "Google Chrome",
		"PowerShell 7.6.6.0-x64":      "PowerShell",
		"Java 8 Update 401":           "Java 8 Update 401",
		"Microsoft Visual C++ 2015-2022 Redistributable (x64) - 14.38.33135": "Microsoft Visual C++ 2015-2022 Redistributable",
		"1.2.3": "1.2.3",
	} {
		if got := programName(in); got != quer {
			t.Errorf("%q: %q, quer %q", in, got, quer)
		}
	}
}

// A junção: o mesmo programa e fabricante em versões e arquiteturas diferentes viram uma linha
// por sistema; outro fabricante, outra linha.
func TestJuncaoDosProgramas(t *testing.T) {
	rows := []store.SoftwareRow{
		{OSFamily: "windows", Name: "LibreOffice 25.2.1.1", Publisher: "The Document Foundation", Version: "25.2.1.1", Scope: "machine", Uninstall: "msi", Machines: 40},
		{OSFamily: "windows", Name: "LibreOffice 26.8.3.1", Publisher: "The Document Foundation", Version: "26.8.3.1", Scope: "machine", Uninstall: "msi", Machines: 120},
		{OSFamily: "windows", Name: "LibreOffice 7.0", Publisher: "Outro", Version: "7.0", Scope: "user", Machines: 2},
		{OSFamily: "linux", Name: "libreoffice", Publisher: "", Version: "1:24.2", Machines: 5},
		{OSFamily: "windows", Name: "Microsoft Edge Update", Publisher: "Microsoft", Version: "1.3", System: true, Machines: 9},
	}
	l := aggregatePrograms(rows)
	if len(l) != 4 {
		t.Fatalf("programas: %+v", l)
	}
	lo := l[0]
	if lo.OS != "linux" && lo.Key == "libreoffice" {
		lo = l[1]
	}
	var tdf *fleetProgram
	for i := range l {
		if l[i].OS == "windows" && l[i].Publisher == "The Document Foundation" {
			tdf = &l[i]
		}
	}
	if tdf == nil || tdf.Installs != 160 || tdf.Silent != 160 || len(tdf.Names) != 2 || tdf.Versions["26.8.3.1"] != 120 || tdf.System {
		t.Fatalf("LibreOffice: %+v", tdf)
	}
	if got := versionsText(tdf.Versions, 1); got != "26.8.3.1 (120) e mais 1" {
		t.Errorf("versões: %q", got)
	}
}

func TestNomeDoArquivoDoPrograma(t *testing.T) {
	for in, want := range map[string]string{
		"LibreOffice":                 "libreoffice",
		"Microsoft Visual C++ 2015":   "microsoft-visual-c-2015",
		"  ../..\\x  ":                "x",
		"本":                           "programa",
		"Adobe Acrobat Reader DC MUI": "adobe-acrobat-reader-dc-mui",
		"a234567890b234567890c234567890d234567890e234": "a234567890b234567890c234567890d234567890",
	} {
		if got := fileSlug(in); got != want {
			t.Errorf("fileSlug(%q) = %q, quer %q", in, got, want)
		}
	}
}

// A versão aprovada sem o instalador do tipo da regra: a página do programa avisa que a
// Central não oferece a instalação nova.
func TestAvisoSemInstaladorDaRegra(t *testing.T) {
	pk := func(ver, typ string) store.ThirdPartyPackage {
		return store.ThirdPartyPackage{AppID: "vlc", Version: ver, Type: typ}
	}
	rows := []tpVersionRow{{Version: "3.0.25"}, {Version: "3.0.24", Approved: true}, {Version: "3.0.23", Approved: true}}
	for _, c := range []struct {
		rule string
		pkgs []store.ThirdPartyPackage
		want string
	}{
		{"nullsoft", []store.ThirdPartyPackage{pk("3.0.24", "wix"), pk("3.0.23", "wix")}, "3.0.24"},
		{"nullsoft", []store.ThirdPartyPackage{pk("3.0.24", "wix"), pk("3.0.23", "nullsoft")}, ""},       // a 3.0.23 serve
		{"nullsoft", []store.ThirdPartyPackage{pk("3.0.25", "nullsoft"), pk("3.0.24", "wix")}, "3.0.24"}, // a 3.0.25 não está aprovada
		{"", []store.ThirdPartyPackage{pk("3.0.24", "wix")}, ""},
	} {
		if got := noRuleTypeNote(c.rule, "vlc", rows, c.pkgs); got != c.want {
			t.Errorf("%s %v: %q, quer %q", c.rule, c.pkgs, got, c.want)
		}
	}
	if got := noRuleTypeNote("nullsoft", "vlc", []tpVersionRow{{Version: "3.0.24"}}, []store.ThirdPartyPackage{pk("3.0.24", "wix")}); got != "" {
		t.Errorf("nada aprovado: %q", got)
	}
}
