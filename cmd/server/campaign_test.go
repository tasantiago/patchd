package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/campaign"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
	"github.com/tasantiago/patchd/internal/tpinstall"
)

func TestComandosDeCampanha(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	for maq, anel := range map[string]int{"60ef97e4-0001": 0, "92fc3b93-0002": 1} {
		_, _ = mem.SaveInventory(ctx, maq, protocol.InventoryReport{SchemaVersion: 1, Hash: maq})
		_, _ = mem.SetMachineRing(ctx, maq, anel)
	}
	agora := time.Now().UTC()
	roda := func(args ...string) (int, string, string) {
		var o, e bytes.Buffer
		code := campaignCommand(ctx, mem, nil, args, agora, &o, &e)
		return code, o.String(), e.String()
	}
	for _, c := range []struct {
		args []string
		msg  string
	}{
		{[]string{"create", "-name", "x", "-type", "rescan", "-rings", "1,0"}, "ordem"},
		{[]string{"create", "-name", "x", "-type", "update", "-rings", "0,1"}, "fontes"},
		{[]string{"create", "-type", "rescan", "-rings", "0"}, "nome"},
		{[]string{"create", "-name", "x", "-type", "shell", "-rings", "0"}, "desconhecido"},
	} {
		if code, _, e := roda(c.args...); code != exitConfig || !strings.Contains(e, c.msg) {
			t.Errorf("%v: %d %s", c.args, code, e)
		}
	}
	code, o, e := roda("create", "-name", "busca geral", "-type", "rescan", "-rings", "0,1", "-observe", "10m", "-ttl", "15m")
	if code != exitOK || strings.TrimSpace(o) != "1" || !strings.Contains(e, "libera o anel 0 no próximo minuto") {
		t.Fatalf("create: %d %q %s", code, o, e)
	}
	// O laço do servidor libera o anel 0.
	_, key, _ := jobs.GenerateKey()
	eng := &campaign.Engine{Store: mem, Key: key}
	if err := eng.Step(ctx, agora); err != nil {
		t.Fatal(err)
	}
	_, o, _ = roda("show", "-id", "1")
	if !strings.Contains(o, "0 (atual)  1    60ef97e4  pendente") || !strings.Contains(o, "ainda não liberado") || !strings.Contains(o, "observação: 10m0s") {
		t.Errorf("show:\n%s", o)
	}
	if _, o, _ := roda("list"); !strings.Contains(o, "[0]→1") || !strings.Contains(o, "em_andamento") {
		t.Errorf("list:\n%s", o)
	}
	if code, _, e := roda("resume", "-id", "1"); code != exitRuntime || !strings.Contains(e, "não pausada") {
		t.Errorf("resume em andamento: %d %s", code, e)
	}
	if code, _, e := roda("pause", "-id", "1"); code != exitOK || !strings.Contains(e, "pausada") {
		t.Errorf("pause: %d %s", code, e)
	}
	if code, _, _ := roda("resume", "-id", "1"); code != exitOK {
		t.Error("resume")
	}
	code, _, e = roda("cancel", "-id", "1")
	if code != exitOK || !strings.Contains(e, "1 job(s) pendente(s) cancelado(s)") {
		t.Errorf("cancel: %d %s", code, e)
	}
	if l, _ := mem.Jobs(ctx, "60ef97e4-0001", 1); l[0].State != jobs.StateCanceled || !strings.Contains(l[0].Detail, "campanha 1") {
		t.Errorf("job cancelado: %+v", l[0])
	}
	if code, _, _ := roda("cancel", "-id", "1"); code != exitRuntime {
		t.Error("cancelar de novo")
	}
	if code, _, e := roda("show", "-id", "9"); code != exitRuntime || !strings.Contains(e, "não existe") {
		t.Errorf("inexistente: %s", e)
	}
}

// Campanha de terceiros pela linha de comando (Aula 14.7): a versão tem de estar pronta na
// criação; o show diz o programa e o que cada anel liberou.
func TestCampanhaDeTerceirosPelaLinhaDeComando(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	dir := t.TempDir()
	inst := []byte("MSI do LibreOffice")
	h := sha256.Sum256(inst)
	sum := hex.EncodeToString(h[:])
	_, _ = mem.SaveThirdPartyPackage(ctx, store.ThirdPartyPackage{AppID: "libreoffice", WingetID: "TheDocumentFoundation.LibreOffice",
		Version: "26.8.1.1", Architecture: "x64", Type: "msi", URL: "https://x", SHA256: sum, Size: int64(len(inst)), Arguments: "/quiet /norestart"})
	_ = os.WriteFile(filepath.Join(dir, sum+".msi"), inst, 0o640)
	for maq, sw := range map[string][]protocol.Software{
		"92fc3b93-0002": {{Name: "LibreOffice 25.2.1.1", Version: "25.2.1.1", Publisher: "The Document Foundation", Scope: "machine", ProductCode: "{LO}"}},
		"aaaa0000-0003": {{Name: "Google Chrome", Version: "155.0", Publisher: "Google LLC", Scope: "machine"}},
	} {
		_, _ = mem.SaveInventory(ctx, maq, protocol.InventoryReport{SchemaVersion: 1, Hash: maq, OS: protocol.OSInfo{ID: "windows", Arch: "amd64"}, Software: sw})
		_, _ = mem.SetMachineRing(ctx, maq, 0)
	}
	m, _ := thirdparty.Default()
	tp := &tpinstall.Campaigner{Store: mem, Manifest: m, Dir: dir}
	roda := func(args ...string) (int, string, string) {
		var o, e bytes.Buffer
		code := campaignCommand(ctx, mem, tp, args, time.Now().UTC(), &o, &e)
		return code, o.String(), e.String()
	}
	criar := []string{"create", "-type", "thirdparty", "-app", "libreoffice", "-version", "26.8.1.1", "-rings", "0,1", "-observe", "1h"}
	if code, _, e := roda("create", "-type", "thirdparty", "-app", "libreoffice", "-rings", "0"); code != exitConfig || !strings.Contains(e, "-app e -version") {
		t.Errorf("sem versão: %d %s", code, e)
	}
	if code, _, e := roda(criar...); code != exitRuntime || !strings.Contains(e, "a campanha não foi criada: libreoffice 26.8.1.1 não está aprovado") {
		t.Errorf("sem aprovação: %d %s", code, e)
	}
	_, _ = mem.ApproveThirdParty(ctx, store.ThirdPartyApproval{AppID: "libreoffice", Version: "26.8.1.1", ApprovedBy: "ana"})
	code, o, e := roda(criar...)
	if code != exitOK || strings.TrimSpace(o) != "1" {
		t.Fatalf("create: %d %q %s", code, o, e)
	}
	_, key, _ := jobs.GenerateKey()
	eng := &campaign.Engine{Store: mem, Key: key, ThirdParty: tp}
	if err := eng.Step(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	_, o, _ = roda("show", "-id", "1")
	for _, tr := range []string{"campanha 1: libreoffice 26.8.1.1", "tipo: thirdparty (programa: libreoffice 26.8.1.1)",
		"liberação: anel 0: 1 job(s); fora: 1 sem o programa", "92fc3b93  pendente"} {
		if !strings.Contains(o, tr) {
			t.Errorf("show: faltou %q\n%s", tr, o)
		}
	}
	if _, o, _ := roda("list"); !strings.Contains(o, "thirdparty (libreoffice 26.8.1.1)") {
		t.Errorf("list:\n%s", o)
	}
}

// Aula 16.4: a campanha do Windows pela linha de comando leva KBs normalizados.
func TestCampanhaDoWindowsPelaLinhaDeComando(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	roda := func(args ...string) (int, string, string) {
		var o, e bytes.Buffer
		code := campaignCommand(ctx, mem, nil, args, time.Now().UTC(), &o, &e)
		return code, o.String(), e.String()
	}
	for _, c := range []struct {
		args []string
		msg  string
	}{
		{[]string{"create", "-type", "windows_update", "-rings", "0"}, "-kb vai com -type windows_update"},
		{[]string{"create", "-name", "x", "-type", "rescan", "-kb", "5127648", "-rings", "0"}, "-kb vai com"},
		{[]string{"create", "-type", "windows_update", "-kb", "5127648", "-sources", "curl", "-rings", "0"}, "sem -sources"},
		{[]string{"create", "-type", "windows_update", "-kb", "KB51x", "-rings", "0"}, "fora do formato"},
	} {
		if code, _, e := roda(c.args...); code != exitConfig || !strings.Contains(e, c.msg) {
			t.Errorf("%v: %d %s", c.args, code, e)
		}
	}
	code, o, e := roda("create", "-type", "windows_update", "-kb", "kb5127648, 5127648,KB5130001", "-rings", "0,1", "-window")
	if code != exitOK || strings.TrimSpace(o) != "1" {
		t.Fatalf("create: %d %q %s", code, o, e)
	}
	c, _, _ := mem.Campaign(ctx, 1)
	if c.Name != "Windows KB5127648, KB5130001" || strings.Join(c.Sources, ",") != "KB5127648,KB5130001" || !c.UseWindow {
		t.Errorf("campanha: %+v", c)
	}
	if _, o, _ := roda("show", "-id", "1"); !strings.Contains(o, "tipo: windows_update (KBs: KB5127648, KB5130001)") {
		t.Errorf("show:\n%s", o)
	}
}
