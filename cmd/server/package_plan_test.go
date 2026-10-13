package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
)

func TestPackagePlan(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	win := func(id, npp string, extra ...protocol.Software) {
		sw := append([]protocol.Software{
			{Name: "Notepad++ (64-bit x64)", Version: npp, Publisher: "Notepad++ Team", Scope: "machine"},
			{Name: "7-Zip 26.02 (x64)", Version: "26.02", Publisher: "Igor Pavlov", Scope: "machine"},
		}, extra...)
		if _, err := st.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: id,
			OS: protocol.OSInfo{ID: "windows", Name: "Windows 11 Pro", Version: "25H2", Arch: "amd64"}, Software: sw}); err != nil {
			t.Fatal(err)
		}
	}
	win("aaaa0000-0001", "8.9.7", protocol.Software{Name: "7-Zip 26.02 (x64 edition)", Version: "26.02.00.0", Publisher: "Igor Pavlov",
		Scope: "machine", ProductCode: "{23170F69-40C1-2702-2602-000001000000}"})
	win("aaaa0000-0002", "8.9.8.1")
	win("aaaa0000-0003", "8.9.6")
	if _, err := st.SaveInventory(ctx, "bbbb0000-0004", protocol.InventoryReport{SchemaVersion: 1, Hash: "l",
		OS: protocol.OSInfo{ID: "ubuntu", Name: "Ubuntu", Arch: "amd64"}}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []store.ThirdPartyPackage{
		{AppID: "notepad-plus-plus", WingetID: "Notepad++.Notepad++", Version: "8.9.8.1", Architecture: "x64", Type: "nullsoft",
			URL: "https://x", SHA256: strings.Repeat("26", 32), Size: 1, Arguments: "/S"},
		{AppID: "7zip", WingetID: "7zip.7zip", Version: "26.04", Architecture: "x64", Type: "exe", URL: "https://x", SHA256: strings.Repeat("d5", 32), Size: 1},
	} {
		st.SaveThirdPartyPackage(ctx, p)
	}
	m, _ := thirdparty.Default()
	plan := func(args ...string) (int, string) {
		var out, errb bytes.Buffer
		code := packagePlan(ctx, memoriaComPrefixo{st}, m, args, &out, &errb)
		return code, out.String() + errb.String()
	}

	// O começo do ID, como o machine list mostra (8 caracteres), resolve para o ID inteiro.
	if code, out := plan("-machine", "bbbb0000"); code != exitConfig || !strings.Contains(out, "a máquina bbbb0000-0004 é Ubuntu") {
		t.Errorf("prefixo de 8: %d %s", code, out)
	}
	code, out := plan("-machine", "aaaa0000-0001")
	for _, tr := range []string{
		"máquina aaaa0000-0001 · Windows 11 Pro 25H2 · x64\n",
		"Notepad++ (notepad-plus-plus)\n  decisão     ATUALIZAR: 8.9.7 → 8.9.8.1 · nullsoft · x64\n  pacote      pacotes/" + strings.Repeat("26", 32) + ".exe\n" +
			"  instalado   Notepad++ (64-bit x64) · exe · 8.9.7\n",
		"7-Zip (7zip)\n  decisão     DUPLICADO: 2 instalações visíveis (exe 26.02, msi 26.02.00.0): limpar antes, deixando uma\n",
		"  instalado   7-Zip 26.02 (x64 edition) · msi · 26.02.00.0\n",
	} {
		if code != exitOK || !strings.Contains(out, tr) {
			t.Errorf("-machine: faltou %q em\n%s", tr, out)
		}
	}
	// ATUALIZAR vem antes de DUPLICADO.
	if strings.Index(out, "ATUALIZAR") > strings.Index(out, "DUPLICADO") {
		t.Errorf("ordem:\n%s", out)
	}

	code, out = plan("-all")
	if code != exitOK || !strings.Contains(out, "máquinas Windows avaliadas: 3\n") {
		t.Errorf("-all: %d\n%s", code, out)
	}
	// Notepad++: 2 a atualizar (8.9.7 e 8.9.6), 1 em dia. 7-Zip: 2 a atualizar, 1 duplicado.
	linhas := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		if f := strings.Fields(l); len(f) > 0 {
			linhas[f[0]] = strings.Join(f[1:], " ")
		}
	}
	if linhas["notepad-plus-plus"] != "2 0 0 0 0 0 1" || linhas["7zip"] != "2 0 1 0 0 0 0" {
		t.Errorf("-all:\n%s", out)
	}
	if !strings.Contains(out, "\nPROGRAMA           ATUALIZAR  SEM PACOTE  DUPLICADO  BUNDLE  USUÁRIO  VERSÃO ILEGÍVEL  EM DIA\n") {
		t.Errorf("cabeçalho:\n%s", out)
	}

	for _, c := range []struct {
		args []string
		msg  string
	}{
		{nil, "use um (e só um) de -machine e -all"},
		{[]string{"-machine", "x", "-all"}, "use um (e só um)"},
		{[]string{"-machine", "nao-existe"}, `nenhuma máquina com o ID (ou começo de ID) "nao-existe"`},
		{[]string{"-machine", "aaaa0000"}, `"aaaa0000" é o começo de mais de uma máquina`},
		{[]string{"-machine", "bbbb0000-0004"}, "é Ubuntu; a instalação de terceiros é só para Windows"},
	} {
		if code, out := plan(c.args...); code != exitConfig || !strings.Contains(out, c.msg) {
			t.Errorf("%v: %d %s", c.args, code, out)
		}
	}
}

func TestSizeDelta(t *testing.T) {
	for d, want := range map[int64]string{1: "+1 byte", -1: "-1 byte", 2048: "+2048 bytes", -3: "-3 bytes"} {
		if got := sizeDelta(d); got != want {
			t.Errorf("%d: %s", d, got)
		}
	}
}
