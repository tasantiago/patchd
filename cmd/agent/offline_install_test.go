package main

import (
	"context"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/credential"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/store"
)

func TestOfflineImageEnrollment(t *testing.T) {
	oldTransport := enrollTransport
	t.Cleanup(func() { enrollTransport = oldTransport })
	st := store.NewMemory()
	ctx := context.Background()
	token, hash := identity.NewSecret(identity.EnrollmentPrefix)
	tokenID, err := st.CreateEnrollmentToken(ctx, hash, "ISO reutilizável", time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(api.New(st, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer server.Close()
	dir := t.TempDir()
	ca := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "token.txt")
	os.WriteFile(file, []byte(token), 0600)
	cfg := agentConfig{ServerURL: server.URL, DataDir: dir, EnrollTokenFile: file, CAFile: ca, OfflineInstall: true, ImageInstall: true}
	args, err := prepareOfflineInstall(cfg, []string{"-offline", "-image", "-ca-file", ca}, ambienteFalso(nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(args) == 0 {
		t.Fatal("service arguments missing")
	}
	if _, err := os.Stat(credential.Path(dir)); !os.IsNotExist(err) {
		t.Fatal("image created an identity")
	}
	if _, err := offlineAttempt(ctx, dir, "v-test"); err == nil {
		t.Fatal("image registered before activation")
	}
	if machines, _ := st.Machines(ctx); len(machines) != 0 {
		t.Fatal("reference image enrolled")
	}
	p, _ := readOffline(dir)
	p.HoldImage = false
	if err := saveOffline(dir, p); err != nil {
		t.Fatal(err)
	}
	os.Remove(ca)
	os.Remove(file) // Installation media is no longer needed.
	c, err := offlineAttempt(ctx, dir, "v-test")
	if err != nil {
		t.Fatal(err)
	}
	again, err := offlineAttempt(ctx, dir, "v-test")
	if err != nil || again.MachineID != c.MachineID {
		t.Fatal("duplicate registration", err)
	}
	if _, err := os.Stat(filepath.Join(dir, offlineFile)); !os.IsNotExist(err) {
		t.Fatal("bootstrap token not removed")
	}
	if _, err := prepareOfflineInstall(cfg, nil, ambienteFalso(nil)); err == nil {
		t.Fatal("accepted a registered golden image")
	}
	// The same token can enroll another image, but remains revocable.
	dir2 := t.TempDir()
	p.HoldImage = false
	if err := saveOffline(dir2, p); err != nil {
		t.Fatal(err)
	}
	other, err := offlineAttempt(ctx, dir2, "v-test")
	if err != nil || other.MachineID == c.MachineID {
		t.Fatal("clones shared an identity", err)
	}
	st.RevokeEnrollmentToken(ctx, tokenID)
	dir3 := t.TempDir()
	saveOffline(dir3, p)
	if _, err := offlineAttempt(ctx, dir3, "v-test"); err == nil {
		t.Fatal("revoked unlimited token accepted")
	}
}

func TestOfflineWaitCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dir := t.TempDir()
	if err := saveOffline(dir, offlineEnrollment{ServerURL: "https://unreachable.invalid", Token: "patchd_enr_test", HoldImage: true}); err != nil {
		t.Fatal(err)
	}
	_, err := waitOfflineEnrollment(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), dir, "test")
	if err != context.Canceled {
		t.Fatalf("shutdown ignored: %v", err)
	}
}

// Instalação offline com -relay: a configuração guarda o relé; o registro adiado tenta o
// servidor direto e, sem resposta, o relé; o relé fica no estado para os check-ins.
func TestOfflineComRele(t *testing.T) {
	oldTransport, oldRelay := enrollTransport, enrollRelay
	t.Cleanup(func() { enrollTransport, enrollRelay = oldTransport, oldRelay })
	look := ambienteFalso(nil)
	if _, err := loadAgentConfig([]string{"-offline", "-relay", "203.0.113.10:8443"}, look, io.Discard); err != nil {
		t.Fatalf("-offline com -relay recusado: %v", err)
	}
	st := store.NewMemory()
	ctx := context.Background()
	token, hash := identity.NewSecret(identity.EnrollmentPrefix)
	if _, err := st.CreateEnrollmentToken(ctx, hash, "lote", time.Time{}, 0); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(api.New(st, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer server.Close()
	dir := t.TempDir()
	ca := filepath.Join(t.TempDir(), "ca.crt")
	os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600)
	file := filepath.Join(t.TempDir(), "token.txt")
	os.WriteFile(file, []byte(token), 0600)
	cfg := agentConfig{ServerURL: server.URL, DataDir: dir, EnrollTokenFile: file, CAFile: ca, OfflineInstall: true, Relay: "203.0.113.10:8443"}
	args, err := prepareOfflineInstall(cfg, []string{"-offline", "-relay", "203.0.113.10:8443", "-ca-file", ca}, look)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range args {
		if a == "-relay" || a == "203.0.113.10:8443" {
			t.Errorf("o relé foi para a linha do serviço: %v", args)
		}
	}
	if p, err := readOffline(dir); err != nil || p.Relay != "203.0.113.10:8443" {
		t.Fatalf("configuração offline sem o relé: %+v %v", p, err)
	}
	// Rodar de novo com outro relé (ou outro token, o que venceu antes de o notebook conectar):
	// a instalação individual ainda não registrada aceita; outro servidor, não.
	other := cfg
	other.Relay = "203.0.113.11:8443"
	if _, err := prepareOfflineInstall(other, nil, look); err != nil {
		t.Errorf("recusou o relé novo: %v", err)
	}
	if p, _ := readOffline(dir); p.Relay != "203.0.113.11:8443" {
		t.Errorf("o relé novo não ficou: %+v", p)
	}
	if _, err := prepareOfflineInstall(cfg, nil, look); err != nil {
		t.Fatal(err)
	}
	outroServidor := cfg
	outroServidor.ServerURL = "https://outro.exemplo:8443"
	if _, err := prepareOfflineInstall(outroServidor, nil, look); err == nil {
		t.Error("aceitou outro servidor")
	}
	// O servidor responde direto: registra direto, e o relé fica no estado.
	c, err := offlineAttempt(ctx, dir, "v-test")
	if err != nil || c.MachineID == "" {
		t.Fatalf("registro: %v", err)
	}
	if enrollRelay != "203.0.113.10:8443" {
		t.Errorf("o registro adiado não usou o relé: %q", enrollRelay)
	}
	b, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil || !strings.Contains(string(b), "203.0.113.10:8443") {
		t.Errorf("o relé não ficou no estado: %s %v", b, err)
	}
}
