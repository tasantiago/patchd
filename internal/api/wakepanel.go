package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/wake"
)

// O cartão "Ligar pela rede" na aba Hardware (Aula 22.1): a decisão do internal/wake e o
// que pesou nela, para conferir no laboratório.

type wakeCard struct {
	State, Level string
	Blockers     []string
	Notes        []string
	Adapters     []wakeAdapterRow
	Settings     []protocol.WakeSetting
	FastStartup  string
	BitLocker    string
}

type wakeAdapterRow struct {
	Name, MAC, Wired, Magic, DeviceWake string
}

func wakeView(hw protocol.Hardware) *wakeCard {
	if hw.Wake == nil {
		return nil
	}
	w := hw.Wake
	v := wake.Decide(w, wake.LaptopChassis(hw.ChassisType))
	c := &wakeCard{State: v.State, Blockers: v.Blockers, Notes: v.Notes, Settings: w.Settings}
	switch v.State {
	case wake.Ready:
		c.Level = "ok"
	case wake.Blocked:
		c.Level = "erro"
	default:
		c.Level = "atencao"
	}
	yesNo := func(p *bool, yes, no string) string {
		switch {
		case p == nil:
			return "—"
		case *p:
			return yes
		}
		return no
	}
	magic := map[string]string{protocol.WakeOn: "ligado", protocol.WakeOff: "desligado", protocol.WakeUnsupported: "sem suporte", "": "—"}
	for _, a := range w.Adapters {
		wired := "não (Wi-Fi ou outra)"
		if a.Wired {
			wired = "sim"
		}
		c.Adapters = append(c.Adapters, wakeAdapterRow{Name: a.Name, MAC: dash(a.MAC), Wired: wired, Magic: magic[a.MagicPacket],
			DeviceWake: yesNo(a.DeviceWake, "sim", "não")})
	}
	c.FastStartup = yesNo(w.FastStartup, "ligado", "desligado")
	c.BitLocker = yesNo(w.BitLockerPIN, "pede PIN na partida", "sem PIN na partida")
	return c
}

// --- Ligar agora (Aula 22.2) ------------------------------------------------------------

// panelWakeTTL: o job do repetidor vale 2 horas (ele recebe no próximo check-in, de hora em
// hora; depois disso a decisão de ligar já é outra).
const panelWakeTTL = 2 * time.Hour

// scheduledWakeTTL: o pacote agendado (reinício em horário marcado) vale meia hora a partir
// do horário: depois disso, o reinício já passou da hora de ligar.
const scheduledWakeTTL = 30 * time.Minute

// wakeRetry: um pedido com o pacote mandado e sem resposta pode ser repetido depois disso.
const wakeRetry = 10 * time.Minute

// wakeShown: os pedidos mostrados no cartão.
const wakeShown = 5

type wakeAction struct {
	CanRequest bool   // admin, com a chave de jobs
	Pending    string // um pedido em andamento (o botão some)
	MACs       string
	Requests   []wakeRequestRow
}

type wakeRequestRow struct {
	When, By, Network, Relays, Sent, Answer, Level string
}

// wakePending: o pedido mais novo ainda está correndo (sem pacote dentro do prazo do job, ou
// com pacote há menos de wakeRetry e sem resposta).
func wakePending(list []store.WakeRequest, now time.Time) *store.WakeRequest {
	if len(list) == 0 {
		return nil
	}
	r := list[0]
	switch {
	case r.AnsweredAt != nil:
		return nil
	case r.SentAt == nil && now.Sub(r.RequestedAt) < panelWakeTTL:
		return &r
	case r.SentAt != nil && now.Sub(*r.SentAt) < wakeRetry:
		return &r
	}
	return nil
}

func (a *api) wakeActionView(ctx context.Context, id string, hw *protocol.Hardware, st *protocol.MachineState, admin bool) (*wakeAction, error) {
	list, err := a.store.WakeRequests(ctx, id, wakeShown)
	if err != nil {
		return nil, err
	}
	var w *protocol.Wake
	if hw != nil {
		w = hw.Wake
	}
	macs := wake.MACs(w, st)
	v := &wakeAction{CanRequest: admin && a.jobKey != nil && len(macs) > 0, MACs: strings.Join(macs, ", ")}
	now := a.now()
	if p := wakePending(list, now); p != nil {
		v.Pending = "Pedido de " + a.stamp(p.RequestedAt) + " em andamento"
		if p.SentAt != nil {
			v.Pending += ": pacote enviado " + ago(now.Sub(*p.SentAt)) + ", esperando o check-in da máquina"
		} else {
			v.Pending += ": esperando o check-in de um repetidor (até " + a.checkinWait() + ")"
		}
		v.CanRequest = false
	}
	names, err := a.machineNames(ctx)
	if err != nil {
		return nil, err
	}
	name := func(id string) string {
		if n := names[id]; n != "" {
			return n
		}
		return short(id)
	}
	for _, r := range list {
		row := wakeRequestRow{When: a.stamp(r.RequestedAt), By: r.RequestedBy, Network: r.Network}
		var rel []string
		for _, x := range r.Relays {
			rel = append(rel, name(x))
		}
		row.Relays = strings.Join(rel, ", ")
		switch {
		case r.SentAt != nil:
			row.Sent = a.stamp(*r.SentAt) + " por " + name(r.SentBy)
		case now.Sub(r.RequestedAt) < panelWakeTTL:
			row.Sent = "esperando um repetidor"
		default:
			row.Sent, row.Level = "nenhum repetidor mandou no prazo", "atencao"
		}
		switch {
		case r.AnsweredAt != nil && r.SentAt != nil:
			row.Answer, row.Level = "respondeu "+lapse(r.AnsweredAt.Sub(*r.SentAt))+" depois do pacote", "ok"
		case r.SentAt != nil && now.Sub(*r.SentAt) > wakeRetry:
			row.Answer, row.Level = "não respondeu", "erro"
		case r.SentAt != nil:
			row.Answer = "esperando o check-in"
		default:
			row.Answer = "—"
		}
		v.Requests = append(v.Requests, row)
	}
	return v, nil
}

func wakeBack(id, query string) string { return machinePath(id) + "?aba=hardware&" + query + "#ligar" }

// wakeAction: POST /painel/maquinas/{id}/ligar. Monta o pedido (MACs, sub-rede, até 3
// repetidores da mesma sub-rede com check-in recente) e cria um job assinado para cada um.
func (a *api) wakeOnLANAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	id := r.PathValue("id")
	if !machineIDPattern.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	if a.jobKey == nil {
		http.Redirect(w, r, wakeBack(id, "ligar-erro=sem-chave"), http.StatusSeeOther)
		return
	}
	res, err := a.requestWake(r, s, id, time.Time{})
	switch {
	case errors.Is(err, errWakeNotFound):
		http.NotFound(w, r)
	case err != nil:
		a.internalError(w, r, "montar o pedido de ligar", err)
	case res.problem != "":
		http.Redirect(w, r, wakeBack(id, res.problem), http.StatusSeeOther)
	default:
		http.Redirect(w, r, wakeBack(id, fmt.Sprintf("ligar=pedido&n=%d", res.jobs)), http.StatusSeeOther)
	}
}

var errWakeNotFound = errors.New("máquina sem estado")

// wakeResult: o pedido de ligar criado (jobs: quantos repetidores) ou o motivo de não criar
// (problem: o parâmetro da volta, ex.: "ligar-erro=mac").
type wakeResult struct {
	jobs    int
	problem string
}

// requestWake monta e grava o pedido de ligar: um job wake_on_lan para cada repetidor da
// sub-rede. at: o horário do pacote (zero: agora), para o reinício em horário marcado ligar a
// máquina antes; os jobs só são entregues a partir dele.
func (a *api) requestWake(r *http.Request, s panel.Session, id string, at time.Time) (wakeResult, error) {
	ctx := r.Context()
	now := a.now()
	list, err := a.store.WakeRequests(ctx, id, 1)
	if err != nil {
		return wakeResult{}, err
	}
	if wakePending(list, now) != nil {
		return wakeResult{problem: "ligar-erro=andamento"}, nil
	}
	h, found, err := a.store.HostState(ctx, id)
	if err != nil {
		return wakeResult{}, err
	}
	if !found {
		return wakeResult{}, errWakeNotFound
	}
	var hw *protocol.Wake
	if inv, ok, err := a.store.LatestInventory(ctx, id); err != nil {
		return wakeResult{}, err
	} else if ok && inv.Hardware != nil {
		hw = inv.Hardware.Wake
	}
	cands, err := a.store.WakeRelayHosts(ctx, id, now.Add(-wake.RelayFresh))
	if err != nil {
		return wakeResult{}, err
	}
	hosts := make([]wake.Host, 0, len(cands))
	for _, c := range cands {
		hosts = append(hosts, wake.Host{ID: c.ID, Name: c.Name, LastSeen: c.LastSeen, State: c.State})
	}
	plan, err := wake.Build(hw, h.State, hosts, now)
	var noRelay wake.NoRelayError
	switch {
	case errors.Is(err, wake.ErrNoMAC):
		return wakeResult{problem: "ligar-erro=mac"}, nil
	case errors.Is(err, wake.ErrNoNetwork):
		return wakeResult{problem: "ligar-erro=rede"}, nil
	case errors.As(err, &noRelay):
		return wakeResult{problem: "ligar-erro=repetidor&rede=" + url.QueryEscape(fmt.Sprint(noRelay.Networks))}, nil
	case err != nil:
		return wakeResult{}, err
	}
	params, err := jobs.WakeOnLANParamsJSON(jobs.WakeOnLANParams{Target: id, MACs: plan.MACs, Network: plan.Network.String()})
	if err != nil {
		return wakeResult{}, err
	}
	requested := now
	if at.After(now) {
		requested = at
	}
	req := store.WakeRequest{MachineID: id, RequestedAt: requested, RequestedBy: s.Username, Network: plan.Network.String(), MACs: plan.MACs}
	var relayNames []string
	for _, relay := range plan.Relays {
		jr := jobs.Request{MachineID: relay.ID, Type: jobs.TypeWakeOnLAN, TTL: panelWakeTTL, By: s.Username, Params: params}
		if at.After(now) {
			jr.NotBefore, jr.TTL = at, scheduledWakeTTL
		}
		rec, err := jobs.Issue(ctx, a.store, a.jobKey, jr, now)
		if errors.Is(err, jobs.ErrMachineUnavailable) {
			continue // aposentada entre a lista e agora
		}
		if err != nil {
			return wakeResult{}, err
		}
		req.Relays = append(req.Relays, relay.ID)
		req.JobIDs = append(req.JobIDs, rec.ID)
		relayNames = append(relayNames, dash(relay.Name))
	}
	if len(req.JobIDs) == 0 {
		return wakeResult{problem: "ligar-erro=repetidor"}, nil
	}
	if _, err := a.store.CreateWakeRequest(ctx, req); err != nil {
		return wakeResult{}, err
	}
	when := "agora"
	if at.After(now) {
		when = at.UTC().Format(time.RFC3339)
	}
	a.logger.Info("ligar pela rede pedido", "machine_id", id, "rede", req.Network, "macs", strings.Join(req.MACs, ","),
		"repetidores", strings.Join(req.Relays, ","), "jobs", fmt.Sprint(req.JobIDs), "quando", when, "username", s.Username, "request_id", RequestID(ctx))
	a.audit(r, s, "maquina.ligar", id, fmt.Sprintf("rede %s · %d repetidor(es): %s · jobs %v · %s", req.Network, len(req.JobIDs), strings.Join(relayNames, ", "), req.JobIDs, when))
	return wakeResult{jobs: len(req.JobIDs)}, nil
}

// wakeFlash: o aviso da volta do pedido de ligar.
func wakeFlash(q url.Values, b *pageBase) bool {
	if q.Get("ligar") == "pedido" {
		n, _ := strconv.Atoi(q.Get("n"))
		b.Flash = fmt.Sprintf("Pedido de ligar criado para %d %s da mesma sub-rede. O pacote sai no próximo check-in de um deles (até %s); a máquina conta como ligada no primeiro check-in dela depois disso.",
			max(n, 1), plural(max(n, 1), "repetidor", "repetidores"), b.CheckinWait)
		return true
	}
	if code := q.Get("ligar-erro"); code != "" {
		b.Error = wakeProblem(q.Encode())
		return true
	}
	return false
}

// wakeProblem: a mensagem de um pedido de ligar que não saiu, pelo parâmetro da volta
// ("ligar-erro=repetidor&rede=10.1.2.0/24").
func wakeProblem(raw string) string {
	q, _ := url.ParseQuery(raw)
	msg, ok := map[string]string{
		"sem-chave": "O servidor está sem a chave de jobs (PATCHD_JOB_SIGNING_KEY_FILE); nada foi pedido.",
		"andamento": "Já há um pedido de ligar em andamento para esta máquina; espere o resultado.",
		"mac":       wake.ErrNoMAC.Error() + ". Nada foi pedido.",
		"rede":      wake.ErrNoNetwork.Error() + ". Nada foi pedido.",
		"repetidor": "Nenhuma máquina ligada, com agente atualizado, fez check-in nas últimas 2 horas na mesma sub-rede " + q.Get("rede") + ". O pacote mágico não passa pelo roteador: ligue uma máquina dessa sub-rede (ou atualize o agente dela) e tente de novo.",
	}[q.Get("ligar-erro")]
	if !ok {
		return "o pedido de ligar não foi criado."
	}
	return msg
}

// wakeJobFinished anota, no pedido, o primeiro repetidor que mandou o pacote.
func (a *api) wakeJobFinished(ctx context.Context, job store.JobRow, state string) {
	if state != jobs.StateDone {
		return
	}
	if _, err := a.store.MarkWakeSent(ctx, job.ID, job.MachineID, a.now()); err != nil {
		a.logger.Error("ligar pela rede: anotar o pacote enviado", "job_id", job.ID, "error", err)
	}
}

// lapse: um intervalo curto em palavras ("menos de 1 min", "3 min", "1 h 5 min").
func lapse(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "menos de 1 min"
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	}
	return fmt.Sprintf("%d h %d min", int(d.Hours()), int(d.Minutes())%60)
}
