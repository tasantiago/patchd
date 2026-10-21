package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/machinebox"
)

func mapa(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestBiosKeygen(t *testing.T) {
	tr := &trilhaFalsa{}
	old := cliAuditOpen
	t.Cleanup(func() { cliAuditOpen = old })
	cliAuditOpen = func(config.Lookup) (auditRecorder, func(), error) { return tr, func() {}, nil }
	dir := t.TempDir()
	arq := filepath.Join(dir, "guarda.key")
	var o, e bytes.Buffer
	if code := run([]string{"bios", "keygen", "-out", arq}, mapa(nil), &o, &e); code != exitOK {
		t.Fatalf("%d %s", code, e.String())
	}
	id := strings.TrimSpace(o.String())
	if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(id) || !strings.Contains(e.String(), "duas cópias fora do servidor") {
		t.Errorf("saída: %q %q", id, e.String())
	}
	if len(tr.ev) != 1 || tr.ev[0].Action != "cli.bios.keygen" {
		t.Errorf("trilha: %+v", tr.ev)
	}
	fi, err := os.Stat(arq)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("arquivo: %v %v", fi, err)
	}
	b, _ := os.ReadFile(arq)
	priv, err := machinebox.ParsePrivate(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	if pub, _ := machinebox.Public(priv); machinebox.KeyID(pub) != id {
		t.Error("a impressão não é a da chave gravada")
	}
	// Não sobrescreve.
	if code := run([]string{"bios", "keygen", "-out", arq}, mapa(nil), &o, &e); code == exitOK {
		t.Error("sobrescreveu a chave")
	}
	// O arquivo vazio do gerar-segredos.sh recebe a chave.
	vazio := filepath.Join(dir, "vazio.key")
	_ = os.WriteFile(vazio, nil, 0o600)
	if code := run([]string{"bios", "keygen", "-out", vazio}, mapa(nil), &o, &e); code != exitOK {
		t.Errorf("arquivo vazio: %d %s", code, e.String())
	}
	if b, _ := os.ReadFile(vazio); !strings.HasPrefix(string(b), "patchd_mbox_") {
		t.Error("a chave não foi gravada no arquivo vazio")
	}
	// key: a impressão da configurada; sem a variável, erro de configuração.
	o.Reset()
	if code := run([]string{"bios", "key"}, mapa(map[string]string{"PATCHD_BIOS_ESCROW_KEY_FILE": arq}), &o, &e); code != exitOK || strings.TrimSpace(o.String()) != id {
		t.Errorf("key: %d %q", code, o.String())
	}
	if code := run([]string{"bios", "key"}, mapa(nil), &o, &e); code != exitConfig {
		t.Errorf("key sem a variável: %d", code)
	}
	ruim := filepath.Join(dir, "ruim.key")
	_ = os.WriteFile(ruim, []byte("patchd_mbox_xyz"), 0o600)
	if _, err := biosEscrowKey(mapa(map[string]string{"PATCHD_BIOS_ESCROW_KEY_FILE": ruim})); err == nil {
		t.Error("chave ilegível aceita")
	}
	if k, err := biosEscrowKey(mapa(nil)); k != nil || err != nil {
		t.Error("sem a variável: guarda desligada, sem erro")
	}
	if code := run([]string{"bios"}, mapa(nil), &o, &e); code != exitConfig {
		t.Errorf("sem comando: %d", code)
	}
}
