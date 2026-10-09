package jobs

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

func TestAssinarEConferir(t *testing.T) {
	pub, priv, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	agora := time.Date(2026, 10, 8, 20, 0, 0, 0, time.UTC)
	j := Job{ID: 7, MachineID: "60ef97e4", Type: TypeRescan, IssuedAt: agora, NotAfter: agora.Add(24 * time.Hour)}
	env, err := Sign(priv, j)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Verify(pub, env, "60ef97e4", agora.Add(time.Hour), 6)
	if err != nil || got.ID != 7 || got.Type != TypeRescan {
		t.Fatalf("job válido: %+v %v", got, err)
	}
	if ID(env) != 7 {
		t.Error("ID")
	}

	// Cada forma de recusa.
	outro, _, _ := GenerateKey()
	alterado := env
	alterado.Payload = strings.Replace(env.Payload, `"rescan"`, `"rescan" `, 1)
	cruzado, _ := Sign(priv, Job{ID: 8, MachineID: "92fc3b93", Type: TypeRescan, IssuedAt: agora, NotAfter: agora.Add(time.Hour)})
	for nome, c := range map[string]struct {
		pub    []byte
		env    protocol.SignedJob
		maq    string
		agora  time.Time
		ultimo int64
		msg    string
	}{
		"sem chave":           {nil, env, "60ef97e4", agora, 0, "sem chave"},
		"outra chave":         {outro, env, "60ef97e4", agora, 0, "assinatura inválida"},
		"alterado":            {pub, alterado, "60ef97e4", agora, 0, "assinatura inválida"},
		"assinatura ilegível": {pub, protocol.SignedJob{Payload: env.Payload, Signature: "!!"}, "60ef97e4", agora, 0, "assinatura inválida"},
		"outra máquina":       {pub, cruzado, "60ef97e4", agora, 0, "de outra máquina"},
		"vencido":             {pub, env, "60ef97e4", agora.Add(24*time.Hour + ClockSkew + time.Second), 0, "vencido"},
		"do futuro":           {pub, env, "60ef97e4", agora.Add(-ClockSkew - time.Second), 0, "no futuro"},
		"já visto":            {pub, env, "60ef97e4", agora, 7, "já visto"},
	} {
		_, err := Verify(c.pub, c.env, c.maq, c.agora, c.ultimo)
		if err == nil || !strings.Contains(err.Error(), c.msg) {
			t.Errorf("%s: %v", nome, err)
		}
	}
	// Dentro da tolerância de relógio, aceita.
	if _, err := Verify(pub, env, "60ef97e4", agora.Add(24*time.Hour+ClockSkew-time.Second), 0); err != nil {
		t.Errorf("tolerância: %v", err)
	}
}

func TestTipoDesconhecidoECampoNovo(t *testing.T) {
	pub, priv, _ := GenerateKey()
	agora := time.Now().UTC()
	// Um servidor mais novo assina um tipo que este agente não conhece: recusa.
	payload, _ := json.Marshal(Job{ID: 1, MachineID: "m", Type: "install_updates", IssuedAt: agora, NotAfter: agora.Add(time.Hour)})
	env := protocol.SignedJob{Payload: string(payload), Signature: signRaw(priv, payload)}
	if _, err := Verify(pub, env, "m", agora, 0); err == nil || !strings.Contains(err.Error(), "tipo desconhecido") {
		t.Errorf("tipo: %v", err)
	}
	// Campo que o agente não conhece num job assinado: recusa (não executa pela metade).
	payload = []byte(`{"id":2,"machine_id":"m","type":"rescan","issued_at":"` + agora.Format(time.RFC3339) + `","not_after":"` +
		agora.Add(time.Hour).Format(time.RFC3339) + `","command":"rm -rf /"}`)
	env = protocol.SignedJob{Payload: string(payload), Signature: signRaw(priv, payload)}
	if _, err := Verify(pub, env, "m", agora, 0); err == nil || !strings.Contains(err.Error(), "ilegível") {
		t.Errorf("campo desconhecido: %v", err)
	}
	// Sign recusa job inválido.
	for _, j := range []Job{
		{ID: 0, MachineID: "m", Type: TypeRescan, IssuedAt: agora, NotAfter: agora.Add(time.Hour)},
		{ID: 1, MachineID: "m", Type: "shell", IssuedAt: agora, NotAfter: agora.Add(time.Hour)},
		{ID: 1, MachineID: "m", Type: TypeRescan, IssuedAt: agora, NotAfter: agora.Add(MaxTTL + time.Hour)},
		{ID: 1, MachineID: "m", Type: TypeRescan, IssuedAt: agora, NotAfter: agora},
	} {
		if _, err := Sign(priv, j); err == nil {
			t.Errorf("deveria recusar %+v", j)
		}
	}
}

func signRaw(priv []byte, payload []byte) string {
	env, _ := signBytes(priv, payload)
	return env
}

func TestChaves(t *testing.T) {
	pub, priv, _ := GenerateKey()
	txt := EncodePrivateKey(priv)
	if !strings.HasPrefix(txt, KeyPrefix) {
		t.Error("prefixo")
	}
	back, err := ParsePrivateKey(txt)
	if err != nil || !back.Equal(priv) {
		t.Errorf("privada: %v", err)
	}
	if p, err := ParsePublicKey(EncodePublicKey(pub)); err != nil || !p.Equal(pub) {
		t.Errorf("pública: %v", err)
	}
	for _, ruim := range []string{"", "patchd_relkey_abc", KeyPrefix + "curta"} {
		if _, err := ParsePrivateKey(ruim); err == nil {
			t.Errorf("%q", ruim)
		}
	}
	if _, err := ParsePublicKey("abc"); err == nil {
		t.Error("pública curta")
	}
	if !errors.Is(ErrNoKey, ErrNoKey) || Final(StatePending) || Final(StateDelivered) || !Final(StateDone) {
		t.Error("estados")
	}
}
