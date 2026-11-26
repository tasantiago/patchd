package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
	"github.com/tasantiago/patchd/internal/window"
)

// prediosCLI põe as máquinas (ID → tombo no nome) nos prédios do patrimônio e devolve os IDs
// dos prédios pelo nome.
func prediosCLI(t *testing.T, mem *store.Memory, maq map[string]string, bens []store.AssetLocation) map[string]int64 {
	t.Helper()
	ctx := context.Background()
	for id, nome := range maq {
		if _, err := mem.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: id + nome, OS: protocol.OSInfo{ID: "windows", Arch: "amd64", Hostname: nome}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := mem.ReplaceAssetLocations(ctx, bens, store.AssetImport{By: "t"}); err != nil {
		t.Fatal(err)
	}
	out := map[string]int64{}
	bs, _ := mem.Buildings(ctx)
	for _, b := range bs {
		out[b.Name] = b.ID
	}
	return out
}

func TestComandosDeCampanha(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	ids := prediosCLI(t, mem, map[string]string{"60ef97e4-0001": "EQ1001", "92fc3b93-0002": "EQ1002"},
		[]store.AssetLocation{{Tombo: "1001", Building: "PREDIO A"}, {Tombo: "1002", Building: "PREDIO B"}})
	a, b := fmt.Sprint(ids["PREDIO A"]), fmt.Sprint(ids["PREDIO B"])
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
		{[]string{"create", "-name", "x", "-type", "rescan", "-rings", "0,1"}, "as campanhas novas são por prédio e andar"},
		{[]string{"create", "-name", "x", "-type", "rescan"}, "informe o escopo"},
		{[]string{"create", "-name", "x", "-type", "rescan", "-buildings", "999"}, "Prédio desconhecido"},
		{[]string{"create", "-name", "x", "-type", "rescan", "-floor", "1"}, "ID do prédio=nome do andar"},
		{[]string{"create", "-name", "x", "-type", "rescan", "-buildings", a, "-pilot-building", b}, "O piloto precisa ser"},
		{[]string{"create", "-name", "x", "-type", "rescan", "-buildings", a, "-window"}, "sem janela de manutenção ativa: Predio A"},
		{[]string{"create", "-name", "x", "-type", "update", "-all-buildings"}, "fontes"},
		{[]string{"create", "-type", "rescan", "-all-buildings"}, "nome"},
		{[]string{"create", "-name", "x", "-type", "shell", "-all-buildings"}, "desconhecido"},
	} {
		if code, _, e := roda(c.args...); code != exitConfig || !strings.Contains(e, c.msg) {
			t.Errorf("%v: %d %s", c.args, code, e)
		}
	}
	code, o, e := roda("create", "-name", "busca geral", "-type", "rescan", "-all-buildings", "-pilot-building", a, "-observe", "10m", "-ttl", "15m")
	if code != exitOK || strings.TrimSpace(o) != "1" || !strings.Contains(e, `libera "Piloto — Predio A" no próximo minuto`) || !strings.Contains(e, "2 equipamento(s)") {
		t.Fatalf("create: %d %q %s", code, o, e)
	}
	if c, _, _ := mem.Campaign(ctx, 1); c.Scope == nil || len(c.Rings) != 0 || c.Scope.Pilot.BuildingID != ids["PREDIO A"] {
		t.Fatalf("campanha: %+v", c)
	}
	// O laço do servidor libera o piloto.
	_, key, _ := jobs.GenerateKey()
	eng := &campaign.Engine{Store: mem, Key: key}
	if err := eng.Step(ctx, agora); err != nil {
		t.Fatal(err)
	}
	_, o, _ = roda("show", "-id", "1")
	if !strings.Contains(o, "Piloto — Predio A (atual)                    1    60ef97e4  pendente") || !strings.Contains(o, "ainda não liberado") || !strings.Contains(o, "observação: 10m0s") ||
		!strings.Contains(o, "etapas: [Piloto — Predio A] → Demais equipamentos do escopo") {
		t.Errorf("show:\n%s", o)
	}
	if _, o, _ := roda("list"); !strings.Contains(o, "[Piloto — Predio A]") || !strings.Contains(o, "em_andamento") {
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
// criação; o show diz o programa e o que cada grupo de implantação liberou.
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
	}
	m, _ := thirdparty.Default()
	tp := &tpinstall.Campaigner{Store: mem, Manifest: m, Dir: dir}
	roda := func(args ...string) (int, string, string) {
		var o, e bytes.Buffer
		code := campaignCommand(ctx, mem, tp, args, time.Now().UTC(), &o, &e)
		return code, o.String(), e.String()
	}
	criar := []string{"create", "-type", "thirdparty", "-app", "libreoffice", "-version", "26.8.1.1", "-unlocated", "-observe", "1h"}
	if code, _, e := roda("create", "-type", "thirdparty", "-app", "libreoffice", "-unlocated"); code != exitConfig || !strings.Contains(e, "-app e -version") {
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
		"liberação: Equipamentos sem localização identificada: 1 job(s); fora: 1 sem o programa", "92fc3b93  pendente"} {
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
		{[]string{"create", "-type", "windows_update", "-unlocated"}, "-kb vai com -type windows_update"},
		{[]string{"create", "-name", "x", "-type", "rescan", "-kb", "5127648", "-unlocated"}, "-kb vai com"},
		{[]string{"create", "-type", "windows_update", "-kb", "5127648", "-sources", "curl", "-unlocated"}, "sem -sources"},
		{[]string{"create", "-type", "windows_update", "-kb", "KB51x", "-unlocated"}, "fora do formato"},
	} {
		if code, _, e := roda(c.args...); code != exitConfig || !strings.Contains(e, c.msg) {
			t.Errorf("%v: %d %s", c.args, code, e)
		}
	}
	ids := prediosCLI(t, mem, map[string]string{"m1": "EQ5001"}, []store.AssetLocation{{Tombo: "5001", Building: "FORUM X"}})
	loc, _ := time.LoadLocation("America/Porto_Velho")
	_ = mem.SetBuildingWindows(ctx, []int64{ids["FORUM X"]}, window.Window{Days: window.AllDays, Start: 22 * time.Hour, Duration: 6 * time.Hour, Location: loc}, "t")
	code, o, e := roda("create", "-type", "windows_update", "-kb", "kb5127648, 5127648,KB5130001", "-all-buildings", "-window")
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

// Aula 16.5: a campanha de reinício pela linha de comando.
func TestCampanhaDeReinicioPelaLinhaDeComando(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	roda := func(args ...string) (int, string, string) {
		var o, e bytes.Buffer
		code := campaignCommand(ctx, mem, nil, args, time.Now().UTC(), &o, &e)
		return code, o.String(), e.String()
	}
	if code, _, e := roda("create", "-name", "x", "-type", "rescan", "-delay", "15m", "-unlocated"); code != exitConfig || !strings.Contains(e, "-delay vai só com -type reboot") {
		t.Errorf("delay em rescan: %d %s", code, e)
	}
	if code, _, e := roda("create", "-type", "reboot", "-delay", "90m", "-unlocated"); code != exitConfig || !strings.Contains(e, "aviso do reinício") {
		t.Errorf("delay fora: %d %s", code, e)
	}
	ids := prediosCLI(t, mem, map[string]string{"m1": "EQ5001"}, []store.AssetLocation{{Tombo: "5001", Building: "FORUM X"}})
	_ = mem.SetBuildingWindows(ctx, []int64{ids["FORUM X"]}, window.Window{Days: window.AllDays, Start: 2 * time.Hour, Duration: 2 * time.Hour, Location: time.UTC}, "t")
	if code, _, e := roda("create", "-type", "reboot", "-buildings", fmt.Sprint(ids["FORUM X"]), "-window"); code != exitOK {
		t.Fatalf("create: %d %s", code, e)
	}
	c, _, _ := mem.Campaign(ctx, 1)
	if c.Name != "Reinício" || c.RebootDelay != 15*time.Minute || !c.UseWindow {
		t.Errorf("campanha: %+v", c)
	}
	if _, o, _ := roda("show", "-id", "1"); !strings.Contains(o, "tipo: reboot (onde houver reinício pendente, com aviso de 15 min)") {
		t.Errorf("show:\n%s", o)
	}
}
