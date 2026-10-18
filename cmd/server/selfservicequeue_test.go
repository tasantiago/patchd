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

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/pkgcache"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/selfservice"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
)

// Aula 18.7: a fila da TI pelo terminal.
func TestFilaDaTIPelaLinhaDeComando(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	for _, maq := range []string{"60ef97e4-0001", "7aa604f7-0002"} {
		_, _ = mem.SaveInventory(ctx, maq, protocol.InventoryReport{SchemaVersion: 1, Hash: maq,
			OS:       protocol.OSInfo{ID: "windows", Arch: "amd64"},
			Software: []protocol.Software{{Name: "7-Zip 26.01 (x64)", Version: "26.01", Publisher: "Igor Pavlov", Scope: "machine"}}})
	}
	dir := t.TempDir()
	inst := []byte("instalador")
	sum := sha256.Sum256(inst)
	sha := hex.EncodeToString(sum[:])
	_ = os.WriteFile(filepath.Join(dir, pkgcache.FileName(sha, false)), inst, 0o640)
	_, _ = mem.SaveThirdPartyPackage(ctx, store.ThirdPartyPackage{AppID: "7zip", Version: "26.04", Type: "exe", Architecture: "x64", SHA256: sha, Size: int64(len(inst)), Arguments: "/S"})
	_, _ = mem.ApproveThirdParty(ctx, store.ThirdPartyApproval{AppID: "7zip", Version: "26.04", ApprovedBy: "x", ApprovedAt: time.Now()})
	_ = mem.SetSelfServiceOffer(ctx, store.SelfServiceOffer{AppID: "7zip", CanUpdate: true, NeedsApproval: true, UpdatedBy: "x"})
	m, _ := thirdparty.Default()
	_, key, _ := jobs.GenerateKey()
	svc := &selfservice.Service{Store: mem, Manifest: &m, Key: key, PackagesDir: dir, Now: time.Now}
	st := memoriaComPrefixo{mem}
	roda := func(args ...string) (int, string, string) {
		var o, e bytes.Buffer
		var code int
		switch args[0] {
		case "requests", "approve", "refuse":
			code = selfServiceQueue(ctx, st, svc, args, "thyago", &o, &e)
		default:
			code = selfServiceCommand(ctx, st, svc, args, &o, &e)
		}
		return code, o.String(), e.String()
	}
	if _, o, _ := roda("requests"); !strings.Contains(o, "nenhum pedido aguardando a TI") {
		t.Errorf("fila vazia: %s", o)
	}
	for _, u := range []string{"60ef", "7aa6"} {
		if code, o, _ := roda("request", "-machine", u, "-user", `EXEMPLO\ana`, "-app", "7zip", "-action", "atualizar"); code != exitOK || !strings.HasPrefix(o, "aprovacao:") {
			t.Fatalf("pedido em %s: %d %s", u, code, o)
		}
	}
	// O catálogo mostra o pedido à TI pelo número, não "job 0".
	if _, o, _ := roda("catalog", "-machine", "60ef", "-user", `EXEMPLO\ana`); !strings.Contains(o, "pedido: pedido à TI nº 1 · 7zip 26.04") {
		t.Errorf("catálogo:\n%s", o)
	}
	_, o, _ := roda("requests")
	if !strings.Contains(o, "aguardando  60ef97e4") || !strings.Contains(o, `EXEMPLO\ana`) || !strings.Contains(o, "atualizar 7zip") ||
		!strings.Contains(o, "expira em 6 dia(s)") && !strings.Contains(o, "expira em 7 dia(s)") || !strings.Contains(o, "2 pedido(s) aguardando a TI") {
		t.Errorf("fila:\n%s", o)
	}
	// Uso errado.
	for _, a := range [][]string{{"approve"}, {"approve", "-id", "1", "-note", "x"}, {"requests", "-id", "1"}, {"refuse", "-all", "-id", "1"}} {
		if code, _, _ := roda(a...); code != exitConfig {
			t.Errorf("%v: %d", a, code)
		}
	}
	if code, _, e := roda("approve", "-id", "99"); code != exitRuntime || !strings.Contains(e, "não existe") {
		t.Errorf("inexistente: %d %s", code, e)
	}
	// Aprovar: o job sai com o usuário como autor; a decisão entra na trilha pela linha de comando.
	code, o, _ := roda("approve", "-id", "1")
	if code != exitOK || !strings.HasPrefix(o, "pedido nº 1 aprovado: job 1 para a máquina 60ef97e4 (7zip 26.04, de EXEMPLO\\ana)") {
		t.Fatalf("aprovar: %d %s", code, o)
	}
	if j, ok, _ := mem.JobForMachine(ctx, "60ef97e4-0001", 1); !ok || j.CreatedBy != selfservice.Author(`EXEMPLO\ana`) {
		t.Errorf("job: %+v", j)
	}
	if _, o, _ := roda("catalog", "-machine", "60ef", "-user", `EXEMPLO\ana`); !strings.Contains(o, "pedido: job 1 · 7zip 26.04 · pendente") {
		t.Errorf("catálogo depois:\n%s", o)
	}
	// Decidido não decide de novo.
	if code, _, e := roda("refuse", "-id", "1"); code != exitRuntime || !strings.Contains(e, "já foi decidido") {
		t.Errorf("de novo: %d %s", code, e)
	}
	if code, o, _ := roda("refuse", "-id", "2", "-note", "  fora do escopo  "); code != exitOK || !strings.Contains(o, "pedido nº 2 recusado") {
		t.Errorf("recusar: %d %s", code, o)
	}
	_, o, _ = roda("requests", "-all")
	if !strings.Contains(o, "thyago · job 1") || !strings.Contains(o, "thyago · motivo: fora do escopo") || !strings.Contains(o, "0 pedido(s) aguardando a TI entre os 2 listados") {
		t.Errorf("fila completa:\n%s", o)
	}
	ev, _ := mem.AuditEvents(ctx, store.AuditFilter{Action: "autoatendimento."})
	var dec []store.AuditEvent
	for _, e := range ev {
		if e.Source == cliSource {
			dec = append(dec, e)
		}
	}
	if len(dec) != 2 || dec[0].Action != "autoatendimento.reprovar" || dec[1].Action != "autoatendimento.aprovar" || dec[1].Username != "thyago" ||
		!strings.Contains(dec[1].Detail, "pedido 1 · job 1") || !strings.Contains(dec[0].Detail, "motivo: fora do escopo") {
		t.Errorf("trilha: %+v", dec)
	}
}
