package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/tasantiago/patchd/internal/compliance"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/srvhealth"
	"github.com/tasantiago/patchd/internal/thirdparty"
)

// slowEvaluation: acima disso, a avaliação de uma máquina vai para o log como lenta.
const slowEvaluation = time.Second

// fleetStore é o que o estado da frota usa do banco: a avaliação e a lista de máquinas.
type fleetStore interface {
	complianceStore
	Machines(ctx context.Context) ([]protocol.MachineSummary, error)
}

// complianceService calcula o estado de compliance (RF-23). A avaliação de uma máquina lê o
// inventário inteiro (centenas de pacotes) e o catálogo; refeita a cada página para o parque
// todo, ela é o que deixa Parque, Visão geral e Patch lentos. Por isso cada avaliação fica
// guardada na memória (evalCache) e só é refeita quando o inventário, a busca ou o catálogo
// mudam, ou depois de evalTTL (avisos que dependem do relógio, como catálogo atrasado).
type complianceService struct {
	st     fleetStore
	logger *slog.Logger
	now    func() time.Time
	// manifest: os programas de terceiros (Aula 13.3); nil: não os avalia. O estado da
	// máquina não depende deles; a avaliação guardada já os traz para o Detail e o Items.
	manifest *thirdparty.Manifest

	cache evalCache
	// fill: um preenchimento do parque por vez (o segundo pedido acha o cache pronto). Canal
	// de uma vaga, e não Mutex, para a página desistir de esperar (fillWait).
	fill chan struct{}
	// kick acorda o Warm: a página achou avaliações vencidas demais para refazer na hora.
	kick chan struct{}
	once sync.Once // cria fill e kick (o serviço também é montado sem o construtor, nos testes)

	// memo: as respostas do catálogo que valem para o parque inteiro (memo.go).
	memoMu sync.Mutex
	memo   *evalMemo
}

func (s *complianceService) chans() {
	s.once.Do(func() {
		if s.fill == nil {
			s.fill = make(chan struct{}, 1)
		}
		if s.kick == nil {
			s.kick = make(chan struct{}, 1)
		}
	})
}

func newComplianceService(st fleetStore, logger *slog.Logger) *complianceService {
	return &complianceService{st: st, logger: logger, now: func() time.Time { return time.Now().UTC() }}
}

// errEvaluating: a máquina ainda não tem avaliação nenhuma e o servidor está calculando
// (o Warm, ou outra página); a página mostra "em avaliação" e se recarrega.
var errEvaluating = errors.New("em avaliação")

// Cache das avaliações.
const (
	// evalTTL: uma avaliação guardada vale no máximo isso, mesmo sem mudança (os avisos de
	// catálogo atrasado e o suporte do macOS dependem do relógio, numa escala de dias). Com
	// inventário, busca ou catálogo novos, a chave muda e a máquina é refeita na hora. Era 15
	// min: o parque inteiro refeito a cada 15 min, sem mudança nenhuma, pesava no banco.
	evalTTL = time.Hour
	// evalWorkers: o máximo de avaliações em paralelo; o de fato é um quarto do pool do banco
	// (evalWorkersFor), para os check-ins não esperarem conexão enquanto o parque é avaliado.
	evalWorkers = 4
	// warmEvery: o laço que mantém o cache quente (a primeira página do dia não paga o parque).
	warmEvery = 5 * time.Minute
)

// fillWait: quanto a página espera outro preenchimento (o Warm leva minutos depois de uma
// sincronização do catálogo) antes de mostrar o que já tem. Variável para os testes.
var fillWait = 2 * time.Second

// syncRefreshMax: até quantas avaliações (vencidas ou que faltam) a página faz na hora. Acima
// disso (o catálogo sincronizou, o prazo venceu para o parque inteiro, o servidor acabou de
// subir), ela mostra a avaliação anterior, ou "em avaliação", e acorda o Warm, em vez de pagar
// o parque dentro do prazo da página. Variável para os testes.
var syncRefreshMax = 50

type evalEntry struct {
	key string // inventário, busca e catálogo
	at  time.Time
	ev  machineEval
}

type evalCache struct {
	mu      sync.Mutex
	entries map[string]evalEntry
}

// get devolve a avaliação guardada (ok) e se ela ainda vale (fresh): mesma chave, dentro do prazo.
func (c *evalCache) get(id, key string, now time.Time) (ev machineEval, fresh, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[id]
	if !ok {
		return machineEval{}, false, false
	}
	return e.ev, e.key == key && now.Sub(e.at) < evalTTL && !now.Before(e.at), true
}

func (c *evalCache) put(id, key string, at time.Time, ev machineEval) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]evalEntry{}
	}
	c.entries[id] = evalEntry{key: key, at: at, ev: ev}
}

// keep tira do cache as máquinas que saíram do parque.
func (c *evalCache) keep(ids map[string]bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id := range c.entries {
		if !ids[id] {
			delete(c.entries, id)
		}
	}
}

// catalogKey resume o estado do catálogo (a última sincronização de cada fonte, boa ou não):
// uma sincronização nova muda a chave e as avaliações são refeitas.
func (s *complianceService) catalogKey(ctx context.Context) (string, error) {
	st, err := s.st.CatalogStatus(ctx, catalogSourceNames)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, c := range st {
		fmt.Fprintf(&b, "%s:%v:%v:%d;", c.Source, c.LastOK, c.LastFail, c.Runs)
	}
	return b.String(), nil
}

func evalKey(m protocol.MachineSummary, catalog string) string {
	scan := ""
	if m.ScanReceivedAt != nil {
		scan = m.ScanReceivedAt.UTC().Format(time.RFC3339Nano)
	}
	return m.InventoryHash + "|" + scan + "|" + catalog
}

// result é a avaliação de uma máquina, guardada ou nova.
type result struct {
	ev    *machineEval
	err   error
	stale bool // a avaliação anterior: a de agora está sendo feita em segundo plano
}

// Modos do evaluateAll.
const (
	modePage = iota // página do parque: avaliação anterior quando refazer custa demais
	modeWarm        // o Warm: refaz tudo o que venceu
	modeOne         // o detalhe de uma máquina: sempre a avaliação de agora, sem a vaga do parque
)

// evaluateAll devolve a avaliação de cada máquina: as guardadas, e as que faltam feitas agora,
// em paralelo. Um erro de uma máquina não derruba as outras. Na página, o prazo esgotado não é
// erro: o que não deu tempo fica com a avaliação anterior, ou "em avaliação" (errEvaluating).
// Só o cancelamento (o cliente desistiu) volta como erro.
func (s *complianceService) evaluateAll(ctx context.Context, list []protocol.MachineSummary, now time.Time, mode int) (map[string]result, error) {
	catalog, err := s.catalogKey(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]result, len(list))
	stale := map[string]machineEval{}
	var todo []protocol.MachineSummary
	ids := make(map[string]bool, len(list))
	collect := func() {
		todo = todo[:0]
		var old []protocol.MachineSummary
		for _, m := range list {
			ids[m.ID] = true
			if _, done := out[m.ID]; done {
				continue
			}
			ev, fresh, ok := s.cache.get(m.ID, evalKey(m, catalog), now)
			switch {
			case fresh:
				out[m.ID] = result{ev: &ev}
				delete(stale, m.ID)
			case ok:
				stale[m.ID] = ev
				old = append(old, m)
			default:
				todo = append(todo, m)
			}
		}
		if mode == modePage && len(todo)+len(old) > syncRefreshMax {
			// Muitas de uma vez (o catálogo sincronizou, o prazo venceu para o parque, o
			// servidor acabou de subir): a página usa as anteriores, ou mostra "em
			// avaliação", e o Warm refaz.
			todo = todo[:0]
			s.wake()
			return
		}
		todo = append(todo, old...)
	}
	collect()
	switch {
	case len(todo) == 0:
	case mode == modeOne:
		s.evaluateMissing(ctx, todo, catalog, now, out)
	case s.acquire(ctx, mode):
		collect() // quem preencheu antes deixou o cache pronto
		s.evaluateMissing(ctx, todo, catalog, now, out)
		<-s.fill
	default:
		s.wake()
	}
	// O que ficou sem avaliação de agora: a anterior, ou "em avaliação".
	for _, m := range list {
		if r, done := out[m.ID]; done && r.ev != nil {
			continue
		}
		if ev, ok := stale[m.ID]; ok {
			out[m.ID] = result{ev: &ev, stale: true}
		} else if _, done := out[m.ID]; !done || errors.Is(out[m.ID].err, context.DeadlineExceeded) {
			out[m.ID] = result{err: errEvaluating}
		}
	}
	if mode != modeOne {
		s.cache.keep(ids)
	}
	if errors.Is(ctx.Err(), context.Canceled) || (mode == modeWarm && ctx.Err() != nil) {
		return out, ctx.Err()
	}
	return out, nil
}

// acquire pega a vaga do preenchimento. A página espera no máximo fillWait; o Warm, o
// quanto o prazo dele deixar.
func (s *complianceService) acquire(ctx context.Context, mode int) bool {
	s.chans()
	var limit <-chan time.Time
	if mode == modePage {
		t := time.NewTimer(fillWait)
		defer t.Stop()
		limit = t.C
	}
	select {
	case s.fill <- struct{}{}:
		return true
	case <-ctx.Done():
	case <-limit:
	}
	return false
}

// wake acorda o Warm sem esperar (se já houver um aviso, basta ele).
func (s *complianceService) wake() {
	s.chans()
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

func (s *complianceService) evaluateMissing(ctx context.Context, missing []protocol.MachineSummary, catalog string, now time.Time, out map[string]result) {
	if len(missing) == 0 {
		return
	}
	type done struct {
		id string
		r  result
	}
	work := make(chan protocol.MachineSummary)
	results := make(chan done)
	var wg sync.WaitGroup
	for range min(s.workers(), len(missing)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for m := range work {
				ev, err := s.evaluateOne(ctx, m, catalog, now)
				results <- done{m.ID, result{ev: ev, err: err}}
			}
		}()
	}
	go func() {
		defer close(work)
		for _, m := range missing {
			select {
			case work <- m:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { wg.Wait(); close(results) }()
	for d := range results {
		out[d.id] = d.r
	}
}

// evaluateOne avalia uma máquina e guarda (só o que deu certo e não foi interrompido).
func (s *complianceService) evaluateOne(ctx context.Context, m protocol.MachineSummary, catalog string, now time.Time) (*machineEval, error) {
	start := time.Now()
	ev, err := evaluateID(ctx, memoStore{s.st, s.memoFor(catalog, now)}, m.ID, evalOptions{Now: now, Manifest: s.manifest})
	// Os programas sem regra (quase todo o inventário) só servem ao "compliance -v" da linha
	// de comando; guardados por máquina, eram a maior parte da memória do servidor.
	if ev.Third != nil {
		ev.Third.Unmatched = nil
	}
	if d := time.Since(start); d > slowEvaluation && err == nil {
		s.logger.Warn("avaliação de compliance lenta", "machine_id", m.ID, "duration_ms", d.Milliseconds(),
			"dica", "patchd-server compliance -machine ID -tempos mostra a consulta responsável")
	}
	if err != nil {
		return nil, err
	}
	if ctx.Err() == nil {
		s.cache.put(m.ID, evalKey(m, catalog), now, ev)
	}
	return &ev, nil
}

// Warm mantém o cache quente até o ctx acabar: a cada warmEvery, avalia o que mudou.
func (s *complianceService) Warm(ctx context.Context) {
	s.chans()
	t := time.NewTimer(30 * time.Second) // depois da partida assentar
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.kick:
		}
		wctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
		start := time.Now()
		if _, err := s.fleet(wctx, modeWarm); err != nil && ctx.Err() == nil {
			s.logger.Warn("compliance: aquecer o cache", "error", err)
		} else if d := time.Since(start); d > 10*time.Second {
			s.logger.Info("compliance: cache aquecido", "duration_ms", d.Milliseconds())
		}
		cancel()
		t.Reset(warmEvery)
	}
}

// Fleet avalia todas as máquinas, na ordem do que pede ação primeiro.
func (s *complianceService) Fleet(ctx context.Context) (protocol.ComplianceFleet, error) {
	return s.fleet(ctx, modePage)
}

func (s *complianceService) fleet(ctx context.Context, mode int) (protocol.ComplianceFleet, error) {
	list, err := s.st.Machines(ctx)
	if err != nil {
		return protocol.ComplianceFleet{}, err
	}
	now := s.now()
	fleet := protocol.ComplianceFleet{EvaluatedAt: now, Counts: map[string]int{}, Machines: make([]protocol.ComplianceStatus, 0, len(list))}
	for _, st := range compliance.States {
		fleet.Counts[string(st)] = 0
	}
	evals, err := s.evaluateAll(ctx, list, now, mode)
	if err != nil {
		return protocol.ComplianceFleet{}, err
	}
	for _, m := range list {
		r := evals[m.ID]
		switch {
		case errors.Is(r.err, errEvaluating):
			fleet.Evaluating++
		case r.stale:
			fleet.Refreshing++
		}
		cs, _ := s.statusOf(m, now, r)
		fleet.Counts[cs.State]++
		fleet.Machines = append(fleet.Machines, cs)
	}
	sort.SliceStable(fleet.Machines, func(i, j int) bool {
		a, b := fleet.Machines[i], fleet.Machines[j]
		if ra, rb := compliance.State(a.State).Rank(), compliance.State(b.State).Rank(); ra != rb {
			return ra < rb
		}
		if a.Exploited != b.Exploited {
			return a.Exploited > b.Exploited
		}
		if a.Pending != b.Pending {
			return a.Pending > b.Pending
		}
		return a.MachineID < b.MachineID
	})
	return fleet, nil
}

// Detail avalia uma máquina pelo ID completo, com a lista das pendências (Aula 7.6).
// Máquina aposentada ou desconhecida: found = false.
func (s *complianceService) Detail(ctx context.Context, id string) (protocol.ComplianceDetail, bool, error) {
	list, err := s.st.Machines(ctx)
	if err != nil {
		return protocol.ComplianceDetail{}, false, err
	}
	for _, m := range list {
		if m.ID != id {
			continue
		}
		now := s.now()
		evals, err := s.evaluateAll(ctx, []protocol.MachineSummary{m}, now, modeOne)
		if err != nil {
			return protocol.ComplianceDetail{}, false, err
		}
		cs, ev := s.statusOf(m, now, evals[m.ID])
		d := protocol.ComplianceDetail{ComplianceStatus: cs, AgentVersion: m.AgentVersion, Items: []protocol.PendingItem{}}
		if ev != nil {
			d.Target, d.Items = pendingItems(*ev)
			d.ThirdParty = thirdPartyItems(*ev, now)
		}
		return d, true, nil
	}
	return protocol.ComplianceDetail{}, false, nil
}

// Items avalia a frota e devolve as pendências de cada máquina (Aula 13.1: a visão por
// item, em Patch › Pendentes). É a mesma avaliação do Fleet, sem descartar os itens.
func (s *complianceService) Items(ctx context.Context) ([]protocol.MachineItems, error) {
	list, err := s.st.Machines(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now()
	evals, err := s.evaluateAll(ctx, list, now, modePage)
	if err != nil {
		return nil, err
	}
	out := make([]protocol.MachineItems, 0, len(list))
	// As máquinas com o mesmo produto e o mesmo build do Windows apontam para a mesma
	// avaliação (memo.go): a lista de itens delas é montada uma vez e compartilhada, só de
	// leitura. Uma lista por máquina eram mais de um milhão de itens num parque de 4.000.
	shared := map[*compliance.WindowsResult][]protocol.PendingItem{}
	for _, m := range list {
		cs, ev := s.statusOf(m, now, evals[m.ID])
		mi := protocol.MachineItems{MachineID: m.ID, Hostname: m.Hostname, OS: cs.OS, Catalog: cs.Catalog, Unit: cs.PendingUnit,
			Items: []protocol.PendingItem{}}
		if ev != nil {
			if w := ev.Windows; w != nil && ev.Note == "" && ev.Linux == nil {
				items, ok := shared[w]
				if !ok {
					_, items = pendingItems(*ev)
					shared[w] = items
				}
				mi.Items = items
			} else {
				_, mi.Items = pendingItems(*ev)
			}
			mi.ThirdParty = thirdPartyItems(*ev, now)
		}
		out = append(out, mi)
	}
	return out, nil
}

// statusOf monta o estado de uma máquina a partir da avaliação (guardada ou nova). O último
// contato e o reinício pendente vêm do resumo, na hora: "sem contato" não espera o cache. Um
// erro na avaliação de uma máquina não derruba o parque: ela fica "desconhecido" e o erro vai
// para o log.
func (s *complianceService) statusOf(m protocol.MachineSummary, now time.Time, r result) (protocol.ComplianceStatus, *machineEval) {
	cs := protocol.ComplianceStatus{
		MachineID: m.ID, Hostname: m.Hostname, OS: strings.TrimSpace(m.OSName + " " + m.OSVersion),
		LastSeenAt: m.LastSeenAt, EvaluatedAt: now,
	}
	f := compliance.Facts{LastSeen: m.LastSeenAt, RebootPending: m.RebootPending}
	var evp *machineEval
	switch {
	case errors.Is(r.err, errEvaluating):
		f.Unevaluated = "em avaliação: o servidor está calculando, recarregue em instantes"
	case r.ev == nil && r.err == nil, errors.Is(r.err, context.Canceled), errors.Is(r.err, context.DeadlineExceeded):
		// O cliente desistiu (recarregou a página): não é falha do servidor.
		f.Unevaluated = "avaliação interrompida"
	case r.err != nil:
		s.logger.Error("avaliação de compliance", "machine_id", m.ID, "error", r.err)
		f.Unevaluated = "erro na avaliação (veja o log do servidor)"
	default:
		fillFacts(&f, &cs, *r.ev)
		evp = r.ev
	}
	state, reasons := compliance.Classify(f, now)
	cs.State = string(state)
	cs.Reasons = reasons
	if cs.Reasons == nil {
		cs.Reasons = []string{}
	}
	return cs, evp
}

// pendingItems lista as pendências da avaliação, exploradas primeiro (a ordem da
// avaliação já é essa), e a atualização que resolve todas.
func pendingItems(ev machineEval) (string, []protocol.PendingItem) {
	items := []protocol.PendingItem{}
	switch {
	case ev.Note != "":
		return "", items
	case ev.Linux != nil:
		for _, f := range ev.Linux.Findings {
			ids := make([]string, 0, len(f.Advisories))
			sev := ""
			for _, a := range f.Advisories {
				ids = append(ids, a.ID)
				if sev == "" {
					sev = a.Severity
				}
			}
			note := strings.Join(ids, ", ")
			if len(ids) > 3 {
				note = strings.Join(ids[:3], ", ") + fmt.Sprintf(" e mais %d", len(ids)-3)
			}
			items = append(items, protocol.PendingItem{ID: f.Source, Severity: sev, Exploited: f.Exploited() || f.InferredExploited(),
				Installed: f.Installed, FixedIn: f.Target, Note: note, Packages: f.Binaries})
		}
		return "", items
	case ev.Windows != nil:
		r := ev.Windows
		// Uma máquina tem centenas de CVEs e poucas correções (KB) distintas: o build
		// instalado e o texto de cada correção são feitos uma vez, não um por CVE.
		installed := r.Build.String()
		fixes := map[string]string{}
		items = make([]protocol.PendingItem, 0, len(r.Pending))
		for _, c := range r.Pending {
			fixed := ""
			if c.MinKB != "" {
				k := c.MinKB + "/" + strconv.Itoa(c.Min.Major) + "/" + strconv.Itoa(c.Min.UBR)
				if fixed = fixes[k]; fixed == "" {
					fixed = fmt.Sprintf("%d.%d (KB%s)", c.Min.Major, c.Min.UBR, c.MinKB)
					fixes[k] = fixed
				}
			}
			items = append(items, protocol.PendingItem{ID: c.CVE, Severity: c.Severity, Exploited: c.Explored(),
				Installed: installed, FixedIn: fixed, Note: c.Title})
		}
		target := ""
		if len(r.Pending) > 0 && r.TargetKB != "" {
			target = fmt.Sprintf("KB%s (build %s)", r.TargetKB, r.Target)
		}
		return target, items
	case ev.MacOS != nil:
		r := ev.MacOS.Result
		items = make([]protocol.PendingItem, 0, len(r.Pending))
		for _, c := range r.Pending {
			items = append(items, protocol.PendingItem{ID: c.CVE, Severity: c.Severity, Exploited: c.Exploited || c.KEV,
				Installed: r.Version, FixedIn: c.Min})
		}
		target := ""
		if r.Target != "" {
			target = "macOS " + r.Target
		}
		return target, items
	}
	return "", items
}

// thirdPartyItems: os programas de terceiros identificados, com a origem da referência
// escrita como na linha de comando (fonte e data, mínimo do manifesto, ou a nota da regra).
func thirdPartyItems(ev machineEval, now time.Time) []protocol.ThirdPartyItem {
	if ev.Third == nil {
		return nil
	}
	out := make([]protocol.ThirdPartyItem, 0, len(ev.Third.Findings))
	for _, f := range ev.Third.Findings {
		it := protocol.ThirdPartyItem{AppID: f.App.ID, Program: f.App.Name, Name: f.Software.Name, State: f.State,
			Installed: f.Installed, Reference: f.Reference, Scope: f.Software.Scope}
		if it.Installed == "" {
			it.Installed = f.Software.Version
		}
		switch {
		case f.Ref != nil:
			it.Origin = fmt.Sprintf("%s, %s", f.Ref.Source, f.Ref.Fetched.Format("2006-01-02"))
			if now.Sub(f.Ref.Fetched) > 7*24*time.Hour {
				it.Origin += " (antiga)"
			}
		case f.App.Category == thirdparty.CategoryMinimum:
			it.Origin = "mínimo do manifesto"
		case f.App.Note != "":
			it.Origin = f.App.Note
		}
		out = append(out, it)
	}
	return out
}

// fillFacts extrai da avaliação o que a classificação precisa e o que a resposta mostra.
func fillFacts(f *compliance.Facts, cs *protocol.ComplianceStatus, ev machineEval) {
	cs.Catalog, cs.CatalogWarnings = ev.Catalog, ev.Warnings
	if !ev.Collected.IsZero() {
		t := ev.Collected.UTC()
		cs.InventoryCollectedAt = &t
	}
	switch {
	case ev.NoInventory:
		f.NoInventory = true
		return
	case ev.Note != "":
		f.Unevaluated = ev.Note
		return
	}
	switch {
	case ev.Linux != nil:
		f.Pending, f.PendingUnit = len(ev.Linux.Findings), "pacotes"
		for _, x := range ev.Linux.Findings {
			if x.Exploited() || x.InferredExploited() {
				f.Exploited++
			}
		}
		f.NewerKernel = len(ev.Linux.Kernel.Newer) > 0
	case ev.Windows != nil:
		f.Pending, f.PendingUnit = len(ev.Windows.Pending), "CVEs"
		for _, c := range ev.Windows.Pending {
			if c.Explored() {
				f.Exploited++
			}
		}
	case ev.MacOS != nil:
		f.Pending, f.PendingUnit = len(ev.MacOS.Result.Pending), "CVEs"
		for _, c := range ev.MacOS.Result.Pending {
			if c.Exploited || c.KEV {
				f.Exploited++
			}
		}
		f.SupportEnded = ev.MacOS.Support.State == compliance.SupportEnded
	}
	cs.Pending, cs.PendingUnit, cs.Exploited = f.Pending, f.PendingUnit, f.Exploited
}

// printFleet escreve o estado da frota. Com summary, só as contagens (sem nomes: a saída
// pode ir para um chamado ou para o chat).
func printFleet(fleet protocol.ComplianceFleet, summary bool, out io.Writer) {
	parts := make([]string, 0, len(compliance.States))
	for _, st := range compliance.States {
		parts = append(parts, fmt.Sprintf("%s %d", st.Label(), fleet.Counts[string(st)]))
	}
	fmt.Fprintf(out, "Parque: %d máquina(s): %s\n", len(fleet.Machines), strings.Join(parts, " · "))
	if summary {
		return
	}
	fmt.Fprintln(out)
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ESTADO\tMÁQUINA\tSO\tPENDENTES\tEXPLORADAS\tÚLTIMO CONTATO\tNOME\tMOTIVOS")
	for _, m := range fleet.Machines {
		id := m.MachineID
		if len(id) > 8 {
			id = id[:8]
		}
		pend := "-"
		if m.Pending > 0 {
			pend = fmt.Sprintf("%d %s", m.Pending, compliance.Unit(m.Pending, m.PendingUnit))
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s\n", compliance.State(m.State).Label(), id, orDash(m.OS), pend, m.Exploited,
			since(fleet.EvaluatedAt.Sub(m.LastSeenAt)), orDash(m.Hostname), strings.Join(m.Reasons, "; "))
	}
	_ = tw.Flush()
}

// since escreve há quanto tempo: minutos, horas ou dias.
func since(d time.Duration) string {
	if d < time.Hour {
		return fmt.Sprintf("há %d min", int(d.Minutes()))
	}
	return ago(d)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// workers: avaliações em paralelo, um quarto do pool do banco (no mínimo 1, no máximo
// evalWorkers). Com o pool padrão de 4, uma só: as outras conexões ficam para os check-ins e o
// painel. O store sem pool (memória, testes) usa o máximo.
func (s *complianceService) workers() int {
	ps, ok := s.st.(interface{ PoolStat() srvhealth.PoolStat })
	if !ok {
		return evalWorkers
	}
	return evalWorkersFor(int(ps.PoolStat().Max))
}

func evalWorkersFor(poolMax int) int {
	return min(evalWorkers, max(1, poolMax/4))
}
