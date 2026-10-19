package userapp

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Aula 18.8: o app percebe a cópia trocada (como o agente faz: renomeia a velha, põe a nova)
// e o caminho sumido; um sumiço passageiro não fecha nada.
func TestRenovacao(t *testing.T) {
	dir := t.TempDir()
	app := filepath.Join(dir, "patchd-app.exe")
	os.WriteFile(app, []byte("v1"), 0o755)
	if _, err := WatchSelf(filepath.Join(dir, "outro.exe")); err == nil {
		t.Error("caminho inexistente: não vigia")
	}
	s, err := WatchSelf(app)
	if err != nil {
		t.Fatal(err)
	}
	if r := s.Check(); r != RenewNone {
		t.Fatalf("o mesmo arquivo: %v", r)
	}
	// Reescrever o mesmo arquivo (mesma identidade) não é troca.
	os.WriteFile(app, []byte("v1 de novo"), 0o755)
	if r := s.Check(); r != RenewNone {
		t.Errorf("mesmo arquivo reescrito: %v", r)
	}
	// O meio da troca: a velha saiu, a nova ainda não entrou.
	os.Rename(app, app+".old-1")
	if r := s.Check(); r != RenewNone {
		t.Errorf("sumiço passageiro: %v", r)
	}
	os.WriteFile(app+".novo", []byte("v2"), 0o755)
	os.Rename(app+".novo", app)
	if r := s.Check(); r != RenewReplaced {
		t.Errorf("cópia nova: %v", r)
	}
	// Sumido de vez (agente removido): fecha na segunda conferência.
	s2, _ := WatchSelf(app)
	os.Remove(app)
	if r := s2.Check(); r != RenewNone {
		t.Errorf("primeira falta: %v", r)
	}
	if r := s2.Check(); r != RenewGone {
		t.Errorf("segunda falta: %v", r)
	}
	// Wait devolve a troca, ou nada quando o contexto acaba.
	os.WriteFile(app, []byte("v3"), 0o755)
	s3, _ := WatchSelf(app)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	if r := s3.Wait(ctx, 10*time.Millisecond); r != RenewNone {
		t.Errorf("sem troca, até o fim do contexto: %v", r)
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		os.Rename(app, app+".old-2")
		os.WriteFile(app, []byte("v4"), 0o755)
	}()
	if r := s3.Wait(context.Background(), 5*time.Millisecond); r != RenewReplaced {
		t.Errorf("Wait: %v", r)
	}
}
