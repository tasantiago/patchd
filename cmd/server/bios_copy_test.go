package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/biospw"
	"github.com/tasantiago/patchd/internal/machinebox"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

// TestBiosCopiaDeEmergencia: o export leva só as senhas que podem estar valendo, cifradas; o
// reveal abre pelo arquivo (ID, começo do ID ou nome) e pela cópia do Registro, sem banco.
func TestBiosCopiaDeEmergencia(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	priv, _ := machinebox.Generate()
	pub, _ := machinebox.Public(priv)
	kid := machinebox.KeyID(pub)
	agora := time.Now().UTC()
	maquina := func(id, nome string) {
		if _, err := st.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: id, OS: protocol.OSInfo{Hostname: nome}}); err != nil {
			t.Fatal(err)
		}
	}
	senhas := map[int64]string{}
	guarda := func(id, estado string) int64 {
		t.Helper()
		pw, _ := biospw.Generate()
		env, _ := biospw.Seal(pub, id, pw)
		b, ok, err := st.CreateBIOSPassword(ctx, store.BIOSPassword{MachineID: id, Escrow: env, EscrowKeyID: kid, CreatedAt: agora, CreatedBy: "thyago"})
		if err != nil || !ok {
			t.Fatalf("guarda: %v", err)
		}
		senhas[b.ID] = pw
		if estado != store.BIOSPending {
			_, _ = st.AttachBIOSJob(ctx, b.ID, b.ID+100, agora)
			if _, err := st.SettleBIOSPassword(ctx, b.ID, b.ID+100, estado, "", agora); err != nil {
				t.Fatal(err)
			}
		}
		return b.ID
	}
	maquina("60ef97e4-0001", "pc-lab-01")
	maquina("7aa604f7-0002", "pc-lab-02")
	maquina("92fc3b93-0003", "pc-lab-03")
	falhou := guarda("60ef97e4-0001", store.BIOSFailed)
	ativa := guarda("60ef97e4-0001", store.BIOSActive)
	pendente := guarda("7aa604f7-0002", store.BIOSPending)

	dir := t.TempDir()
	arq := filepath.Join(dir, "bios-export.json")
	_ = os.WriteFile(arq, []byte("cópia antiga"), 0o600)
	n, maqs, err := biosExport(ctx, st, arq, agora)
	if err != nil || n != 2 || maqs != 2 {
		t.Fatalf("export: %d %d %v", n, maqs, err)
	}
	raw, _ := os.ReadFile(arq)
	for _, pw := range senhas {
		if bytes.Contains(raw, []byte(pw)) {
			t.Fatal("senha legível no export")
		}
	}
	var f biosExportFile
	if err := json.Unmarshal(raw, &f); err != nil || f.Format != biosExportFormat || len(f.Passwords) != 2 {
		t.Fatalf("arquivo: %v %+v", err, f)
	}
	for _, r := range f.Passwords {
		if r.Guard == falhou {
			t.Error("a que falhou não vale na BIOS e não vai para a cópia")
		}
	}
	if fi, _ := os.Stat(arq); fi.Mode().Perm() != 0o600 {
		t.Errorf("permissão %v", fi.Mode().Perm())
	}
	if l, _ := filepath.Glob(filepath.Join(dir, ".bios-export-*")); len(l) != 0 {
		t.Errorf("temporário ficou: %v", l)
	}

	chave := filepath.Join(dir, "guarda.key")
	_ = os.WriteFile(chave, []byte(machinebox.EncodePrivate(priv)+"\n"), 0o600)
	revela := func(args ...string) (int, string, string) {
		var o, e bytes.Buffer
		code := run(append([]string{"bios", "reveal"}, args...), mapa(nil), &o, &e)
		return code, o.String(), e.String()
	}
	for _, m := range []string{"60ef97e4-0001", "60ef97e4", "PC-LAB-01"} {
		code, o, e := revela("-key", chave, "-file", arq, "-machine", m)
		if code != exitOK || !strings.Contains(o, biospw.Grouped(senhas[ativa])) || !strings.Contains(o, "pc-lab-01 (60ef97e4-0001), ativa, guarda #") ||
			!strings.Contains(e, "não entra na trilha") {
			t.Errorf("%s: %d %q %q", m, code, o, e)
		}
	}
	if code, _, _ := revela("-key", chave, "-file", arq, "-machine", "pc-lab-03"); code == exitOK {
		t.Error("máquina sem senha: deveria falhar")
	}
	if code, _, _ := revela("-key", chave, "-file", arq, "-machine", ""); code != exitConfig {
		t.Error("sem -machine")
	}
	// Prefixo vazio casaria com todas: o ID começa por um caractere qualquer.
	if code, _, e := revela("-key", chave, "-file", arq, "-machine", "pc-lab"); code == exitOK {
		t.Errorf("prefixo de nome não vale: %s", e)
	}

	// A cópia do Registro, com a chave colada na entrada.
	var linha string
	for _, r := range f.Passwords {
		if r.Guard == pendente {
			linha = r.Copy
		}
	}
	old := biosStdin
	t.Cleanup(func() { biosStdin = old })
	biosStdin = strings.NewReader(machinebox.EncodePrivate(priv) + "\n")
	code, o, _ := revela("-key", "-", "-copia", " "+linha+"\n")
	if code != exitOK || !strings.Contains(o, biospw.Grouped(senhas[pendente])) {
		t.Errorf("cópia do Registro: %d %q", code, o)
	}
	// Outra chave: recusa, sem senha na saída.
	outra, _ := machinebox.Generate()
	_ = os.WriteFile(chave, []byte(machinebox.EncodePrivate(outra)), 0o600)
	if code, o, e := revela("-key", chave, "-copia", linha); code == exitOK || o != "" || !strings.Contains(e, "outra chave de guarda") {
		t.Errorf("outra chave: %d %q %q", code, o, e)
	}
	if code, _, _ := revela("-copia", linha); code != exitConfig {
		t.Error("sem -key")
	}
}
