// Package jobs define os jobs que o servidor manda o agente executar (Módulo 8): os tipos
// fechados (RF-08), os estados (RF-25) e a assinatura Ed25519 de cada job (RNF-05).
//
// Quem assina é o servidor, com uma chave própria (decisão da Aula 8.1), separada da chave
// de release, que continua fora do servidor. O job é assinado na criação e guardado
// assinado no banco: quem altera uma linha da tabela não consegue fazer o agente aceitar
// o job alterado. O agente confere com a chave pública embutida no executável.
package jobs

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

// PublicKey é a chave pública dos jobs, em base64, injetada no build com
//
//	-ldflags "-X github.com/tasantiago/patchd/internal/jobs.PublicKey=..."
//
// Vazia (build sem PATCHD_JOB_PUBKEY): o agente recusa todo job, e diz por quê.
var PublicKey = ""

// Tipos de job (RF-08). A lista é fechada: o agente recusa o que não conhece, e nenhum
// tipo leva comando livre.
const (
	TypeRescan = "rescan" // coleta o inventário e faz a busca de atualizações agora
)

// KnownType diz se o tipo existe.
func KnownType(t string) bool { return t == TypeRescan }

// Estados (RF-25). Vida de um job: pendente → entregue → concluido | falhou | rejeitado;
// pendente ou entregue → cancelado (pelo operador) | expirado (passou de NotAfter).
const (
	StatePending   = "pendente"
	StateDelivered = "entregue"
	StateDone      = "concluido"
	StateFailed    = "falhou"
	StateRejected  = "rejeitado" // o agente recusou: assinatura, máquina, prazo, tipo
	StateCanceled  = "cancelado"
	StateExpired   = "expirado"
)

// Final diz se o estado não muda mais.
func Final(s string) bool {
	return s != StatePending && s != StateDelivered
}

// Limites.
const (
	MaxTTL     = 7 * 24 * time.Hour // um job velho demais descreve uma decisão que já não vale
	ClockSkew  = 5 * time.Minute    // tolerância entre o relógio do servidor e o da máquina
	KeyPrefix  = "patchd_jobkey_"
	maxPayload = 16 << 10
)

// signingContext entra na mensagem assinada: uma assinatura feita com esta chave para
// outra finalidade nunca vale como job.
const signingContext = "patchd-job-v1\n"

// Job é o que o agente recebe e executa. É serializado uma vez, na criação; os bytes
// exatos são assinados e guardados.
type Job struct {
	ID        int64           `json:"id"`
	MachineID string          `json:"machine_id"`
	Type      string          `json:"type"`
	Params    json.RawMessage `json:"params,omitempty"`
	IssuedAt  time.Time       `json:"issued_at"`
	// NotBefore (Aula 8.3): o início da janela de manutenção. Zero = já pode rodar, e o
	// campo nem vai ao payload (omitzero): agentes da 8.1 e da 8.2 continuam aceitando os
	// jobs sem janela, e recusam os com janela (campo desconhecido), que é o seguro.
	NotBefore time.Time `json:"not_before,omitzero"`
	NotAfter  time.Time `json:"not_after"`
}

// Sign serializa e assina o job, e devolve o envelope que vai para o banco e para o agente.
func Sign(priv ed25519.PrivateKey, j Job) (protocol.SignedJob, error) {
	if j.ID <= 0 || j.MachineID == "" || !KnownType(j.Type) || !j.NotAfter.After(j.IssuedAt) || !j.NotAfter.After(j.NotBefore) ||
		j.NotAfter.Sub(later(j.IssuedAt, j.NotBefore)) > MaxTTL || j.NotBefore.Sub(j.IssuedAt) > MaxTTL {
		return protocol.SignedJob{}, fmt.Errorf("job inválido: %+v", j)
	}
	payload, err := json.Marshal(j)
	if err != nil {
		return protocol.SignedJob{}, err
	}
	sig := ed25519.Sign(priv, append([]byte(signingContext), payload...))
	return protocol.SignedJob{Payload: string(payload), Signature: base64.StdEncoding.EncodeToString(sig)}, nil
}

// Erros da conferência. O motivo vai ao servidor no resultado "rejeitado".
var (
	ErrNoKey     = errors.New("o agente foi compilado sem chave de jobs (PATCHD_JOB_PUBKEY)")
	ErrSignature = errors.New("assinatura inválida (outra chave ou job alterado)")
)

// Verify confere o envelope com a chave pública e só então interpreta o job. Recusa:
// assinatura inválida, job de outra máquina, fora do prazo, tipo desconhecido, e job já
// visto (ID menor ou igual a lastID: um job copiado de uma resposta antiga não roda de novo).
func Verify(pub ed25519.PublicKey, env protocol.SignedJob, machineID string, now time.Time, lastID int64) (Job, error) {
	var j Job
	if len(pub) != ed25519.PublicKeySize {
		return j, ErrNoKey
	}
	if len(env.Payload) > maxPayload {
		return j, errors.New("job grande demais")
	}
	sig, err := base64.StdEncoding.DecodeString(env.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize || !ed25519.Verify(pub, append([]byte(signingContext), env.Payload...), sig) {
		return j, ErrSignature
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(env.Payload)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&j); err != nil {
		return j, fmt.Errorf("job assinado, mas ilegível: %w", err)
	}
	switch {
	case j.MachineID != machineID:
		return j, fmt.Errorf("job %d é de outra máquina", j.ID)
	case !KnownType(j.Type):
		return j, fmt.Errorf("job %d de tipo desconhecido %q (agente antigo?)", j.ID, j.Type)
	case now.After(j.NotAfter.Add(ClockSkew)):
		return j, fmt.Errorf("job %d vencido em %s", j.ID, j.NotAfter.UTC().Format(time.RFC3339))
	case now.Before(j.IssuedAt.Add(-ClockSkew)):
		return j, fmt.Errorf("job %d emitido no futuro (relógio desta máquina atrasado?)", j.ID)
	case !j.NotBefore.IsZero() && now.Before(j.NotBefore.Add(-ClockSkew)):
		return j, fmt.Errorf("job %d fora da janela: só vale a partir de %s", j.ID, j.NotBefore.UTC().Format(time.RFC3339))
	case j.ID <= lastID:
		return j, fmt.Errorf("job %d já visto (o último aceito foi %d)", j.ID, lastID)
	}
	return j, nil
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// ID lê só o ID do envelope, sem conferir nada: serve para o agente dizer ao servidor qual
// job recusou quando a assinatura não confere.
func ID(env protocol.SignedJob) int64 {
	var j struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal([]byte(env.Payload), &j)
	return j.ID
}

// GenerateKey cria o par de chaves dos jobs.
func GenerateKey() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return ed25519.GenerateKey(rand.Reader)
}

// EncodePrivateKey: a semente de 32 bytes em base64 URL, com o prefixo patchd_jobkey_.
func EncodePrivateKey(priv ed25519.PrivateKey) string {
	return KeyPrefix + base64.RawURLEncoding.EncodeToString(priv.Seed()) + "\n"
}

// ParsePrivateKey lê o arquivo da chave privada.
func ParsePrivateKey(s string) (ed25519.PrivateKey, error) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(s), KeyPrefix)
	seed, err := base64.RawURLEncoding.DecodeString(rest)
	if !ok || err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("arquivo de chave de jobs inválido (esperado patchd_jobkey_...)")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// EncodePublicKey é o formato do PATCHD_JOB_PUBKEY.
func EncodePublicKey(pub ed25519.PublicKey) string { return base64.StdEncoding.EncodeToString(pub) }

// ParsePublicKey lê a chave pública em base64.
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, errors.New("chave pública de jobs inválida (esperado base64 de 32 bytes)")
	}
	return ed25519.PublicKey(raw), nil
}
