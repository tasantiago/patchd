package jobs

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

// MinTTL é o menor prazo de um job: menos que isso não cobre um check-in com atraso.
const MinTTL = 10 * time.Minute

// ErrMachineUnavailable: a máquina não existe ou está aposentada. Job para ela nunca
// seria entregue, então nem é criado.
var ErrMachineUnavailable = errors.New("máquina inexistente ou aposentada")

// Record é o job como a fila grava: o envelope assinado e as colunas para listar.
type Record struct {
	ID        int64
	MachineID string
	Type      string
	Signed    protocol.SignedJob
	CreatedBy string
	NotBefore time.Time // zero: sem janela (Aula 8.3)
	NotAfter  time.Time
}

// Queue é onde o job assinado é gravado (o PostgreSQL, ou a memória nos testes).
type Queue interface {
	// NextJobID reserva o ID: ele entra no payload assinado antes da gravação.
	NextJobID(ctx context.Context) (int64, error)
	// CreateJob grava o job pendente; devolve ErrMachineUnavailable se a máquina não
	// existe ou está aposentada.
	CreateJob(ctx context.Context, r Record) error
}

// Request é o pedido de um job: quem pediu, para qual máquina, o quê e por quanto tempo.
// Com NotBefore (a janela de manutenção, Aula 8.3), o prazo conta a partir dele.
type Request struct {
	MachineID string
	Type      string
	TTL       time.Duration
	By        string    // usuário do painel, ou "linha de comando"
	NotBefore time.Time // zero: vale já
}

// Issue cria o job: reserva o ID, assina e grava. A linha de comando e o painel passam
// por aqui, com as mesmas regras.
func Issue(ctx context.Context, q Queue, key ed25519.PrivateKey, req Request, now time.Time) (Record, error) {
	if len(key) != ed25519.PrivateKeySize {
		return Record{}, errors.New("sem a chave privada de jobs (PATCHD_JOB_SIGNING_KEY_FILE)")
	}
	if !KnownType(req.Type) {
		return Record{}, fmt.Errorf("tipo de job desconhecido %q", req.Type)
	}
	if req.TTL < MinTTL || req.TTL > MaxTTL {
		return Record{}, fmt.Errorf("prazo do job fora de %v a %v", MinTTL, MaxTTL)
	}
	if req.NotBefore.Sub(now) > MaxTTL {
		return Record{}, fmt.Errorf("início do job a mais de %v daqui", MaxTTL)
	}
	if req.MachineID == "" || req.By == "" {
		return Record{}, errors.New("job sem máquina ou sem autor")
	}
	id, err := q.NextJobID(ctx)
	if err != nil {
		return Record{}, err
	}
	now = now.UTC()
	j := Job{ID: id, MachineID: req.MachineID, Type: req.Type, IssuedAt: now, NotAfter: later(now, req.NotBefore).Add(req.TTL)}
	if !req.NotBefore.IsZero() {
		j.NotBefore = req.NotBefore.UTC()
	}
	env, err := Sign(key, j)
	if err != nil {
		return Record{}, err
	}
	rec := Record{ID: id, MachineID: req.MachineID, Type: req.Type, Signed: env, CreatedBy: req.By, NotBefore: j.NotBefore, NotAfter: j.NotAfter}
	if err := q.CreateJob(ctx, rec); err != nil {
		return Record{}, err
	}
	return rec, nil
}

// KeyID é a impressão curta da chave pública (12 dígitos do SHA-256): aparece no log do
// servidor e do agente para conferir, sem mostrar a chave, que os dois usam o mesmo par.
func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:6])
}
