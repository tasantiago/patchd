package credential

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestSaveLoad(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "patchd")
	if _, err := Load(dir); !errors.Is(err, ErrNotEnrolled) {
		t.Fatalf("sem arquivo: ErrNotEnrolled, veio %v", err)
	}
	c := Credential{MachineID: "baceb0cd-67c5-4a58-90cd-245b7e5fb400", Credential: "patchd_mac_abc",
		ServerURL: "http://127.0.0.1:18080", EnrolledAt: time.Date(2026, 10, 4, 11, 0, 0, 0, time.UTC)}
	if err := Save(dir, c); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil || got != c {
		t.Fatalf("Load = %+v, %v", got, err)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(Path(dir))
		dst, _ := os.Stat(dir)
		if st.Mode().Perm() != 0o600 || dst.Mode().Perm() != 0o700 {
			t.Errorf("permissões: arquivo %v, pasta %v", st.Mode().Perm(), dst.Mode().Perm())
		}
	}
	sobras, _ := filepath.Glob(filepath.Join(dir, ".credential-*"))
	if len(sobras) != 0 {
		t.Errorf("nenhum temporário deve sobrar: %v", sobras)
	}
}

func TestLoadIncompleto(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(Path(dir), []byte(`{"machine_id":"x"}`), 0o600)
	if _, err := Load(dir); err == nil {
		t.Error("credencial sem segredo deveria ser recusada")
	}
}

func TestCheckWritable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nova")
	if err := CheckWritable(dir); err != nil {
		t.Fatal(err)
	}
	if sobras, _ := filepath.Glob(filepath.Join(dir, ".write-check-*")); len(sobras) != 0 {
		t.Errorf("o teste de escrita não deve deixar arquivo: %v", sobras)
	}
}
