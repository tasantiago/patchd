package jobs

import (
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestVerifyAny (Aula 20.7, S8): na troca, o agente aceita a chave atual e a seguinte. Uma
// recusa que não é de assinatura (outra máquina, vencido) vale para qualquer chave e não
// vira "assinatura inválida" por causa da chave seguinte.
func TestVerifyAny(t *testing.T) {
	atual, privAtual, _ := GenerateKey()
	seguinte, privSeguinte, _ := GenerateKey()
	_, privOutra, _ := GenerateKey()
	agora := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	job := Job{ID: 9, MachineID: "60ef97e4", Type: TypeRescan, IssuedAt: agora, NotAfter: agora.Add(time.Hour)}
	chaves := []ed25519.PublicKey{atual, seguinte}

	for nome, priv := range map[string]ed25519.PrivateKey{"atual": privAtual, "seguinte": privSeguinte} {
		env, _ := Sign(priv, job)
		if j, err := VerifyAny(chaves, env, "60ef97e4", agora, 8); err != nil || j.ID != 9 {
			t.Errorf("%s: %+v %v", nome, j, err)
		}
	}
	envOutra, _ := Sign(privOutra, job)
	if _, err := VerifyAny(chaves, envOutra, "60ef97e4", agora, 8); !errors.Is(err, ErrSignature) {
		t.Errorf("outra chave: %v", err)
	}
	// Assinado pela atual, mas para outra máquina: a recusa é essa, não "assinatura inválida"
	// da tentativa com a seguinte.
	envAtual, _ := Sign(privAtual, job)
	if _, err := VerifyAny(chaves, envAtual, "92fc3b93", agora, 8); err == nil || errors.Is(err, ErrSignature) || !strings.Contains(err.Error(), "máquina") {
		t.Errorf("outra máquina: %v", err)
	}
	// A chave seguinte vazia (nil) é ignorada; nenhuma chave é "sem chave".
	if _, err := VerifyAny([]ed25519.PublicKey{atual, nil}, envAtual, "60ef97e4", agora, 8); err != nil {
		t.Errorf("seguinte nil: %v", err)
	}
	if _, err := VerifyAny(nil, envAtual, "60ef97e4", agora, 8); !errors.Is(err, ErrNoKey) {
		t.Errorf("sem chaves: %v", err)
	}
}
