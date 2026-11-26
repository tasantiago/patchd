package userapp

import "github.com/tasantiago/patchd/internal/protocol"

// O servidor calcula a classificação. A apresentação não reordena os lugares, estima
// computadores protegidos a partir de percentuais arredondados nem muda a escala das barras.
type shieldRanking struct {
	Title, Unit, MineLabel, UpdatesHref string
	Mine                                string
	Pos, Of                             int
	Own                                 *protocol.ShieldGroupView
	Podium                              []protocol.ShieldGroupView
	Rows                                []shieldRankingRow
}

type shieldRankingRow struct {
	protocol.ShieldGroupView
	Gap bool
}

func shieldRankingOf(v protocol.ShieldPlaceView, updatesHref string) shieldRanking {
	r := shieldRanking{Title: v.Title, Mine: v.Mine, Pos: v.Pos, Of: v.Of, UpdatesHref: updatesHref,
		Unit: "locais", MineLabel: "Seu local"}
	switch v.Kind {
	case "predio":
		r.Unit, r.MineLabel = "prédios", "Seu prédio"
	case "andar":
		r.Unit, r.MineLabel = "andares", "Seu andar"
	case "sala":
		r.Unit, r.MineLabel = "salas", "Sua sala"
	case "grupo":
		r.Unit, r.MineLabel = "grupos", "Seu grupo"
	}
	// Só há pódio com as três posições presentes. Rankings pequenos e respostas parciais
	// usam a lista, sem cartões vazios ou participantes inventados.
	var leaders [3]*protocol.ShieldGroupView
	for _, row := range v.Rows {
		if row.Mine {
			own := row
			r.Own = &own
		}
		if row.Pos >= 1 && row.Pos <= 3 {
			leader := row
			leaders[row.Pos-1] = &leader
		}
	}
	if leaders[0] != nil && leaders[1] != nil && leaders[2] != nil {
		for _, leader := range leaders {
			r.Podium = append(r.Podium, *leader)
		}
	}
	previous := len(r.Podium)
	for _, row := range v.Rows {
		if len(r.Podium) > 0 && row.Pos <= 3 {
			continue
		}
		r.Rows = append(r.Rows, shieldRankingRow{ShieldGroupView: row, Gap: row.Pos > previous+1})
		previous = row.Pos
	}
	return r
}

const shieldRankingTemplate = `
{{define "rank-trophy"}}<svg viewBox="0 0 32 32" width="28" height="28" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M10 5h12v7a6 6 0 0 1-12 0V5ZM10 7H5v3a5 5 0 0 0 6 5M22 7h5v3a5 5 0 0 1-6 5M16 18v6m-5 3h10m-8-3h6"/><path d="m16 8 .9 1.8 2 .3-1.4 1.4.3 2-1.8-.9-1.8.9.3-2-1.4-1.4 2-.3Z" fill="currentColor" stroke="none"/></svg>{{end}}
{{define "rank-bar"}}<svg class="rank-bar" viewBox="0 0 100 6" preserveAspectRatio="none" role="img" aria-label="{{.Percent}}% dos computadores no selo 100%"><rect class="rank-track" width="100" height="6" rx="3"/><rect class="rank-fill" width="{{.Percent}}" height="6" rx="3"/></svg>{{end}}
{{define "shield-ranking"}}
<section class="shield-ranking">
  <header class="rank-heading">
    <span class="rank-icon">{{template "rank-trophy"}}</span>
    <div><span class="shield-eyebrow">PROTEÇÃO EM EQUIPE</span><h2>Ranking: {{.Title}}</h2><p>Cada computador protegido faz a diferença.</p></div>
    {{if .Of}}<span class="rank-count">{{.Of}} {{.Unit}}</span>{{end}}
  </header>
  {{if .Pos}}<div class="rank-own">
    <div class="rank-position"><span class="rank-own-label">SUA POSIÇÃO</span><strong>{{.Pos}}<span>º</span></strong>{{if .Of}}<span>de {{.Of}}</span>{{end}}</div>
    <div class="rank-own-copy"><span class="rank-own-label">{{.MineLabel}}</span><h3>{{.Mine}}</h3><p>{{if eq .Pos 1}}Na liderança! Vamos continuar cuidando dessa conquista.{{else}}Vamos subir juntos? Cada computador no selo 100% conta.{{end}}</p></div>
    {{with .Own}}<div class="rank-own-score"><strong>{{.Percent}}<span>%</span></strong><span>no selo 100%</span>{{template "rank-bar" .}}</div>{{end}}
  </div>{{end}}
  {{if .Podium}}<ol class="rank-podium" aria-label="Três primeiros colocados">{{range .Podium}}
    <li class="rank-podium-card rank-top-{{.Pos}}{{if .Mine}} rank-is-mine{{end}}" value="{{.Pos}}">
      <span class="rank-medallion" aria-label="{{.Pos}}º lugar">{{if eq .Pos 1}}{{template "rank-trophy"}}{{else}}{{.Pos}}º{{end}}</span>
      <span class="rank-podium-label">{{if eq .Pos 1}}1º LUGAR{{else if eq .Pos 2}}2º LUGAR{{else}}3º LUGAR{{end}}</span>
      <strong class="rank-name">{{.Name}}</strong>{{if .Mine}}<span class="rank-you">{{$.MineLabel}}</span>{{end}}
      <span class="rank-machines">{{.Machines}} {{if eq .Machines 1}}computador{{else}}computadores{{end}}</span>
      <span class="rank-podium-score"><b>{{.Percent}}%</b> no selo</span>{{template "rank-bar" .}}
    </li>{{end}}</ol>{{end}}
  {{if .Rows}}<div class="rank-list-heading"><h3>{{if .Podium}}Na disputa{{else}}Classificação{{end}}</h3><span>Computadores no selo 100%</span></div>
  <ol class="rank-list" aria-label="Classificação de {{.Title}}">{{range .Rows}}
    {{if .Gap}}<li class="rank-gap" aria-label="Posições intermediárias não exibidas"><span aria-hidden="true">•••</span></li>{{end}}
    <li class="rank-row{{if .Mine}} rank-is-mine{{end}}" value="{{.Pos}}">
      <span class="rank-number">{{.Pos}}<small>º</small></span>
      <div class="rank-detail"><strong class="rank-name">{{.Name}}</strong>{{if .Mine}}<span class="rank-you">{{$.MineLabel}}</span>{{end}}<span class="rank-machines">{{.Machines}} {{if eq .Machines 1}}computador{{else}}computadores{{end}}</span></div>
      <div class="rank-row-score"><span><b>{{.Percent}}%</b> no selo</span>{{template "rank-bar" .}}</div>
    </li>{{end}}</ol>{{end}}
  <footer class="rank-footer"><div><strong>Sua parte fortalece a equipe</strong><p>Confira suas atualizações e ajude a construir um ambiente mais seguro.</p></div><a class="btn rank-action" href="{{.UpdatesHref}}">Ver minhas atualizações <svg viewBox="0 0 20 20" width="14" height="14" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M5 15 15 5M5 5h10v10"/></svg></a></footer>
</section>
{{end}}`

const shieldRankingCSS = `
.shield-ranking{--rank-ink:#163457;--rank-blue:#3569ce;--rank-gold:#a86d12;--rank-soft:#f5f7fb}
.rank-heading{display:flex;align-items:center;gap:.85rem;margin-bottom:1.25rem;min-width:0}
.rank-heading>div{min-width:0;flex:1}.rank-heading .shield-eyebrow{color:#626d83;font-size:.65rem;letter-spacing:.16em}
.shield-dashboard .rank-heading h2{margin:0;color:var(--rank-ink);font-size:1.35rem;overflow-wrap:anywhere}
.rank-heading p{margin:.2rem 0 0;color:var(--muted);font-size:.85rem}
.rank-icon{display:grid;place-items:center;width:3rem;height:3rem;flex:none;border-radius:14px;background:#fff4d9;color:#9a6816;border:1px solid #f4e3b8}
.rank-count{font-size:.75rem;color:#5c6980;border:1px solid var(--borda);border-radius:999px;padding:.3rem .65rem;white-space:nowrap;background:#fafbfd}
.rank-own{position:relative;display:flex;align-items:center;gap:1.25rem;padding:1.25rem 1.4rem;border-radius:14px;background:linear-gradient(115deg,#172f54,#234d7e);color:#fff;overflow:hidden}
.rank-position{display:flex;flex-direction:column;align-items:center;flex:none;padding-right:1.25rem;border-right:1px solid #ffffff30}
.rank-own-label{font-size:.65rem;letter-spacing:.12em;color:#cee0fa;line-height:1.5}
.rank-position>strong{font-size:2.8rem;font-weight:750;line-height:1.15;letter-spacing:-.06em;font-variant-numeric:tabular-nums}
.rank-position strong span{font-size:1.45rem;vertical-align:super;margin-left:.1rem}.rank-position>span:last-child:not(:first-child){font-size:.75rem;color:#cee0fa}
.rank-own-copy{flex:1;min-width:0}.rank-own-copy h3{font-size:1.05rem;line-height:1.4;margin:.15rem 0 .35rem;color:#fff;overflow-wrap:anywhere}
.rank-own-copy p{font-size:.8rem;color:#d6e5f9;line-height:1.5;margin:0;max-width:30rem}
.rank-own-score{display:flex;flex-direction:column;align-items:flex-end;gap:.2rem;min-width:6rem;flex:none}.rank-own-score>strong{font-size:1.9rem;line-height:1.2;letter-spacing:-.04em}.rank-own-score>strong span{font-size:1.1rem;margin-left:.1rem}
.rank-own-score>span{font-size:.72rem;color:#d6e5f9}.rank-own-score .rank-bar{width:6rem;margin-top:.35rem}.rank-own .rank-track{fill:#ffffff30}.rank-own .rank-fill{fill:#91c9ff}
.rank-bar{display:block;width:100%;height:6px;border-radius:999px;overflow:hidden;flex:none}.rank-track{fill:#e7edf5}.rank-fill{fill:var(--rank-blue)}
ol.rank-podium{list-style:none;margin:1.6rem 0 1.4rem;padding:0;display:grid;grid-template-columns:repeat(3,minmax(0,1fr));align-items:end;gap:.75rem}
.rank-podium-card{display:flex;flex-direction:column;align-items:center;justify-content:center;text-align:center;gap:.35rem;position:relative;min-width:0;min-height:12.5rem;padding:1rem .85rem .9rem;border:1px solid #e1e7ef;border-radius:14px;background:linear-gradient(160deg,#f8fafc,#fff);border-top:3px solid #b7c5d8}
.rank-top-1{grid-column:2;grid-row:1;min-height:14rem;border-color:#e8d3a3;border-top-color:#d5a143;background:linear-gradient(160deg,#fff5d9,#fffdf6 70%);box-shadow:0 5px 16px #a8781710}
.rank-top-2{grid-column:1;grid-row:1}.rank-top-3{grid-column:3;grid-row:1;border-top-color:#bc977d;background:linear-gradient(160deg,#fdf5ef,#fff)}
.rank-medallion{display:grid;place-items:center;flex:none;width:2.5rem;height:2.5rem;border-radius:50%;color:#506580;background:#e7edf5;border:1px solid #cfdae7;font-size:1.05rem;font-weight:700;margin-bottom:.1rem}
.rank-top-1 .rank-medallion{width:3rem;height:3rem;color:#94600e;background:#fbe4a5;border-color:#e8c777;box-shadow:0 0 0 5px #fff4d6}
.rank-top-3 .rank-medallion{background:#f1e0d2;color:#876047;border-color:#dec5b3}
.rank-podium-label{font-size:.6rem;font-weight:700;letter-spacing:.12em;color:#64748b}.rank-top-1 .rank-podium-label{color:#92620f}
.rank-name{font-size:.9rem;font-weight:650;color:var(--rank-ink);overflow-wrap:anywhere;line-height:1.4}
.rank-machines{display:block;font-size:.74rem;color:#66748b;line-height:1.4}
.rank-podium-score{display:block;color:#66748b;font-size:.73rem;margin-top:auto;padding-top:.45rem}.rank-podium-score b{color:var(--rank-ink);font-size:1.2rem;font-weight:750;letter-spacing:-.03em;margin-right:.15rem;font-variant-numeric:tabular-nums}
.rank-podium .rank-bar{margin-top:.2rem}.rank-top-1 .rank-fill{fill:#bb861f}.rank-top-3 .rank-fill{fill:#ac7a59}
.rank-list-heading{display:flex;align-items:center;justify-content:space-between;gap:1rem;margin:1rem 0 .4rem}
.rank-list-heading h3{font-size:.86rem;color:var(--rank-ink);margin:0}.rank-list-heading>span{font-size:.7rem;color:#66748b}
ol.rank-list{list-style:none;padding:0;margin:0;display:grid;gap:.35rem}
.rank-row{display:grid;grid-template-columns:2.35rem minmax(0,1fr) 9rem;align-items:center;gap:.75rem;border:1px solid transparent;border-bottom-color:#edf0f5;border-radius:9px;padding:.8rem .7rem;min-width:0}
.rank-number{display:grid;grid-template-columns:auto auto;justify-content:center;align-items:start;gap:.05rem;background:#f2f5f9;border-radius:9px;padding:.4rem .25rem;font-size:.95rem;font-weight:650;color:#5d6c83;font-variant-numeric:tabular-nums}.rank-number small{font-size:.65rem;padding-top:.1rem}
.rank-detail{min-width:0;line-height:1.6}.rank-detail .rank-name{vertical-align:middle}.rank-detail .rank-machines{margin-top:.15rem}
.rank-you{display:inline-block;vertical-align:middle;border-radius:999px;padding:.1rem .45rem;background:#dce9ff;color:#254f92;font-size:.63rem;font-weight:650;line-height:1.5;margin-left:.35rem;white-space:nowrap}
.rank-podium .rank-you{margin:0}.rank-is-mine.rank-row{background:#eff5ff;border-color:#c9dcfb;border-left:3px solid #4a79c9;padding-left:calc(.7rem - 2px)}
.rank-is-mine .rank-number{background:#dbe8fc;color:#254f92}.rank-is-mine.rank-podium-card{outline:2px solid #4a79c9;outline-offset:2px}
.rank-row-score{min-width:0;text-align:right}.rank-row-score>span{display:block;font-size:.72rem;color:#66748b;margin-bottom:.35rem}.rank-row-score b{font-size:.95rem;color:var(--rank-ink);font-weight:700;font-variant-numeric:tabular-nums}
.rank-gap{list-style:none;text-align:center;color:#7a879c;font-size:.8rem;letter-spacing:.15em;padding:.15rem}
.rank-footer{display:flex;align-items:center;gap:1rem;justify-content:space-between;margin-top:1.3rem;padding:1rem 0 0;border-top:1px solid #e7edf5}
.rank-footer>div{min-width:0}.rank-footer strong{font-size:.82rem;color:var(--rank-ink)}.rank-footer p{font-size:.75rem;color:#66748b;line-height:1.5;margin:.2rem 0 0;max-width:28rem}
.shield-ranking a.rank-action{background:#eef4ff;color:#2956a0;border-color:#cfddf5;font-size:.76rem;font-weight:600;border-radius:8px;padding:.6rem .8rem;white-space:normal;text-align:center;flex:none}.shield-ranking a.rank-action:hover{background:#e1ecff;border-color:#abc4ed}.rank-action svg{margin-left:.35rem;vertical-align:middle}
@media(max-width:700px){.rank-own{gap:1rem;padding:1.1rem}.rank-position{padding-right:1rem}.rank-position>strong{font-size:2.4rem}.rank-own-score{min-width:4.5rem}.rank-own-score .rank-bar{width:4.5rem}.rank-row{grid-template-columns:2.1rem minmax(0,1fr) 6.5rem;gap:.6rem}.rank-count{font-size:.68rem}.rank-footer{flex-wrap:wrap}.rank-footer .rank-action{flex:none}}
@media(max-width:480px){.rank-heading{gap:.6rem;flex-wrap:wrap}.rank-heading>div{flex-basis:calc(100% - 4rem)}.rank-heading .rank-count{margin-left:3.6rem;margin-top:-.4rem}.shield-dashboard .rank-heading h2{font-size:1.12rem}.rank-heading p{font-size:.78rem}.rank-icon{width:2.8rem;height:2.8rem}.rank-own{flex-wrap:wrap;gap:.8rem}.rank-position{width:4rem}.rank-own-copy{flex-basis:calc(100% - 6rem)}.rank-own-copy h3{font-size:.95rem}.rank-own-score{width:100%;flex-direction:row;align-items:center;gap:.5rem;border-top:1px solid #ffffff25;padding-top:.7rem}.rank-own-score>strong{font-size:1.35rem}.rank-own-score>strong span{font-size:.9rem}.rank-own-score .rank-bar{flex:1;width:auto;margin:0 0 0 .3rem}
ol.rank-podium{grid-template-columns:1fr;margin-top:1rem;gap:.6rem}.rank-podium-card{grid-column:auto;grid-row:auto;display:grid;grid-template-columns:2.5rem minmax(0,1fr) auto;text-align:left;min-height:0;padding:.8rem;gap:.15rem .65rem;border-top-width:1px;border-left:3px solid #b7c5d8}.rank-top-1{border-left-color:#d5a143}.rank-top-3{border-left-color:#bc977d}.rank-medallion,.rank-top-1 .rank-medallion{grid-column:1;grid-row:1/5;width:2.5rem;height:2.5rem;box-shadow:none;margin:0}.rank-podium-label{grid-column:2;grid-row:1}.rank-podium .rank-name{grid-column:2;grid-row:2}.rank-podium .rank-machines{grid-column:2;grid-row:3}.rank-podium .rank-you{grid-column:2;grid-row:4;justify-self:start}.rank-podium-score{grid-column:3;grid-row:1/3;text-align:right;padding:0;margin:0;align-self:center;max-width:4rem}.rank-podium-score b{display:block;margin:0}.rank-podium .rank-bar{grid-column:3;grid-row:3;align-self:center;min-width:3.5rem}
.rank-list-heading>span{display:none}.rank-row{grid-template-columns:1.8rem minmax(0,1fr) 4.5rem;gap:.5rem;padding:.75rem .35rem}.rank-is-mine.rank-row{padding-left:calc(.35rem - 2px)}.rank-number{font-size:.85rem}.rank-row .rank-name{font-size:.82rem}.rank-machines{font-size:.7rem}.rank-detail .rank-you{margin-left:0;display:table;margin-top:.2rem}.rank-footer a.rank-action{width:100%}}
`
