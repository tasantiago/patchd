package localapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/agent"
	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/pkgcache"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
)

// Do pipe ao job: o canal local com o cliente do agente, contra a API de verdade (memória).
func TestDoCanalAoJob(t *testing.T) {
	m, _ := thirdparty.Default()
	_, priv, _ := jobs.GenerateKey()
	dir := t.TempDir()
	st := store.NewMemory()
	h := api.New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), api.WithJobKey(priv), api.WithContent(dir, nil), api.WithThirdPartyManifest(&m))
	srv := httptest.NewServer(h)
	defer srv.Close()

	ctx := context.Background()
	_, th := identity.NewSecret(identity.EnrollmentPrefix)
	_, _ = st.CreateEnrollmentToken(ctx, th, "teste", time.Now().Add(time.Hour), 1)
	cred, ch := identity.NewSecret(identity.CredentialPrefix)
	id := identity.NewMachineID()
	if _, err := st.Enroll(ctx, th, identity.NewMachine{ID: id, CredentialHash: ch}); err != nil {
		t.Fatal(err)
	}
	_, _ = st.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: "w",
		OS:       protocol.OSInfo{ID: "windows", Name: "Windows 11 Pro", Arch: "amd64"},
		Software: []protocol.Software{{Name: "7-Zip 26.01 (x64)", Version: "26.01", Publisher: "Igor Pavlov", Scope: "machine"}}})
	inst := []byte(strings.Repeat("7z", 100))
	sum := sha256.Sum256(inst)
	sha := hex.EncodeToString(sum[:])
	_ = os.MkdirAll(filepath.Join(dir, "pacotes"), 0o750)
	_ = os.WriteFile(filepath.Join(dir, "pacotes", pkgcache.FileName(sha, false)), inst, 0o640)
	_, _ = st.SaveThirdPartyPackage(ctx, store.ThirdPartyPackage{AppID: "7zip", Version: "26.04", Type: "exe", Architecture: "x64",
		SHA256: sha, Size: int64(len(inst)), Arguments: "/S"})
	_, _ = st.ApproveThirdParty(ctx, store.ThirdPartyApproval{AppID: "7zip", Version: "26.04", ApprovedBy: "thyago", ApprovedAt: time.Now().UTC()})
	_ = st.SetSelfServiceOffer(ctx, store.SelfServiceOffer{AppID: "7zip", CanUpdate: true, Groups: []string{"GG-Exemplo"}, UpdatedBy: "thyago"})

	var acordou atomic.Int32
	s := &Server{Backend: &agent.Agent{Client: agent.NewClient(srv.URL, cred, "v-teste")}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		OnCreated: func() { acordou.Add(1) }}
	noGrupo := tokenDe(`EXEMPLO\teste`, interativo, TokenGroup{SID: "S-1-5-21-1-2-3-2001", Attrs: ligado, Name: `EXEMPLO\GG-Exemplo`})

	// Fora do grupo, a loja vem vazia; no grupo, "atualizar".
	if r, _ := chama(t, s, tokenDe(`EXEMPLO\outro`, interativo), `{"op":"catalog"}`+"\n"); !r.OK || len(r.Catalog.Items) != 0 {
		t.Errorf("fora do grupo: %+v", r)
	}
	r, _ := chama(t, s, noGrupo, `{"op":"catalog"}`+"\n")
	if !r.OK || len(r.Catalog.Items) != 1 || r.Catalog.Items[0].Action != protocol.SelfServiceUpdate {
		t.Fatalf("no grupo: %+v", r)
	}
	r, _ = chama(t, s, noGrupo, `{"op":"request","app":"7zip","action":"atualizar"}`+"\n")
	if !r.OK || r.Result.Status != protocol.SelfServiceCreated || r.Result.JobID == 0 {
		t.Fatalf("pedido: %+v", r)
	}
	j, _, _ := st.JobForMachine(ctx, id, r.Result.JobID)
	if j.Type != jobs.TypeThirdParty || j.CreatedBy != `autoatendimento: EXEMPLO\teste` {
		t.Errorf("job: %+v", j)
	}
	if acordou.Load() != 1 {
		t.Error("o pedido criado antecipa o check-in (é o que leva o job à máquina)")
	}
	// O mesmo pedido de novo: o servidor diz "aguarde".
	if r, _ := chama(t, s, noGrupo, `{"op":"request","app":"7zip","action":"atualizar"}`+"\n"); !r.OK || r.Result.Status != protocol.SelfServiceWait {
		t.Errorf("de novo: %+v", r)
	}

	// Autoatendimento desligado no servidor (sem as regras de terceiros): 503 com o código.
	h2 := api.New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), api.WithJobKey(priv))
	srv2 := httptest.NewServer(h2)
	defer srv2.Close()
	s2 := &Server{Backend: agent.NewClient(srv2.URL, cred, "v-teste"), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if r, _ := chama(t, s2, noGrupo, `{"op":"catalog"}`+"\n"); r.OK || r.Code != CodeOff {
		t.Errorf("desligado: %+v", r)
	}
}
