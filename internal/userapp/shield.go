package userapp

import (
	"context"
	"fmt"
	"html/template"
	"math"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/localapi"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/ui"
)

// Atende+ Shield (docs/shield.md): a aba "Minha Proteção". Tudo vem do canal local (operação
// shield): o servidor calcula, a Central só desenha. Sem JavaScript e sem estilo inline: o
// anel é um SVG com stroke-dasharray (atributo, não estilo), e as cores vêm de classes, sempre
// acompanhadas de ícone e texto (a cor nunca é a única pista).

const tabShield = "protecao"

// shieldTTL: a Central reaproveita a última resposta por esse tempo (o menu de toda página
// precisa saber se a aba existe).
const shieldTTL = 2 * time.Minute

// IcEscudo é o ícone da aba (o mesmo do painel).
var IcEscudo = ui.IcEscudo

// shieldFetch devolve a "Minha Proteção" (nil: o agente, o servidor ou a política não têm),
// do cache curto, ou do agente com force.
func (a *App) shieldFetch(ctx context.Context, force bool) *protocol.ShieldView {
	a.shieldMu.Lock()
	if !force && !a.shieldAt.IsZero() && a.now().Sub(a.shieldAt) < shieldTTL {
		v := a.shieldView
		a.shieldMu.Unlock()
		return v
	}
	a.shieldMu.Unlock()
	resp, err := a.Call(ctx, localapi.Request{Op: localapi.OpShield})
	var v *protocol.ShieldView
	if err == nil && resp.OK && resp.Shield != nil && resp.Shield.Enabled && resp.Shield.Check() == nil {
		v = resp.Shield
	}
	a.shieldMu.Lock()
	a.shieldView, a.shieldAt = v, a.now()
	a.shieldMu.Unlock()
	return v
}

// ringCircumference: 2πr do anel (r = 52).
var ringCircumference = 2 * math.Pi * 52

// shieldPage é o que a aba desenha.
type shieldPage struct {
	Brand, Mission, Slogan string
	Display                int
	Dash                   string // o trecho preenchido do anel (stroke-dasharray)
	Ring                   string // classe: protegida, quase, atencao, risco, sem_dado
	Sealed, Pending        bool
	OverallLabel           string
	Evaluated              string
	Blockers               []string
	Criteria               []shieldCrit
	Actions                []shieldCrit // os que a pessoa resolve na própria Central
	Player                 *protocol.ShieldPlayerView
	LevelMax, LevelValue   int // para o <meter> do próximo nível
	Badges, Locked         []protocol.ShieldBadgeView
	Recent                 []shieldXP
	Missions               []shieldMission
	Rankings               []shieldRanking
}

type shieldCrit struct {
	Label, Status, StatusLabel, Icon, Detail, Blame, Deadline, Action, ActionLabel, ActionHref string
	Mandatory                                                                                  bool
	Points                                                                                     string
}

type shieldXP struct {
	Reason, Day string
	Points      int
}

type shieldMission struct {
	Title, Description, Status, StatusLabel, Progress, Period string
	Percent, XP                                               int
	Collective                                                bool
}

// statusIcon: o símbolo de cada situação (junto do texto).
func statusIcon(s string) string {
	switch s {
	case "conforme":
		return "✓"
	case "dentro_do_prazo":
		return "◷"
	case "aguardando_usuario":
		return "!"
	case "aguardando_ti", "parcial":
		return "…"
	case "nao_conforme":
		return "✕"
	case "excecao":
		return "≈"
	case "nao_aplicavel":
		return "–"
	}
	return "?"
}

func missionLabel(s string) string {
	switch s {
	case "ativa":
		return "em andamento"
	case "agendada":
		return "começa em breve"
	case "concluida":
		return "cumprida"
	}
	return s
}

// shieldPageOf monta a aba.
func (a *App) shieldPageOf(v *protocol.ShieldView) *shieldPage {
	p := &shieldPage{Brand: v.Brand, Mission: v.Mission, Slogan: v.Slogan, Player: v.Player}
	for _, place := range v.Places {
		p.Rankings = append(p.Rankings, shieldRankingOf(place, a.base()+"?aba="+tabUpdates))
	}
	if len(v.Groups) > 0 {
		group := protocol.ShieldPlaceView{Kind: "grupo", Title: "grupos", Rows: v.Groups}
		for _, row := range v.Groups {
			if row.Mine {
				group.Mine, group.Pos = row.Name, row.Pos
			}
		}
		p.Rankings = append(p.Rankings, shieldRankingOf(group, a.base()+"?aba="+tabUpdates))
	}
	loc := a.loc()
	if m := v.Machine; m != nil {
		p.Display, p.Sealed, p.Pending, p.OverallLabel, p.Blockers = m.Display, m.Sealed, m.Pending, m.OverallLabel, m.Blockers
		p.Ring = m.Overall
		if !m.EvaluatedAt.IsZero() {
			p.Evaluated = m.EvaluatedAt.In(loc).Format("02/01 15:04")
		}
		p.Dash = fmt.Sprintf("%.1f %.1f", ringCircumference*float64(m.Display)/100, ringCircumference)
		for _, c := range m.Criteria {
			x := shieldCrit{Label: c.Label, Status: c.Status, StatusLabel: c.StatusLabel, Icon: statusIcon(c.Status), Detail: c.Detail,
				Blame: c.BlameLabel, Mandatory: c.Mandatory, Action: c.Action}
			if c.Status != "nao_aplicavel" && c.Status != "excecao" {
				x.Points = fmt.Sprintf("%s de %s", trimFloat(c.Obtained), trimFloat(c.Weight))
			}
			if c.Deadline != nil {
				x.Deadline = c.Deadline.In(loc).Format("02/01 15:04")
			}
			switch c.Action {
			case "reiniciar":
				x.ActionLabel, x.ActionHref = "Escolher a hora do reinício", a.base()+"?aba="+tabReboot
			case "atualizar":
				x.ActionLabel, x.ActionHref = "Ver as atualizações", a.base()+"?aba="+tabUpdates
			}
			p.Criteria = append(p.Criteria, x)
			if x.ActionHref != "" {
				p.Actions = append(p.Actions, x)
			}
		}
	}
	if pl := v.Player; pl != nil {
		p.Badges, p.Locked = pl.Badges, pl.Locked
		if pl.NextLevelDays > 0 {
			p.LevelMax, p.LevelValue = pl.Protected+pl.NextLevelDays, pl.Protected
		}
		for _, e := range pl.Recent {
			p.Recent = append(p.Recent, shieldXP{Reason: e.Reason, Points: e.Points, Day: e.Day.Format("02/01")})
		}
	}
	for _, m := range v.Missions {
		p.Missions = append(p.Missions, shieldMission{Title: m.Title, Description: m.Description, Status: m.Status, StatusLabel: missionLabel(m.Status),
			Progress: m.Progress, Percent: m.Percent, XP: m.XP, Collective: m.Scope == "coletiva",
			Period: m.StartsAt.In(loc).Format("02/01") + " a " + m.EndsAt.In(loc).Format("02/01")})
	}
	return p
}

func trimFloat(f float64) string {
	s := strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", f), "0"), ".")
	return strings.Replace(s, ".", ",", 1)
}

// shieldTip é o texto do ícone perto do relógio.
func shieldTip(v *protocol.ShieldView) string {
	if v == nil || v.Machine == nil || v.Machine.Pending {
		return Title
	}
	if v.Machine.Sealed {
		return Title + " — 100% protegido"
	}
	return fmt.Sprintf("%s — proteção %d%% (%s)", Title, v.Machine.Display, v.Machine.OverallLabel)
}

// shieldNotifier decide os avisos do Shield: cada fato uma vez (pelo ID), no máximo um a cada
// MinHours, e só no expediente.
type shieldNotifier struct {
	seen map[string]bool
	last time.Time
}

func (s *shieldNotifier) pick(v *protocol.ShieldView, now time.Time) *protocol.ShieldNotice {
	if s.seen == nil {
		s.seen = map[string]bool{}
	}
	if v == nil || !v.Notify.Enabled {
		return nil
	}
	if h := now.Hour(); h < v.Notify.WorkStart || h >= v.Notify.WorkEnd {
		return nil
	}
	if !s.last.IsZero() && now.Sub(s.last) < time.Duration(max(v.Notify.MinHours, 1))*time.Hour {
		return nil
	}
	for _, n := range v.Notices {
		if !s.seen[n.ID] {
			s.seen[n.ID] = true
			s.last = now
			return &n
		}
	}
	return nil
}

// WatchShield confere a "Minha Proteção" a cada every: atualiza o texto do ícone e manda os
// avisos que passam pelas regras (shieldNotifier). Sem Shield, só mantém o texto padrão.
func (a *App) WatchShield(ctx context.Context, first, every time.Duration, out chan<- Notice) {
	var n shieldNotifier
	wait := first
	lastTip := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = every
		cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		v := a.shieldFetch(cctx, true)
		cancel()
		var notice Notice
		if tip := shieldTip(v); tip != lastTip {
			notice.Tip, lastTip = tip, tip
		}
		if s := n.pick(v, a.now().In(a.loc())); s != nil {
			notice.Shield = s
		}
		if notice.Tip != "" || notice.Shield != nil {
			select {
			case out <- notice:
			default:
			}
		}
	}
}

var _ = template.Must(pageTmpl.New("protecao").Parse(`{{with .Shield}}
<div class="shield-dashboard"><section class="shield"><div class="shield-topo">
<svg class="anel {{.Ring}}" viewBox="0 0 120 120" width="132" height="132" role="img" aria-label="proteção {{.Display}}%">
<circle class="trilho" cx="60" cy="60" r="52" fill="none" stroke-width="12"/>
<circle class="valor" cx="60" cy="60" r="52" fill="none" stroke-width="12" stroke-linecap="round" stroke-dasharray="{{.Dash}}" transform="rotate(-90 60 60)"/>
<text x="60" y="66" text-anchor="middle" class="num">{{if .Pending}}—{{else}}{{.Display}}%{{end}}</text></svg>
<div><span class="shield-eyebrow">MINHA PROTEÇÃO</span><h1>{{.Brand}}</h1><p class="shield-mission-title">{{.Mission}}</p>
{{if .Pending}}<p class="estado sem_dado">Este computador ainda está sendo avaliado. Volte em alguns minutos.</p>
{{else if .Sealed}}<p class="estado protegida">✓ <b>Selo 100%</b>: todos os critérios aplicáveis foram comprovados.</p>
{{else}}<p class="estado {{.Ring}}">{{if eq .Ring "risco"}}✕{{else if eq .Ring "sem_dado"}}?{{else}}!{{end}} <b>{{.OverallLabel}}</b>: faltam {{len .Blockers}} item(ns) para o selo 100%.</p>{{end}}
{{if .Evaluated}}<p class="nota">Última avaliação: {{.Evaluated}} · Dados verificados pelo servidor.</p>{{end}}</div></div>
{{if .Actions}}<div class="acoes"><h2>O que você pode fazer agora</h2><ul class="lista">{{range .Actions}}<li><div><strong>{{.Label}}</strong><span class="ver">{{.Detail}}{{if .Deadline}} · prazo: {{.Deadline}}{{end}}</span></div><a class="btn" href="{{.ActionHref}}">{{.ActionLabel}}</a></li>{{end}}</ul></div>{{end}}
</section>
{{with .Player}}<section><span class="shield-eyebrow">SUA JORNADA</span><h2>Seu progresso</h2><ul class="kpis-c">
<li><span class="rot">Sequência</span><b>{{.Streak}}</b><small>dias úteis seguidos (melhor: {{.Best}})</small></li>
<li><span class="rot">Dias protegidos</span><b>{{.Protected}}</b><small>no total</small></li>
<li><span class="rot">XP</span><b>{{.XP}}</b><small>pontos de experiência</small></li>
<li><span class="rot">Nível</span><b>{{if .Level}}{{.Level}}{{else}}—{{end}}</b><small>{{if .NextLevel}}faltam {{.NextLevelDays}} dia(s) para {{.NextLevel}}{{else}}nível máximo{{end}}</small></li></ul>
{{if $.Shield.LevelMax}}<label class="nivel">Rumo a {{.NextLevel}} <meter min="0" max="{{$.Shield.LevelMax}}" value="{{$.Shield.LevelValue}}">{{$.Shield.LevelValue}} de {{$.Shield.LevelMax}}</meter></label>{{end}}
<p class="nota">A sequência conta os dias úteis com o computador no selo 100% no fim do dia. Pendência da TI ou da automação pausa a sequência, não quebra; o que é seu e passou do prazo quebra.</p></section>{{end}}
{{if .Criteria}}<section><span class="shield-eyebrow">SAÚDE DO COMPUTADOR</span><h2>Como a pontuação é formada</h2><ul class="lista crit">{{range .Criteria}}<li><div><strong>{{.Label}}{{if .Mandatory}} <span class="ver">(obrigatório para o selo)</span>{{end}}</strong>
<span class="ver">{{.Detail}}{{if .Blame}} · responsável: {{.Blame}}{{end}}{{if .Deadline}} · prazo: {{.Deadline}}{{end}}</span></div>
<span class="st {{.Status}}"><span><span aria-hidden="true">{{.Icon}}</span> {{.StatusLabel}}</span>{{if .Points}}<small>{{.Points}} pontos</small>{{end}}</span></li>{{end}}</ul></section>{{end}}
{{if .Missions}}<section><h2>Missões</h2><ul class="lista">{{range .Missions}}<li><div><strong>{{.Title}}</strong><span class="ver">{{if .Collective}}missão coletiva · {{end}}{{.Period}} · {{.StatusLabel}}{{if .XP}} · {{.XP}} XP{{end}}</span>{{if .Description}}<span class="ver">{{.Description}}</span>{{end}}
<progress max="100" value="{{.Percent}}">{{.Percent}}%</progress><span class="ver">{{.Progress}}</span></div></li>{{end}}</ul></section>{{end}}
{{if or .Badges .Locked}}<section><span class="shield-eyebrow">COLEÇÃO DE MEDALHAS</span><h2>Conquistas</h2><ul class="medalhas">{{range .Badges}}<li class="ganha"><span class="shield-medal" aria-hidden="true"><svg viewBox="0 0 64 72" width="54" height="60"><path d="M12 3h16l9 26-15 8zM36 3h16L42 37l-15-8z" fill="#2563eb"/><circle cx="32" cy="46" r="23" fill="#f5c451" stroke="#a66c13" stroke-width="2"/><circle cx="32" cy="46" r="17" fill="#fff1b8" stroke="#d69e2e"/><path d="m23 46 6 6 12-13" fill="none" stroke="#81520d" stroke-width="4" stroke-linecap="round" stroke-linejoin="round"/></svg></span><strong>{{.Name}}</strong><small>{{.Description}}</small></li>{{end}}
{{range .Locked}}<li class="falta"><span class="shield-medal" aria-hidden="true"><svg viewBox="0 0 64 72" width="54" height="60"><path d="M12 3h16l9 26-15 8zM36 3h16L42 37l-15-8z" fill="#94a3b8"/><circle cx="32" cy="46" r="23" fill="#e2e8f0" stroke="#94a3b8" stroke-width="2"/><rect x="24" y="43" width="16" height="13" rx="3" fill="#64748b"/><path d="M27 43v-5a5 5 0 0 1 10 0v5" fill="none" stroke="#64748b" stroke-width="3"/></svg></span><strong>{{.Name}}</strong><small>{{.Description}}</small></li>{{end}}</ul></section>{{end}}
{{range .Rankings}}{{template "shield-ranking" .}}{{end}}
{{if .Rankings}}<p class="nota">Os rankings contam só computadores: ninguém vê o seu nome nem o seu computador. Locais com poucos computadores não aparecem.</p>{{end}}
{{if .Recent}}<section><h2>De onde veio seu XP</h2><ul class="lista">{{range .Recent}}<li><div><strong>{{.Reason}}</strong><span class="ver">{{.Day}}</span></div><span class="selo {{if lt .Points 0}}atencao{{else}}sucesso{{end}}">{{if gt .Points 0}}+{{end}}{{.Points}} XP</span></li>{{end}}</ul></section>{{end}}
{{if .Slogan}}<p class="slogan">{{.Slogan}}</p>{{end}}
</div>{{end}}` + shieldRankingTemplate))

// shieldCSS: o estilo da aba (no estilo.css, sem inline).
const shieldCSS = `
.shield-topo{display:flex;gap:1.25rem;align-items:center;flex-wrap:wrap}.shield-topo>div{flex:1;min-width:15rem}.shield-topo h1{margin:0 0 .4rem}
svg.anel .trilho{stroke:#e6e9ef}svg.anel .valor{stroke:#8a94a6}svg.anel .num{font-size:24px;font-weight:700;fill:var(--texto)}
svg.anel.protegida .valor{stroke:#1f8a4c}svg.anel.quase .valor{stroke:#2f6fd0}svg.anel.atencao .valor{stroke:#c98a00}svg.anel.risco .valor{stroke:#c0392b}
.estado{margin:.2rem 0 .4rem;padding:.4rem .7rem;border-radius:6px;border:1px solid var(--borda)}
.estado.protegida{background:#eaf7ee;color:#14532d;border-color:#bfe5c9}.estado.quase{background:#eef5fd;color:#0b3f73;border-color:#c3dbf5}
.estado.atencao{background:#fff6e0;color:#7a4b00;border-color:#f6d68b}.estado.risco{background:#fdecea;color:#7f1d1d;border-color:#f5c2bc}.estado.sem_dado{background:#f3f4f6;color:#374151}
.acoes{margin-top:.75rem}.st{white-space:nowrap;font-size:.85rem;display:flex;flex-direction:column;align-items:flex-end;gap:.1rem}.st small{color:var(--muted)}
.st.conforme{color:#14532d}.st.nao_conforme{color:#7f1d1d}.st.aguardando_usuario{color:#7a4b00;font-weight:600}.st.desconhecido{color:#374151}
ul.kpis-c{list-style:none;margin:0 0 .75rem;padding:0;display:grid;grid-template-columns:repeat(auto-fit,minmax(10rem,1fr));gap:.6rem}
ul.kpis-c li{border:1px solid var(--borda);border-radius:var(--raio);padding:.6rem .75rem;display:flex;flex-direction:column}
ul.kpis-c .rot{color:var(--muted);font-size:.8rem}ul.kpis-c b{font-size:1.25rem}ul.kpis-c small{color:var(--muted);font-size:.78rem}
label.nivel{display:flex;align-items:center;gap:.6rem;font-size:.85rem;color:var(--muted)}label.nivel meter{flex:1;height:1rem}
progress{width:100%;height:.8rem;margin:.35rem 0 .1rem}
ul.medalhas{list-style:none;margin:0;padding:0;display:grid;grid-template-columns:repeat(auto-fill,minmax(12rem,1fr));gap:.6rem}
ul.medalhas li{border:1px solid var(--borda);border-radius:var(--raio);padding:.55rem .7rem;display:flex;flex-direction:column;gap:.15rem}
ul.medalhas li.ganha{background:#fffbea;border-color:#f3dc8a}ul.medalhas li.falta{color:var(--muted)}ul.medalhas small{font-size:.78rem}
ul.lista li.meu{background:#eef5fd;margin:0 -1.25rem;padding:.7rem 1.25rem}
.destaque{margin:0 0 .6rem;padding:.45rem .7rem;border-radius:6px;background:#eef5fd;border:1px solid #c3dbf5;color:#0b3f73}
.slogan{text-align:center;color:var(--muted);font-style:italic;margin:1.5rem 0 0}

/* Shield dashboard: visual hierarchy, local SVG, no inline style. */
.shield-dashboard{display:grid;gap:1rem}
.shield-dashboard>section{margin:0;border:1px solid var(--borda);border-radius:18px;padding:1.4rem;box-shadow:0 3px 12px #142c4910}
.shield-dashboard .shield{background:linear-gradient(125deg,#0b2545,#174a79 72%,#146c80);color:#f8fafc;border:0;padding:1.65rem}
.shield-dashboard .shield-topo{gap:1.8rem}
.shield-dashboard .shield-topo h1{color:#fff;font-size:clamp(1.65rem,4vw,2.4rem);letter-spacing:-.04em;margin:.25rem 0}
.shield-eyebrow{display:block;font-size:.68rem;font-weight:750;letter-spacing:.13em;color:var(--muted);margin-bottom:.35rem}
.shield .shield-eyebrow{color:#b9d9f6}.shield-mission-title{font-size:1rem;color:#c7e3ff;margin:0 0 .85rem}
.shield .anel{width:160px;height:160px;flex:none}.shield .anel .trilho{stroke:#ffffff25}.shield .anel .num{fill:#fff;font-size:27px}
.shield .anel.protegida .valor{stroke:#5ee3a1}.shield .anel.quase .valor{stroke:#8bcaff}.shield .anel.atencao .valor{stroke:#fbd36e}.shield .anel.risco .valor{stroke:#ff9c9c}
.shield .estado{border-radius:10px;padding:.6rem .85rem;font-size:.9rem}.shield .nota{color:#c8def1}
.shield .acoes{background:#ffffff10;border:1px solid #ffffff30;border-radius:12px;padding:1rem;margin-top:1.2rem}.shield .acoes h2,.shield .acoes strong{color:#fff}.shield .acoes .ver{color:#d1e5f8}.shield .acoes li{border-color:#ffffff30}
.shield-dashboard h2{font-size:1.15rem;letter-spacing:-.02em;margin:.15rem 0 1rem}
.shield-dashboard ul.kpis-c{grid-template-columns:repeat(auto-fit,minmax(8.5rem,1fr));gap:.7rem}
.shield-dashboard ul.kpis-c li{padding:1rem;border-radius:12px;background:linear-gradient(140deg,#2563eb0c,transparent);gap:.4rem;border-top:3px solid #3b82f6}
.shield-dashboard ul.kpis-c li:nth-child(2){border-top-color:#16a34a}.shield-dashboard ul.kpis-c li:nth-child(3){border-top-color:#d97706}.shield-dashboard ul.kpis-c li:nth-child(4){border-top-color:#7c3aed}
.shield-dashboard ul.kpis-c b{font-size:1.65rem;letter-spacing:-.04em;font-variant-numeric:tabular-nums}
.shield-dashboard label.nivel{background:#2563eb0c;border-radius:10px;padding:.8rem;margin:.8rem 0;flex-wrap:wrap}
.shield-dashboard meter,.shield-dashboard progress{accent-color:#2563eb}
.shield-dashboard .crit li{padding:.85rem 0;gap:.8rem}.shield-dashboard .st{white-space:normal;text-align:right;max-width:45%}
.shield-dashboard ul.medalhas{grid-template-columns:repeat(auto-fill,minmax(10rem,1fr));gap:.85rem}
.shield-dashboard ul.medalhas li{align-items:center;text-align:center;padding:1rem .65rem;min-height:9rem;border-radius:14px;gap:.4rem}
.shield-dashboard ul.medalhas li.ganha{background:linear-gradient(155deg,#f5c45119,transparent);border-color:#d6a84d}
.shield-dashboard ul.medalhas li.falta{background:#94a3b80a;border-style:dashed}
.shield-dashboard .shield-medal{display:grid;place-items:center;height:66px}
.shield-dashboard .medalhas strong{font-size:.9rem}.shield-dashboard .medalhas small{line-height:1.4}
.shield-dashboard .lista li.meu{border-radius:8px;margin:0;padding:.7rem;color:#0b3f73}
.shield-dashboard .slogan{letter-spacing:.02em;margin:.5rem 0 1rem}
@media(max-width:560px){.shield-dashboard>section{padding:1rem}.shield-dashboard .shield-topo{justify-content:center;text-align:center;gap:.8rem}.shield-dashboard .shield-topo>div{min-width:0;flex-basis:100%}.shield-dashboard .shield{padding:1.1rem}}
` + shieldRankingCSS
