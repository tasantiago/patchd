// Package wakereq monta e grava o pedido de ligar pela rede (docs/wake-on-lan.md): um job
// wake_on_lan para cada repetidor da mesma sub-rede da máquina. Usado pelo botão do painel,
// pelo reinício em horário marcado e pelas campanhas que ligam as máquinas na janela.
package wakereq

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/wake"
)

const (
	// TTL: o prazo dos jobs dos repetidores (o pacote sai no próximo check-in de um deles).
	TTL = 2 * time.Hour
	// ScheduledTTL: o prazo dos jobs de um pacote marcado para uma hora (a partir dela).
	ScheduledTTL = 30 * time.Minute
	// Retry: com o pacote mandado e sem resposta, dá para pedir de novo depois disso.
	Retry = 10 * time.Minute
)

// ErrNotFound: a máquina não tem estado (nunca fez check-in): não há rede para escolher.
var ErrNotFound = errors.New("máquina sem estado")

// Store é o que o pedido usa do banco.
type Store interface {
	jobs.Queue
	WakeRequests(ctx context.Context, machineID string, limit int) ([]store.WakeRequest, error)
	HostState(ctx context.Context, id string) (store.HostState, bool, error)
	LatestInventory(ctx context.Context, machineID string) (protocol.InventoryReport, bool, error)
	WakeRelayHosts(ctx context.Context, exclude string, since time.Time) ([]store.WakeHost, error)
	CreateWakeRequest(ctx context.Context, r store.WakeRequest) (int64, error)
}

// Result: o pedido gravado, ou por que não saiu (Problem, no formato da volta do painel:
// "ligar-erro=repetidor&rede=...").
type Result struct {
	Problem    string
	ID         int64
	Request    store.WakeRequest
	RelayNames []string
}

// Pending: o pedido mais novo ainda está correndo (sem pacote dentro do prazo do job, ou com
// pacote há menos de Retry e sem resposta).
func Pending(list []store.WakeRequest, now time.Time) *store.WakeRequest {
	if len(list) == 0 {
		return nil
	}
	r := list[0]
	switch {
	case r.AnsweredAt != nil:
		return nil
	case r.SentAt == nil && now.Sub(r.RequestedAt) < TTL:
		return &r
	case r.SentAt != nil && now.Sub(*r.SentAt) < Retry:
		return &r
	}
	return nil
}

// Request monta e grava o pedido de ligar a máquina id. at: o horário do pacote (zero ou no
// passado: agora); os jobs só são entregues a partir dele. by: quem pediu (o usuário, ou
// "campanha N").
func Request(ctx context.Context, st Store, key ed25519.PrivateKey, id string, at, now time.Time, by string) (Result, error) {
	list, err := st.WakeRequests(ctx, id, 1)
	if err != nil {
		return Result{}, err
	}
	if Pending(list, now) != nil {
		return Result{Problem: "ligar-erro=andamento"}, nil
	}
	h, found, err := st.HostState(ctx, id)
	if err != nil {
		return Result{}, err
	}
	if !found {
		return Result{}, ErrNotFound
	}
	var hw *protocol.Wake
	if inv, ok, err := st.LatestInventory(ctx, id); err != nil {
		return Result{}, err
	} else if ok && inv.Hardware != nil {
		hw = inv.Hardware.Wake
	}
	cands, err := st.WakeRelayHosts(ctx, id, now.Add(-wake.RelayFresh))
	if err != nil {
		return Result{}, err
	}
	hosts := make([]wake.Host, 0, len(cands))
	for _, c := range cands {
		hosts = append(hosts, wake.Host{ID: c.ID, Name: c.Name, LastSeen: c.LastSeen, State: c.State})
	}
	plan, err := wake.Build(hw, h.State, hosts, now)
	var noRelay wake.NoRelayError
	switch {
	case errors.Is(err, wake.ErrNoMAC):
		return Result{Problem: "ligar-erro=mac"}, nil
	case errors.Is(err, wake.ErrNoNetwork):
		return Result{Problem: "ligar-erro=rede"}, nil
	case errors.As(err, &noRelay):
		return Result{Problem: "ligar-erro=repetidor&rede=" + url.QueryEscape(fmt.Sprint(noRelay.Networks))}, nil
	case err != nil:
		return Result{}, err
	}
	params, err := jobs.WakeOnLANParamsJSON(jobs.WakeOnLANParams{Target: id, MACs: plan.MACs, Network: plan.Network.String()})
	if err != nil {
		return Result{}, err
	}
	requested := now
	if at.After(now) {
		requested = at
	}
	req := store.WakeRequest{MachineID: id, RequestedAt: requested, RequestedBy: by, Network: plan.Network.String(), MACs: plan.MACs}
	var names []string
	for _, relay := range plan.Relays {
		jr := jobs.Request{MachineID: relay.ID, Type: jobs.TypeWakeOnLAN, TTL: TTL, By: by, Params: params}
		if at.After(now) {
			jr.NotBefore, jr.TTL = at, ScheduledTTL
		}
		rec, err := jobs.Issue(ctx, st, key, jr, now)
		if errors.Is(err, jobs.ErrMachineUnavailable) {
			continue // aposentada entre a lista e agora
		}
		if err != nil {
			return Result{}, err
		}
		req.Relays = append(req.Relays, relay.ID)
		req.JobIDs = append(req.JobIDs, rec.ID)
		names = append(names, relay.Name)
	}
	if len(req.JobIDs) == 0 {
		return Result{Problem: "ligar-erro=repetidor"}, nil
	}
	rid, err := st.CreateWakeRequest(ctx, req)
	if err != nil {
		return Result{}, err
	}
	return Result{ID: rid, Request: req, RelayNames: names}, nil
}

// ProblemText: o motivo de um pedido que não saiu, em texto ("" sem problema).
func ProblemText(problem string) string {
	q, _ := url.ParseQuery(problem)
	switch q.Get("ligar-erro") {
	case "":
		return ""
	case "andamento":
		return "já havia um pedido de ligar em andamento"
	case "mac":
		return wake.ErrNoMAC.Error()
	case "rede":
		return wake.ErrNoNetwork.Error()
	case "repetidor":
		return "nenhum repetidor ligado na sub-rede " + q.Get("rede")
	}
	return problem
}
