package api

import (
	"context"
	"fmt"
	"net/netip"
	"time"

	"github.com/tasantiago/patchd/internal/store"
)

// Regras dos grupos de implantação. A máquina vai para o grupo pela ordem:
//
//  1. à mão (detalhe, Parque, Inventário, página do grupo, patchd-server ring): fica onde foi posta;
//  2. pelo prédio do patrimônio (building_rings);
//  3. pela faixa de IP (ip_rings): a mais específica (a menor) vence; duas do mesmo tamanho
//     que dizem grupos diferentes são conflito, e a máquina não sai do lugar;
//  4. sem regra: fica onde está (máquina nova entra no 1, o padrão).
//
// Quando o prédio e a faixa discordam, vale o prédio, e a página do grupo mostra o aviso.
// As regras valem ao gravar uma regra, a cada importação do patrimônio, no inventário de uma
// máquina nova (pelo prédio) e numa passada a cada ringRuleEvery (pela faixa, que vem do
// check-in).

// ringRuleEvery: a passada que aplica as regras (o IP da máquina muda no check-in).
const ringRuleEvery = 5 * time.Minute

type ipRule struct {
	store.IPRing
	rng  ipRange
	size uint32
}

type ringRules struct {
	buildings map[string]int
	ips       []ipRule
}

func (rr ringRules) empty() bool { return len(rr.buildings) == 0 && len(rr.ips) == 0 }

// ruleDecision: para onde as regras mandam a máquina.
type ruleDecision struct {
	OK       bool   // alguma regra decide
	Ring     int    // o grupo
	Source   string // "predio" ou "faixa"
	Rule     string // o prédio ou a faixa
	Conflict string // as regras discordam (o texto do aviso)
	Blocked  bool   // faixas do mesmo tamanho discordam: nenhuma decide
}

func addr4(a netip.Addr) uint32 {
	v := a.As4()
	return uint32(v[0])<<24 | uint32(v[1])<<16 | uint32(v[2])<<8 | uint32(v[3])
}

func (a *api) loadRingRules(ctx context.Context) (ringRules, error) {
	rr := ringRules{buildings: map[string]int{}}
	links, err := a.store.BuildingRings(ctx)
	if err != nil {
		return rr, err
	}
	for _, l := range links {
		rr.buildings[l.Building] = l.Ring
	}
	ips, err := a.store.IPRings(ctx)
	if err != nil {
		return rr, err
	}
	for _, r := range ips {
		rng, ok := parseIPRange(r.Spec)
		if !ok {
			a.logger.Warn("faixa de IP de grupo ilegível, ignorada", "spec", r.Spec, "id", r.ID)
			continue
		}
		rr.ips = append(rr.ips, ipRule{IPRing: r, rng: rng, size: addr4(rng.hi) - addr4(rng.lo)})
	}
	return rr, nil
}

// decideIP: a faixa mais específica que contém algum IP da máquina.
func (rr ringRules) decideIP(e equip) ruleDecision {
	var best []ipRule
	for _, r := range rr.ips {
		if !e.inRange(r.rng) {
			continue
		}
		switch {
		case len(best) == 0 || r.size < best[0].size:
			best = []ipRule{r}
		case r.size == best[0].size:
			best = append(best, r)
		}
	}
	if len(best) == 0 {
		return ruleDecision{}
	}
	d := ruleDecision{OK: true, Ring: best[0].Ring, Source: "faixa", Rule: best[0].Spec}
	for _, r := range best[1:] {
		if r.Ring != d.Ring {
			d.OK, d.Blocked = false, true
			d.Conflict = fmt.Sprintf("as faixas %s (grupo %d) e %s (grupo %d) discordam: a máquina fica onde está", best[0].Spec, best[0].Ring, r.Spec, r.Ring)
			break
		}
	}
	return d
}

// decide: para onde as regras mandam a máquina (sem contar a mão).
func (rr ringRules) decide(e equip) ruleDecision {
	ip := rr.decideIP(e)
	if b := e.Asset.Building; b != "" {
		if ring, ok := rr.buildings[b]; ok {
			d := ruleDecision{OK: true, Ring: ring, Source: "predio", Rule: b}
			switch {
			case ip.OK && ip.Ring != ring:
				d.Conflict = fmt.Sprintf("a faixa %s diz grupo %d; vale o prédio", ip.Rule, ip.Ring)
			case ip.Blocked:
				d.Conflict = ip.Conflict + "; vale o prédio"
			}
			return d
		}
	}
	return ip
}

// fleetEquip: as máquinas da frota com IPs e o bem do patrimônio (a lista do Inventário).
func (a *api) fleetEquip(ctx context.Context) ([]equip, error) {
	rows, err := a.store.InventoryList(ctx)
	if err != nil {
		return nil, err
	}
	all := buildEquip(rows, a.now())
	assets, err := a.assetMap(ctx)
	if err != nil {
		return nil, err
	}
	withAssets(all, assets)
	return all, nil
}

// applyRingRules põe cada máquina (não movida à mão) no grupo que as regras dizem; devolve
// quantas mudaram.
func (a *api) applyRingRules(ctx context.Context) (int, error) {
	rr, err := a.loadRingRules(ctx)
	if err != nil || rr.empty() {
		return 0, err
	}
	all, err := a.fleetEquip(ctx)
	if err != nil {
		return 0, err
	}
	target := map[string]int{}
	for _, e := range all {
		if d := rr.decide(e); d.OK && d.Ring != e.Ring {
			target[e.ID] = d.Ring
		}
	}
	return a.store.ApplyRings(ctx, target)
}

// WithBackground liga as tarefas do painel que rodam sozinhas (a passada das regras dos
// grupos de implantação) até o ctx acabar.
func WithBackground(ctx context.Context) Option {
	return func(a *api) { a.bg = ctx }
}

func (a *api) ringRuleLoop(ctx context.Context) {
	t := time.NewTicker(ringRuleEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		rctx, cancel := context.WithTimeout(ctx, time.Minute)
		n, err := a.applyRingRules(rctx)
		cancel()
		switch {
		case err != nil && ctx.Err() == nil:
			a.logger.Warn("regras dos grupos de implantação", "error", err)
		case n > 0:
			a.logger.Info("máquinas no grupo de implantação das regras", "movidas", n)
		}
	}
}
