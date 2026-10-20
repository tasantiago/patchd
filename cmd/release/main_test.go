package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/release"
)

func TestKeygenEAssinatura(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "chaves", "release.key")
	var out, errs bytes.Buffer
	if code := run([]string{"keygen", "-key", key}, &out, &errs); code != exitOK {
		t.Fatalf("keygen: %d %s", code, errs.String())
	}
	pub, err := release.ParsePublicKey(out.String())
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(key); fi.Mode().Perm() != 0o600 {
		t.Errorf("permissão da chave: %s", fi.Mode().Perm())
	}
	if code := run([]string{"keygen", "-key", key}, &out, &errs); code == exitOK {
		t.Error("keygen não pode sobrescrever uma chave existente")
	}

	// O sign confere os executáveis (Aula 14.7): têm de ser executáveis Go com a versão
	// dentro. O próprio executável deste teste serve: tem o build info e as strings
	// "v0.5.1" e "v0.5.2" deste arquivo.
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	exe, _ := os.ReadFile(self)
	dist := filepath.Join(dir, "dist")
	_ = os.MkdirAll(dist, 0o755)
	_ = os.WriteFile(filepath.Join(dist, "patchd-agent-linux-amd64"), exe, 0o755)
	_ = os.WriteFile(filepath.Join(dist, "patchd-agent-windows-amd64.exe"), exe, 0o755)
	rel := filepath.Join(dir, "releases")

	// Versão que não está nos executáveis (montada em tempo de execução, para não virar
	// string deste binário): nada é assinado.
	outra := "v0." + strconv.Itoa(7) + ".7"
	errs.Reset()
	if code := run([]string{"sign", "-key", key, "-version", outra, "-dist", dist, "-out", rel}, &out, &errs); code != exitConfig ||
		!strings.Contains(errs.String(), "patchd-agent-linux-amd64 não é da versão "+outra) || !strings.Contains(errs.String(), "nada foi assinado") {
		t.Errorf("outra versão: %d %s", code, errs.String())
	}
	if _, err := os.Stat(filepath.Join(rel, outra)); !os.IsNotExist(err) {
		t.Error("a pasta da versão recusada foi criada")
	}

	// Executáveis que não trazem a chave de quem assina (Aula 20.7): nada é assinado. A
	// frota instalaria esta versão, mas recusaria a próxima assinada pela mesma chave.
	args := []string{"sign", "-key", key, "-version", "v0.5.1", "-dist", dist, "-out", rel}
	errs.Reset()
	if code := run(args, &out, &errs); code != exitConfig ||
		!strings.Contains(errs.String(), "não confia na chave que assina (impressão "+release.KeyID(pub)+")") {
		t.Errorf("sem a chave nos executáveis: %d %s", code, errs.String())
	}
	if _, err := os.Stat(filepath.Join(rel, "v0.5.1")); !os.IsNotExist(err) {
		t.Error("a pasta da versão recusada foi criada")
	}
	// Com a chave dentro (o build a injeta com -X; aqui, anexada ao fim do executável).
	exe = append(exe, []byte(release.EncodePublicKey(pub))...)
	_ = os.WriteFile(filepath.Join(dist, "patchd-agent-linux-amd64"), exe, 0o755)
	_ = os.WriteFile(filepath.Join(dist, "patchd-agent-windows-amd64.exe"), exe, 0o755)

	out.Reset()
	if code := run(args, &out, &errs); code != exitOK || !strings.Contains(out.String(), "2 executáveis, commit ") ||
		!strings.Contains(out.String(), "chave "+release.KeyID(pub)) {
		t.Fatalf("sign: %d %s %s", code, out.String(), errs.String())
	}
	data, _ := os.ReadFile(filepath.Join(rel, "v0.5.1", release.ManifestFile))
	sig, _ := os.ReadFile(filepath.Join(rel, "v0.5.1", release.SignatureFile))
	m, err := release.Verify(pub, data, sig)
	if err != nil || len(m.Files) != 2 {
		t.Fatalf("o manifesto assinado confere com a chave pública: %+v %v", m, err)
	}
	if _, err := release.Dir(rel).Check("v0.5.1"); err != nil {
		t.Errorf("a pasta gerada passa na conferência do servidor: %v", err)
	}

	// A mesma versão não é assinada de novo.
	if code := run(args, &out, &errs); code == exitOK {
		t.Error("versão já assinada deveria ser recusada")
	}

	// Chave legível por outros: recusada.
	_ = os.Chmod(key, 0o644)
	args[6] = "v0.5.2"
	errs.Reset()
	if code := run(args, &out, &errs); code != exitConfig || !strings.Contains(errs.String(), "chmod 600") {
		t.Errorf("chave aberta: %d %s", code, errs.String())
	}
}

// TestPubkeyDaPrivada: o pubkey refaz a mesma pública do keygen, a partir da privada, e
// recusa uma chave legível por outros (Aula 9.1b).
func TestPubkeyDaPrivada(t *testing.T) {
	key := filepath.Join(t.TempDir(), "release.key")
	var gerada, errs bytes.Buffer
	if code := run([]string{"keygen", "-key", key}, &gerada, &errs); code != exitOK {
		t.Fatalf("keygen: %d %s", code, errs.String())
	}
	var refeita bytes.Buffer
	if code := run([]string{"pubkey", "-key", key}, &refeita, &errs); code != exitOK {
		t.Fatalf("pubkey: %d %s", code, errs.String())
	}
	if refeita.String() != gerada.String() {
		t.Errorf("pubkey %q, keygen %q", refeita.String(), gerada.String())
	}
	if _, err := release.ParsePublicKey(refeita.String()); err != nil {
		t.Errorf("a saída é um PATCHD_UPDATE_PUBKEY válido: %v", err)
	}
	priv, _ := os.ReadFile(key)
	if strings.Contains(refeita.String()+errs.String(), strings.TrimSpace(string(priv))) {
		t.Error("a chave privada apareceu na saída")
	}
	_ = os.Chmod(key, 0o644)
	if code := run([]string{"pubkey", "-key", key}, &refeita, &errs); code == exitOK {
		t.Error("chave legível por outros deveria ser recusada")
	}
	if code := run([]string{"pubkey"}, &refeita, &errs); code == exitOK {
		t.Error("pubkey sem -key deveria ser recusado")
	}
}

// Um arquivo que não é executável Go, mesmo com a versão dentro, não é assinado.
func TestSignRecusaNaoGo(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "release.key")
	var out, errs bytes.Buffer
	if code := run([]string{"keygen", "-key", key}, &out, &errs); code != exitOK {
		t.Fatal(errs.String())
	}
	dist := filepath.Join(dir, "dist")
	_ = os.MkdirAll(dist, 0o755)
	_ = os.WriteFile(filepath.Join(dist, "patchd-agent-linux-amd64"), []byte("agente v0.5.1 "+out.String()), 0o755)
	errs.Reset()
	if code := run([]string{"sign", "-key", key, "-version", "v0.5.1", "-dist", dist, "-out", filepath.Join(dir, "rel")}, &out, &errs); code != exitConfig ||
		!strings.Contains(errs.String(), "não é um executável Go") {
		t.Errorf("não Go: %d %s", code, errs.String())
	}
	if shortRev("962696cb6ecbb63278041f3601ae5ad215dc12b0") != "962696cb6ecb" || shortRev("") != "sem commit gravado" {
		t.Error("shortRev")
	}
}
