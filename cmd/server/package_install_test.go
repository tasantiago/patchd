package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/pkgcache"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
)

// guarda grava o instalador no cache e o registra, como o package fetch.
func guarda(t *testing.T, st *store.Memory, dir string, p store.ThirdPartyPackage, conteudo []byte) store.ThirdPartyPackage {
	t.Helper()
	h := sha256.Sum256(conteudo)
	p.SHA256, p.Size, p.URL = hex.EncodeToString(h[:]), int64(len(conteudo)), "https://x"
	if err := os.WriteFile(filepath.Join(dir, pkgcache.FileName(p.SHA256, p.MSI())), conteudo, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveThirdPartyPackage(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPackageInstall(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	dir := t.TempDir()
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
	npp := guarda(t, st, dir, store.ThirdPartyPackage{AppID: "notepad-plus-plus", WingetID: "Notepad++.Notepad++", Version: "8.9.8.1",
		Architecture: "x64", Type: "nullsoft", Scope: "machine", Arguments: "/S", ReturnCodes: map[int]string{5: "packageInUse"}}, []byte("instalador npp"))
	guarda(t, st, dir, store.ThirdPartyPackage{AppID: "google-chrome", WingetID: "Google.Chrome", Version: "155.0.8059.40", Architecture: "x64",
		Type: "wix", Arguments: `/quiet INSTALLDIR="C:\x"`}, []byte("msi do chrome"))
	_, key, _ := jobs.GenerateKey()
	m, _ := thirdparty.Default()
	install := func(args ...string) (int, string) {
		var out, errb bytes.Buffer
		code := packageInstall(ctx, memoriaComPrefixo{st}, key, m, dir, args, time.Now().UTC(), &out, &errb)
		return code, out.String() + errb.String()
	}
	approve := func(args ...string) (int, string) {
		var out, errb bytes.Buffer
		code := packageApprove(ctx, st, m, args, "linha de comando", &out, &errb)
		return code, out.String() + errb.String()
	}

	// Sem aprovação: nada é criado, e o comando diz como aprovar.
	if code, out := install("-machine", "92fc3b93", "-app", "notepad-plus-plus"); code != exitRuntime ||
		!strings.Contains(out, "notepad-plus-plus 8.9.8.1 ainda não foi aprovado: package approve -app notepad-plus-plus -version 8.9.8.1") {
		t.Fatalf("sem aprovação: %d %s", code, out)
	}
	// Aprovar só o que está guardado, numa regra com install.
	for _, c := range []struct {
		args []string
		code int
		msg  string
	}{
		{[]string{"-app", "notepad-plus-plus", "-version", "9.0"}, exitRuntime, "nenhum instalador de notepad-plus-plus 9.0 guardado"},
		{[]string{"-app", "adobe-acrobat", "-version", "1"}, exitConfig, `a regra "adobe-acrobat" do Windows não existe ou não tem "install"`},
		{[]string{"-app", "notepad-plus-plus"}, exitConfig, "informe -app e -version"},
		{[]string{"-app", "notepad-plus-plus", "-version", "8.9.8.1", "-note", "testado no piloto"}, exitOK,
			"notepad-plus-plus 8.9.8.1 aprovado (linha de comando): vale para os instaladores guardados dessa versão: nullsoft x64"},
		{[]string{"-app", "notepad-plus-plus", "-version", "8.9.8.1"}, exitOK, "já estava aprovado"},
		{[]string{"-app", "google-chrome", "-version", "155.0.8059.40"}, exitOK, "aprovado"},
	} {
		if code, out := approve(c.args...); code != c.code || !strings.Contains(out, c.msg) {
			t.Errorf("approve %v: %d %s", c.args, code, out)
		}
	}
	if l, _ := st.ThirdPartyApprovals(ctx); len(l) != 2 || l[1].Note != "testado no piloto" {
		t.Errorf("aprovações: %+v", l)
	}

	code, out := install("-machine", "92fc3b93", "-app", "notepad-plus-plus")
	if code != exitOK || !strings.Contains(out, "criado para a máquina 92fc3b93: Notepad++ 8.9.7 → 8.9.8.1 · nullsoft · x64") ||
		!strings.Contains(out, "argumentos  [/S]") || !strings.Contains(out, "assinatura  exigida: NOTEPAD++") {
		t.Fatalf("install: %d\n%s", code, out)
	}
	list, _ := st.Jobs(ctx, "92fc3b93-0003", 10)
	row, _, _ := st.JobForMachine(ctx, "92fc3b93-0003", list[0].ID) // o payload só vem por aqui
	var j jobs.Job
	json.Unmarshal([]byte(row.Payload), &j)
	p, err := jobs.ParseThirdPartyParams(j.Params)
	if err != nil || p.App != "notepad-plus-plus" || p.Version != "8.9.8.1" || p.SHA256 != npp.SHA256 || p.Size != npp.Size ||
		p.Installer != "nullsoft" || strings.Join(p.Args, " ") != "/S" || p.ReturnCodes["5"] != "packageInUse" || p.Signer != "NOTEPAD++" {
		t.Errorf("parâmetros: %+v %v", p, err)
	}

	// O arquivo sumiu do cache: nada é criado (o job só falharia na máquina, com 404).
	os.Remove(filepath.Join(dir, npp.SHA256+".exe"))
	if code, out := install("-machine", "92fc3b93", "-app", "notepad-plus-plus"); code != exitRuntime ||
		!strings.Contains(out, "o instalador de notepad-plus-plus 8.9.8.1 não está no cache ou não confere") {
		t.Errorf("sem arquivo: %d %s", code, out)
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

	// Revogar: some do índice; revogar de novo é erro.
	var out2, errb bytes.Buffer
	if code := packageRevoke(ctx, st, []string{"-app", "google-chrome", "-version", "155.0.8059.40"}, &out2, &errb); code != exitOK ||
		!strings.Contains(out2.String(), "revogada; jobs já criados seguem o prazo deles") {
		t.Errorf("revoke: %d %s %s", code, out2.String(), errb.String())
	}
	errb.Reset()
	if code := packageRevoke(ctx, st, []string{"-app", "google-chrome", "-version", "155.0.8059.40"}, &out2, &errb); code != exitRuntime ||
		!strings.Contains(errb.String(), "não estava aprovado") {
		t.Errorf("revoke de novo: %d %s", code, errb.String())
	}
}
