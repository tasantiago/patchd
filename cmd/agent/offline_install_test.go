package main

import (
	"context"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
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
