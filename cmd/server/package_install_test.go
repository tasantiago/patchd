package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
)

func TestPackageInstall(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	if _, err := st.SaveInventory(ctx, "92fc3b93-0003", protocol.InventoryReport{SchemaVersion: 1, Hash: "w",
		OS: protocol.OSInfo{ID: "windows", Name: "Windows 11 Pro", Arch: "amd64"},
		Software: []protocol.Software{
			{Name: "Notepad++ (64-bit x64)", Version: "8.9.7", Publisher: "Notepad++ Team", Scope: "machine"},
			{Name: "7-Zip 26.02 (x64)", Version: "26.02", Publisher: "Igor Pavlov", Scope: "machine"},
			{Name: "7-Zip 26.02 (x64 edition)", Version: "26.02.00.0", Publisher: "Igor Pavlov", Scope: "machine", ProductCode: "{A}"},
			{Name: "Google Chrome", Version: "150.0.1", Publisher: "Google LLC", Scope: "machine", ProductCode: "{B}"},
		}}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []store.ThirdPartyPackage{
		{AppID: "notepad-plus-plus", WingetID: "Notepad++.Notepad++", Version: "8.9.8.1", Architecture: "x64", Type: "nullsoft", Scope: "machine",
			URL: "https://x", SHA256: strings.Repeat("26", 32), Size: 6963432, Arguments: "/S", ReturnCodes: map[int]string{5: "packageInUse"}},
		{AppID: "google-chrome", WingetID: "Google.Chrome", Version: "155.0.8059.40", Architecture: "x64", Type: "wix",
			URL: "https://x", SHA256: strings.Repeat("32", 32), Size: 1, Arguments: `/quiet INSTALLDIR="C:\x"`},
	} {
		st.SaveThirdPartyPackage(ctx, p)
	}
	_, key, _ := jobs.GenerateKey()
	def, _ := thirdparty.Default()
	// Uma regra local exige o assinante do Notepad++.
	local, err := thirdparty.Parse([]byte(`{"version": 1, "apps": [{"id": "notepad-plus-plus", "os": "windows", "name": "Notepad++",
		"category": "minimum", "min_version": "1", "publisher": "^Notepad\\+\\+ Team$", "match": "^Notepad\\+\\+( \\(|$)",
		"install": {"winget": "Notepad++.Notepad++", "type": "nullsoft", "signer": "Notepad++"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	m := def.Merge(local)
	install := func(args ...string) (int, string) {
		var out, errb bytes.Buffer
		code := packageInstall(ctx, memoriaComPrefixo{st}, key, m, args, time.Now().UTC(), &out, &errb)
		return code, out.String() + errb.String()
	}

	code, out := install("-machine", "92fc3b93", "-app", "notepad-plus-plus")
	if code != exitOK || !strings.Contains(out, "criado para a máquina 92fc3b93: Notepad++ 8.9.7 → 8.9.8.1 · nullsoft · x64") ||
		!strings.Contains(out, "argumentos  [/S]") || !strings.Contains(out, "assinatura  exigida: Notepad++") {
		t.Fatalf("install: %d\n%s", code, out)
	}
	list, _ := st.Jobs(ctx, "92fc3b93-0003", 10)
	if len(list) != 1 || list[0].Type != jobs.TypeThirdParty || list[0].CreatedBy != "linha de comando" {
		t.Fatalf("jobs: %+v", list)
	}
	row, _, _ := st.JobForMachine(ctx, "92fc3b93-0003", list[0].ID) // o payload só vem por aqui
	var j jobs.Job
	json.Unmarshal([]byte(row.Payload), &j)
	p, err := jobs.ParseThirdPartyParams(j.Params)
	if err != nil || p.App != "notepad-plus-plus" || p.Version != "8.9.8.1" || p.SHA256 != strings.Repeat("26", 32) || p.Size != 6963432 ||
		p.Installer != "nullsoft" || strings.Join(p.Args, " ") != "/S" || p.ReturnCodes["5"] != "packageInUse" || p.Signer != "Notepad++" {
		t.Errorf("parâmetros: %+v %v", p, err)
	}

	for _, c := range []struct {
		args []string
		code int
		msg  string
	}{
		{[]string{"-machine", "92fc3b93", "-app", "7zip"}, exitRuntime, "nada a instalar: DUPLICADO: 2 instalações visíveis"},
		{[]string{"-machine", "92fc3b93", "-app", "vscode"}, exitRuntime, `o programa "vscode" não está instalado`},
		{[]string{"-machine", "92fc3b93", "-app", "google-chrome"}, exitRuntime, "os argumentos do instalador guardado não cabem no job: argumentos com aspas"},
		{[]string{"-machine", "92fc3b93"}, exitConfig, "informe -machine e -app"},
		{[]string{"-machine", "ffff", "-app", "7zip"}, exitRuntime, `nenhuma máquina com o ID "ffff"`},
		{[]string{"-machine", "92fc3b93", "-app", "7zip", "-ttl", "1m"}, exitConfig, "-ttl deve ficar entre"},
	} {
		if code, out := install(c.args...); code != c.code || !strings.Contains(out, c.msg) {
			t.Errorf("%v: %d %s", c.args, code, out)
		}
	}
	if list, _ := st.Jobs(ctx, "92fc3b93-0003", 10); len(list) != 1 {
		t.Errorf("as recusas criaram jobs: %d", len(list))
	}
}
