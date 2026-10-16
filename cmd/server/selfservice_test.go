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

// Aula 17.2: a simulação do agente pela linha de comando usa o mesmo serviço da API.
func TestAutoatendimentoPelaLinhaDeComando(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	const maq = "60ef97e4-0001"
	_, _ = mem.SaveInventory(ctx, maq, protocol.InventoryReport{SchemaVersion: 1, Hash: "w",
		OS:       protocol.OSInfo{ID: "windows", Arch: "amd64"},
		Software: []protocol.Software{{Name: "7-Zip 26.01 (x64)", Version: "26.01", Publisher: "Igor Pavlov", Scope: "machine"}}})
	dir := t.TempDir()
	inst := []byte("instalador")
	sum := sha256.Sum256(inst)
	sha := hex.EncodeToString(sum[:])
	_ = os.WriteFile(filepath.Join(dir, pkgcache.FileName(sha, false)), inst, 0o640)
	_, _ = mem.SaveThirdPartyPackage(ctx, store.ThirdPartyPackage{AppID: "7zip", Version: "26.04", Type: "exe", Architecture: "x64", SHA256: sha, Size: int64(len(inst)), Arguments: "/S"})
	_, _ = mem.ApproveThirdParty(ctx, store.ThirdPartyApproval{AppID: "7zip", Version: "26.04", ApprovedBy: "x", ApprovedAt: time.Now()})
	_ = mem.SetSelfServiceOffer(ctx, store.SelfServiceOffer{AppID: "7zip", CanUpdate: true, Groups: []string{"GG-Exemplo"}, UpdatedBy: "x"})
	m, _ := thirdparty.Default()
	_, key, _ := jobs.GenerateKey()
	svc := &selfservice.Service{Store: mem, Manifest: &m, Key: key, PackagesDir: dir, Now: time.Now}
	roda := func(args ...string) (int, string, string) {
		var o, e bytes.Buffer
		code := selfServiceCommand(ctx, memoriaComPrefixo{mem}, svc, args, &o, &e)
		return code, o.String(), e.String()
	}
	if code, _, e := roda("catalog", "-machine", "60ef"); code != exitConfig || !strings.Contains(e, "-machine e -user") {
		t.Errorf("sem usuário: %d %s", code, e)
	}
	if _, o, _ := roda("catalog", "-machine", "60ef", "-user", `EXEMPLO\ana`); !strings.Contains(o, "0 programa(s) na loja") {
		t.Errorf("sem grupo:\n%s", o)
	}
	_, o, _ := roda("catalog", "-machine", "60ef", "-user", `EXEMPLO\ana`, "-groups", `EXEMPLO\Domain Users, EXEMPLO\GG-Exemplo`)
	if !strings.Contains(o, "7zip      26.01      atualizar  26.04") || !strings.Contains(o, "1 programa(s) na loja") {
		t.Errorf("com grupo:\n%s", o)
	}
	code, o, e := roda("request", "-machine", "60ef", "-user", `EXEMPLO\ana`, "-groups", `EXEMPLO\GG-Exemplo`, "-app", "7zip", "-action", "atualizar")
	if code != exitOK || !strings.HasPrefix(o, "criado: Pedido aceito: atualização de 7-Zip 26.04") || !strings.Contains(e, "job 1 criado") {
		t.Fatalf("pedido: %d %q %s", code, o, e)
	}
	code, o, _ = roda("request", "-machine", "60ef", "-user", `EXEMPLO\ana`, "-groups", `EXEMPLO\GG-Exemplo`, "-app", "7zip", "-action", "atualizar")
	if code != exitRuntime || !strings.HasPrefix(o, "aguarde:") {
		t.Errorf("em aberto: %d %q", code, o)
	}
	ev, _ := mem.AuditEvents(ctx, store.AuditFilter{Action: "autoatendimento."})
	if len(ev) != 2 || ev[1].Action != "autoatendimento.pedido" || ev[1].Source != "autoatendimento (linha de comando)" || ev[1].Username != `EXEMPLO\ana` ||
		!strings.Contains(ev[1].Detail, "visto conectado: não") || ev[0].Action != "autoatendimento.recusado" {
		t.Errorf("auditoria: %+v", ev)
	}
}
