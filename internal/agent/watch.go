package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/tasantiago/patchd/internal/perfeval"
	"github.com/tasantiago/patchd/internal/protocol"
)

// A sentinela (docs/monitoramento.md, seções 2 e 3): mede o desempenho a cada minuto, guarda
// as amostras para o próximo check-in e adianta o check-in quando algo relevante muda (usuário,
// IP, programa instalado ou desempenho passando do limite). Ela só antecipa: quem decide o
// alerta é o servidor (P-01).

// Motivos do check-in antecipado (vão no check-in, só para o log do servidor).
const (
	ReasonUser        = "usuario"
	ReasonNetwork     = "rede"
	ReasonSoftware    = "software"
	ReasonPerf        = "desempenho"
	ReasonSelfService = "autoatendimento"
)

const (
	watchTick = time.Minute
	// earlyGap: no máximo um check-in antecipado pela sentinela a cada tanto.
	earlyGap = 5 * time.Minute
	// earlySplay: o check-in antecipado espera um sorteio de até isso (uma mudança em massa,
	// como um programa instalado na frota toda, não vira uma avalanche no servidor).
	earlySplay = 2 * time.Minute
	// defaultSoftwareEvery: a cada quanto a lista de software é conferida.
	defaultSoftwareEvery = 15 * time.Minute
)

// Watch configura a sentinela. Os campos nil desligam a parte correspondente.
type Watch struct {
	// Sample mede o desempenho; disks são os volumes da coleta do estado.
	Sample func(ctx context.Context, at time.Time, disks []protocol.Disk) (protocol.PerfSample, error)
	// State lê usuários, rede e discos (hoststate.Collect).
	State func(ctx context.Context) protocol.MachineState
	// Software lista o software instalado (só a lista, sem o resto do inventário). nil: a
	// lista não é vigiada (macOS, onde ela é cara).
	Software      func(ctx context.Context) ([]protocol.Software, error)
	SoftwareEvery time.Duration
}

// watchState é o que a sentinela guarda entre um minuto e outro.
type watchState struct {
	mu       sync.Mutex
	samples  []protocol.PerfSample
	policy   protocol.MonitorPolicy
	eval     perfeval.State
	users    string
	net      string
	software string
	nextSW   time.Time
	pending  string    // o motivo esperando o check-in antecipado
	dueAt    time.Time // a partir de quando ele pode sair
	early    time.Time // o último check-in antecipado pela sentinela
}

func (a *Agent) watchInit() {
	a.watchOnce.Do(func() {
		a.ws = &watchState{policy: protocol.DefaultMonitorPolicy, eval: perfeval.State{}}
	})
}

// watchLoop roda a sentinela até o contexto acabar.
func (a *Agent) watchLoop(ctx context.Context) {
	a.watchInit()
	for {
		if err := a.sleep(ctx, watchTick); err != nil {
			return
		}
		a.watchOnceTick(ctx)
	}
}

// watchOnceTick é um minuto da sentinela.
func (a *Agent) watchOnceTick(ctx context.Context) {
	a.watchInit()
	w, ws := a.Watch, a.ws
	now := a.now()
	var st protocol.MachineState
	if w.State != nil {
		st = w.State(ctx)
	}
	var sample *protocol.PerfSample
	if w.Sample != nil {
		s, err := w.Sample(ctx, now, st.Disks)
		if err != nil {
			a.Logger.Debug("medida de desempenho falhou", "error", err)
		} else {
			sample = &s
		}
	}
	var sw string
	every := w.SoftwareEvery
	if every <= 0 {
		every = defaultSoftwareEvery
	}
	ws.mu.Lock()
	swDue := w.Software != nil && !now.Before(ws.nextSW)
	ws.mu.Unlock()
	if swDue {
		if list, err := w.Software(ctx); err == nil {
			sw = softwarePrint(list)
		}
	}

	ws.mu.Lock()
	defer ws.mu.Unlock()
	if sample != nil {
		ws.samples = append(ws.samples, *sample)
		if over := len(ws.samples) - protocol.MaxPerfSamples; over > 0 {
			ws.samples = slices.Delete(ws.samples, 0, over)
		}
		if len(ws.eval.Step(ws.policy, *sample)) > 0 {
			ws.note(ReasonPerf, now, a.rand)
		}
	}
	if w.State != nil {
		users, net := usersPrint(st.Users), netPrint(st.Interfaces)
		if ws.users != "" && users != ws.users {
			ws.note(ReasonUser, now, a.rand)
		}
		if ws.net != "" && net != ws.net {
			ws.note(ReasonNetwork, now, a.rand)
		}
		ws.users, ws.net = orEmpty(users), orEmpty(net)
	}
	if swDue {
		ws.nextSW = now.Add(every)
		if sw != "" {
			if ws.software != "" && sw != ws.software {
				ws.note(ReasonSoftware, now, a.rand)
			}
			ws.software = sw
		}
	}
	if ws.pending != "" && !now.Before(ws.dueAt) && (ws.early.IsZero() || now.Sub(ws.early) >= earlyGap) {
		ws.early = now
		a.Logger.Info("check-in antecipado pedido pela sentinela", "motivo", ws.pending)
		a.wakeFor(ws.pending)
		ws.pending = ""
	}
}

// orEmpty: a impressão de "nada" é diferente de "ainda não lida".
func orEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// note marca um motivo para o check-in antecipado (o primeiro motivo fica).
func (ws *watchState) note(reason string, now time.Time, r randFn) {
	if ws.pending != "" {
		return
	}
	ws.pending, ws.dueAt = reason, now.Add(between(r, 0, earlySplay))
}

// perfBatch: as amostras esperando o envio (cópia).
func (a *Agent) perfBatch() []protocol.PerfSample {
	if a.Watch == nil {
		return nil
	}
	a.watchInit()
	a.ws.mu.Lock()
	defer a.ws.mu.Unlock()
	return slices.Clone(a.ws.samples)
}

// perfSent tira da fila as n primeiras amostras (o servidor as aceitou) e aplica os limites
// que ele mandou. Um check-in feito também atende o motivo que esperava.
func (a *Agent) perfSent(n int, policy *protocol.MonitorPolicy) {
	if a.Watch == nil {
		return
	}
	a.watchInit()
	a.ws.mu.Lock()
	defer a.ws.mu.Unlock()
	if n > len(a.ws.samples) {
		n = len(a.ws.samples)
	}
	a.ws.samples = slices.Delete(a.ws.samples, 0, n)
	if policy != nil && policy.Check() == nil {
		a.ws.policy = *policy
	}
	a.ws.pending = ""
}

// usersPrint: quem está conectado (conta e tipo de sessão; bloquear a tela não conta).
func usersPrint(users []protocol.LoggedUser) string {
	l := make([]string, 0, len(users))
	for _, u := range users {
		l = append(l, strings.ToLower(u.Account)+"@"+u.Session)
	}
	slices.Sort(l)
	return strings.Join(slices.Compact(l), ",")
}

// virtualIf: interfaces que mudam sozinhas (containers, bridges de VM) não contam como rede.
var virtualIf = []string{"veth", "docker", "br-", "virbr", "cni", "flannel", "lo"}

// netPrint: os endereços das interfaces ativas.
func netPrint(ifs []protocol.NetInterface) string {
	var l []string
	for _, n := range ifs {
		if !n.Up || slices.ContainsFunc(virtualIf, func(p string) bool { return strings.HasPrefix(n.Name, p) }) {
			continue
		}
		for _, ip := range append(slices.Clone(n.IPv4), n.IPv6...) {
			l = append(l, n.Name+"="+ip)
		}
	}
	slices.Sort(l)
	return strings.Join(l, ",")
}

// softwarePrint: a impressão da lista (nome, versão e editor).
func softwarePrint(list []protocol.Software) string {
	l := make([]string, 0, len(list))
	for _, s := range list {
		l = append(l, s.Name+"\x00"+s.Version+"\x00"+s.Publisher)
	}
	slices.Sort(l)
	h := sha256.Sum256([]byte(strings.Join(l, "\n")))
	return hex.EncodeToString(h[:])
}
