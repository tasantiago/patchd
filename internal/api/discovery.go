package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tasantiago/patchd/internal/descoberta"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/store"
)

// Descoberta de rede (Aula 23.7): o admin cadastra faixas de IP; o servidor varre (sondagem
// TCP a partir do container) e, cruzando com o inventário pelo IP e pelo nome, mostra os
// hosts SEM AGENTE, com o comando de instalação pronto (PowerShell/PsExec/SSH/GPO). Nenhuma
// credencial é guardada: o comando sai com o usuário e o token a preencher.

// scanMaxRanges, scanTimeout: limites da varredura em segundo plano.
const scanTimeout = 10 * time.Minute

// WithNetDial injeta a sondagem TCP (o teste passa uma falsa). Sem ela, o servidor usa o
// dialer padrão.
func WithNetDial(d descoberta.Dial) Option { return func(a *api) { a.netDial = d } }

// WithNetLookup injeta o reverse-DNS (o teste passa um falso; sem ele, o DNS de verdade).
func WithNetLookup(f func(ctx context.Context, ip string) string) Option {
	return func(a *api) { a.netLookup = f }
}

func (a *api) lookup(ctx context.Context, ip string) string {
	if a.netLookup != nil {
		return a.netLookup(ctx, ip)
	}
	return reverseDNS(ctx, &net.Resolver{}, ip)
}

// tcpDial: a sondagem padrão — abre a conexão TCP e fecha; quem aceitou está vivo naquela
// porta. Sem ICMP (o container roda sem root).
func tcpDial(ctx context.Context, ip netip.Addr, port int) bool {
	d := net.Dialer{Timeout: 700 * time.Millisecond}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// scanState: a varredura em andamento (uma por vez). Só na memória: reiniciar o servidor
// interrompe a varredura (o que já foi gravado fica).
type scanState struct {
	mu        sync.Mutex
	running   bool
	by        string
	startedAt time.Time
	total     int
	done      atomic.Int64
}

func (s *scanState) begin(by string, total int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return false
	}
	s.running, s.by, s.startedAt, s.total = true, by, time.Now(), total
	s.done.Store(0)
	return true
}

func (s *scanState) end() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running = false
}

func (s *scanState) snapshot() (running bool, total, done int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running, s.total, int(s.done.Load())
}

func (a *api) dialer() descoberta.Dial {
	if a.netDial != nil {
		return a.netDial
	}
	return tcpDial
}

// --- Página ------------------------------------------------------------------------------

type discoveryView struct {
	pageBase
	Ranges    []rangeRow
	Hosts     []hostRow // só os sem agente
	WithAgent int
	Alive     int
	Runs      []runRow
	Scanning  bool
	ScanDone  int
	ScanTotal int
	LastRun   string
	PublicURL bool // há PATCHD_PUBLIC_URL (o comando sai com o endereço real)
	MaxRanges int
}

type rangeRow struct {
	ID             int64
	Spec, Note, By string
	Enabled        bool
	Created        string
}

type hintRow struct{ Label, Command, Note string }

type hostRow struct {
	IP, Ports, Hostname, OS, OSLabel string
	FirstSeen, LastSeen              string
	Hints                            []hintRow
}

type runRow struct {
	When, By, Note        string
	Scanned, Alive, SemAg int
	Partial               bool
}

// discoveryPage: GET /painel/descoberta, só admin.
func (a *api) discoveryPage(w http.ResponseWriter, r *http.Request, s panel.Session) {
	v := discoveryView{pageBase: a.baseFor(r.Context(), s, "Descoberta de rede", "descoberta"),
		MaxRanges: descoberta.MaxRanges, PublicURL: a.publicURL != ""}
	q := r.URL.Query()
	discoveryFlash(q, &v.pageBase)

	ranges, err := a.store.DiscoveryRanges(r.Context())
	if err != nil {
		a.internalError(w, r, "ler as faixas", err)
		return
	}
	for _, d := range ranges {
		v.Ranges = append(v.Ranges, rangeRow{ID: d.ID, Spec: d.Spec, Note: d.Note, By: d.CreatedBy, Enabled: d.Enabled,
			Created: a.stamp(d.CreatedAt)})
	}

	hosts, err := a.store.DiscoveryHosts(r.Context())
	if err != nil {
		a.internalError(w, r, "ler os hosts", err)
		return
	}
	v.Alive = len(hosts)
	ca := len(a.agentCA) > 0
	for _, h := range hosts {
		if h.MatchID != "" {
			v.WithAgent++
			continue
		}
		row := hostRow{IP: h.IP, Ports: h.Ports, Hostname: h.Hostname, OS: h.OSGuess, OSLabel: osLabel(h.OSGuess),
			FirstSeen: a.stamp(h.FirstSeenAt), LastSeen: a.stamp(h.LastSeenAt)}
		for _, hint := range descoberta.InstallHints(h.IP, a.publicURL, h.OSGuess, ca) {
			row.Hints = append(row.Hints, hintRow{Label: hint.Label, Command: hint.Command, Note: hint.Note})
		}
		v.Hosts = append(v.Hosts, row)
	}

	runs, err := a.store.DiscoveryRuns(r.Context(), 5)
	if err != nil {
		a.internalError(w, r, "ler as varreduras", err)
		return
	}
	for _, d := range runs {
		v.Runs = append(v.Runs, runRow{When: a.stamp(d.StartedAt), By: dash(d.StartedBy), Note: d.Note,
			Scanned: d.Scanned, Alive: d.Alive, SemAg: d.WithoutAgent, Partial: d.Partial})
	}
	if len(runs) > 0 {
		v.LastRun = a.stamp(runs[0].StartedAt)
	}

	if a.discovery != nil {
		if running, total, done := a.discovery.snapshot(); running {
			v.Scanning, v.ScanTotal, v.ScanDone, v.Refresh = true, total, done, 3
		}
	}
	a.render(w, r, http.StatusOK, "descoberta", v)
}

func osLabel(os string) string {
	switch os {
	case "windows":
		return "Windows"
	case "ssh":
		return "Linux/SSH"
	}
	return "—"
}

// --- Faixas ------------------------------------------------------------------------------

// addRangeAction: POST /painel/descoberta/faixas.
func (a *api) addRangeAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	r.Body = http.MaxBytesReader(w, r.Body, maxLoginBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulário inválido", http.StatusBadRequest)
		return
	}
	back := func(q string) { http.Redirect(w, r, "/painel/descoberta?"+q, http.StatusSeeOther) }
	note := strings.TrimSpace(r.PostFormValue("nota"))
	if len([]rune(note)) > 120 || strings.ContainsAny(note, "\r\n\t") {
		back("erro=nota")
		return
	}
	spec, err := descoberta.ParseSpec(r.PostFormValue("faixa"))
	if err != nil {
		back("erro=faixa&motivo=" + url.QueryEscape(shortError(err.Error(), 80)))
		return
	}
	ranges, err := a.store.DiscoveryRanges(r.Context())
	if err != nil {
		a.internalError(w, r, "ler as faixas", err)
		return
	}
	if len(ranges) >= descoberta.MaxRanges {
		back("erro=limite")
		return
	}
	id, err := a.store.AddDiscoveryRange(r.Context(), spec.Text, note, s.Username)
	switch {
	case err == store.ErrRangeExists:
		back("erro=repetida")
		return
	case err != nil:
		a.internalError(w, r, "cadastrar a faixa", err)
		return
	}
	a.audit(r, s, "descoberta.faixa", spec.Text, fmt.Sprintf("faixa %d cadastrada", id))
	back("faixa=" + strconv.FormatInt(id, 10))
}

// rangeOpAction: POST /painel/descoberta/faixas/{id}/{op}, op ∈ ligar|desligar|remover.
func (a *api) rangeOpAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	back := func(q string) { http.Redirect(w, r, "/painel/descoberta?"+q, http.StatusSeeOther) }
	op := r.PathValue("op")
	switch op {
	case "ligar", "desligar":
		ok, err := a.store.SetDiscoveryRangeEnabled(r.Context(), id, op == "ligar")
		if err != nil {
			a.internalError(w, r, "mudar a faixa", err)
			return
		}
		if !ok {
			back("erro=sumiu")
			return
		}
		a.audit(r, s, "descoberta.faixa", fmt.Sprintf("faixa %d", id), op)
		back("mudou=1")
	case "remover":
		ok, err := a.store.DeleteDiscoveryRange(r.Context(), id)
		if err != nil {
			a.internalError(w, r, "remover a faixa", err)
			return
		}
		if !ok {
			back("erro=sumiu")
			return
		}
		a.audit(r, s, "descoberta.faixa", fmt.Sprintf("faixa %d", id), "removida")
		back("removida=1")
	default:
		http.NotFound(w, r)
	}
}

// --- Varredura ---------------------------------------------------------------------------

// scanAction: POST /painel/descoberta/varrer. Dispara a varredura em segundo plano e volta
// para a página, que acompanha o andamento por recarga automática.
func (a *api) scanAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	back := func(q string) { http.Redirect(w, r, "/painel/descoberta?"+q, http.StatusSeeOther) }
	ranges, err := a.store.DiscoveryRanges(r.Context())
	if err != nil {
		a.internalError(w, r, "ler as faixas", err)
		return
	}
	var specs []descoberta.Spec
	for _, d := range ranges {
		if !d.Enabled {
			continue
		}
		if sp, err := descoberta.ParseSpec(d.Spec); err == nil {
			specs = append(specs, sp)
		}
	}
	if len(specs) == 0 {
		back("erro=sem-faixa")
		return
	}
	total := 0
	for _, sp := range specs {
		total += int(sp.Count()) // #nosec G115 -- Count() ≤ MaxSpecHosts (65 536); cabe em int
	}
	if total > descoberta.MaxScanHosts {
		total = descoberta.MaxScanHosts
	}
	if a.discovery == nil {
		a.discovery = &scanState{}
	}
	if !a.discovery.begin(s.Username, total) {
		back("erro=rodando")
		return
	}
	a.logger.Info("varredura de descoberta iniciada", "ranges", len(specs), "addrs", total,
		"username", s.Username, "request_id", RequestID(r.Context()))
	a.audit(r, s, "descoberta.varrer", fmt.Sprintf("%d faixa(s)", len(specs)), fmt.Sprintf("%d endereço(s)", total))

	// Em segundo plano: o contexto da requisição morre no redirect, então um novo, só com o
	// teto de tempo (gosec G118: nada de context.Background sozinho).
	go a.runScan(context.WithoutCancel(r.Context()), specs, s.Username)
	back("iniciada=1")
}

// runScan sonda as faixas, cruza com o inventário e grava o resultado.
func (a *api) runScan(ctx context.Context, specs []descoberta.Spec, by string) {
	defer a.discovery.end()
	ctx, cancel := context.WithTimeout(ctx, scanTimeout)
	defer cancel()
	started := a.now()

	found, scanned, partial := descoberta.Scan(ctx, specs, descoberta.Options{
		Dial: a.dialer(), Progress: &a.discovery.done})

	known, err := a.knownNetwork(ctx)
	if err != nil {
		a.logger.Error("descoberta: ler o inventário", "error", err)
		known = networkIndex{}
	}
	hosts := make([]store.DiscoveryHost, 0, len(found))
	sem := 0
	for _, f := range found {
		h := store.DiscoveryHost{IP: f.IP.String(), Ports: descoberta.PortsText(f.Ports), OSGuess: f.OS}
		// Reverse-DNS, para o nome e para cruzar (melhor esforço, com teto de tempo curto).
		name := a.lookup(ctx, f.IP.String())
		h.Hostname = name
		if id, ok := known.byIP[f.IP.String()]; ok {
			h.MatchID, h.MatchKind = id, "ip"
		} else if id, ok := known.byName[firstLabel(name)]; ok && name != "" {
			h.MatchID, h.MatchKind = id, "nome"
		} else {
			sem++
		}
		hosts = append(hosts, h)
	}
	finished := a.now()
	note := ""
	if partial {
		note = fmt.Sprintf("parcial: o teto de %d endereços por varredura foi atingido", descoberta.MaxScanHosts)
	}
	run := store.DiscoveryRun{StartedAt: started, FinishedAt: &finished, Scanned: scanned, Alive: len(found),
		WithoutAgent: sem, Partial: partial, StartedBy: by, Note: note}
	if _, err := a.store.ReplaceDiscoveryHosts(ctx, hosts, run); err != nil {
		a.logger.Error("descoberta: gravar o resultado", "error", err)
		return
	}
	a.logger.Info("varredura de descoberta terminada", "scanned", scanned, "alive", len(found), "sem_agente", sem, "partial", partial)
}

// networkIndex: o que o inventário sabe da rede, para cruzar com os hosts descobertos.
type networkIndex struct {
	byIP   map[string]string // IP → ID da máquina
	byName map[string]string // hostname (minúsculo, primeiro rótulo) → ID
}

// knownNetwork monta o índice a partir do inventário: os IPs de cada interface e o endereço
// do check-in, mais o hostname.
func (a *api) knownNetwork(ctx context.Context) (networkIndex, error) {
	list, err := a.store.InventoryList(ctx)
	if err != nil {
		return networkIndex{}, err
	}
	idx := networkIndex{byIP: map[string]string{}, byName: map[string]string{}}
	for _, m := range list {
		if m.CheckinAddr != "" {
			idx.byIP[m.CheckinAddr] = m.ID
		}
		if st := m.State; st != nil {
			for _, n := range st.Interfaces {
				for _, a := range append(append([]string{}, n.IPv4...), n.IPv6...) {
					if p, err := netip.ParsePrefix(a); err == nil {
						idx.byIP[p.Addr().String()] = m.ID
					}
				}
			}
		}
		if h := firstLabel(m.Hostname); h != "" {
			idx.byName[h] = m.ID
		}
	}
	return idx, nil
}

// reverseDNS: o nome de um IP, melhor esforço, com teto de tempo curto.
func reverseDNS(ctx context.Context, res *net.Resolver, ip string) string {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	names, err := res.LookupAddr(ctx, ip)
	if err != nil || len(names) == 0 {
		return ""
	}
	return strings.TrimSuffix(names[0], ".")
}

// firstLabel: o primeiro rótulo de um nome, em minúsculas (pc-01.dominio → pc-01). O
// inventário guarda o hostname curto; o reverse-DNS costuma vir com o domínio.
func firstLabel(name string) string {
	name = strings.TrimSuffix(strings.TrimSpace(name), ".")
	if name == "" {
		return ""
	}
	if i := strings.IndexByte(name, '.'); i >= 0 {
		name = name[:i]
	}
	return strings.ToLower(name)
}

// discoveryFlash traduz os parâmetros de volta em mensagem.
func discoveryFlash(q url.Values, b *pageBase) {
	switch {
	case q.Has("faixa"):
		b.Flash = "Faixa cadastrada. Clique em Varrer agora para procurar máquinas nela."
	case q.Has("iniciada"):
		b.Flash = "Varredura iniciada. A página se atualiza sozinha; os hosts aparecem ao terminar."
	case q.Has("mudou"), q.Has("removida"):
		b.Flash = "Faixa atualizada."
	case q.Get("erro") == "faixa":
		b.Error = "Faixa inválida: " + shortError(q.Get("motivo"), 80) + " Use CIDR (10.0.0.0/24) ou intervalo (10.0.0.10-60)."
	case q.Get("erro") == "repetida":
		b.Error = "Essa faixa já está cadastrada."
	case q.Get("erro") == "nota":
		b.Error = "Descrição: até 120 caracteres, numa linha."
	case q.Get("erro") == "limite":
		b.Error = fmt.Sprintf("Limite de %d faixas atingido; remova alguma antes.", descoberta.MaxRanges)
	case q.Get("erro") == "sem-faixa":
		b.Error = "Não há faixa ligada para varrer. Cadastre uma e deixe-a ligada."
	case q.Get("erro") == "rodando":
		b.Error = "Já há uma varredura em andamento; espere ela terminar."
	case q.Get("erro") == "sumiu":
		b.Error = "Essa faixa não existe mais; nada mudou."
	}
}
