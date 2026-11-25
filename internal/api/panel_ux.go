package api

// panelUXCSS is layered after the existing panel theme. It changes presentation
// only; the shared agent UI, semantic status classes and CSP stay unchanged.
const panelUXCSS = `
:root{--marinho:#10233b;--marinho-2:#1a3554;--marinho-3:#24466a;--lilas:#a9c9f2;--lado-texto:#c5d3e5;--rosa:#b83c66;--destaque:#245ab4;--fundo:#f3f6fa;--borda:#dce4ed;--texto:#172b43;--muted:#526378;--azul:#245ab4;--azul-2:#17458f;--verde-ok:#157347;--laranja:#9a5900;--vermelho:#b4232b;--raio:12px;--sombra:0 2px 8px #162c4310}
body{line-height:1.55}
.lado{background:linear-gradient(165deg,#10233b,#142f4e 75%,#10233b)}
.lado .marca{padding-top:.85rem;padding-bottom:.85rem;letter-spacing:-.015em}
.lado .escudo{background:#285eb1;border-radius:11px}
.lado .usuario{padding-top:.9rem;padding-bottom:.9rem}
.lado .grupo{letter-spacing:.1em;color:#a9c9f2}
.lado nav a{border-radius:8px;min-height:2.55rem}
.lado nav a.ativo{background:#274e77;box-shadow:inset 3px 0 0 #8cbcf8}
.barra-topo{box-shadow:0 1px 6px #172b430d}
.trilha .titulo{color:#172b43}
main>h1{font-size:clamp(1.45rem,2vw,1.8rem);letter-spacing:-.035em;font-weight:700;margin:.15rem 0 1.15rem}
h2{font-size:1.08rem;font-weight:650;letter-spacing:-.015em}
.card,section{box-shadow:var(--sombra)}
.card-cab{background:#fff;border-radius:var(--raio) var(--raio) 0 0;min-height:3.2rem}
.kpi{position:relative;padding:1.15rem 1.25rem;border-top:3px solid #8ea9ce}
a.kpi:hover{border-color:#8aa6cb;box-shadow:0 4px 12px #24466a18}
.kpi-rot{font-weight:650;letter-spacing:.045em}
.kpi-val{font-variant-numeric:tabular-nums;letter-spacing:-.04em;font-size:1.9rem;font-weight:700}
.kpi-sub{line-height:1.45}
table{font-size:.87rem}
th{background:#f1f5fa;color:#455970;font-weight:650;letter-spacing:.035em}
th,td{padding:.65rem .7rem}
tbody tr:hover td{background:#f3f7fc}
td.num,td .ux-number{font-variant-numeric:tabular-nums}
.abas{gap:.25rem .35rem;padding:.3rem;border:1px solid var(--borda);background:#fff;border-radius:10px;align-items:center}
.abas a{padding:.55rem .8rem;border:0;margin:0;border-radius:7px}
.abas a:hover{background:#f2f6fb;color:var(--azul)}
.abas a.ativa{background:#e7effc;color:#17458f;font-weight:650;box-shadow:inset 0 -2px 0 #245ab4}
button,a.btn{border-radius:8px;font-weight:600;min-height:2.25rem}
input:not([type=checkbox]):not([type=radio]):not([type=hidden]),select,textarea{border-radius:7px}
input:not([type=checkbox]):not([type=radio]),select,textarea{max-width:100%}
input:focus-visible,select:focus-visible,textarea:focus-visible{outline:2px solid #245ab4;outline-offset:1px}
button:disabled{opacity:.6;cursor:not-allowed}
.vazio{background:#f7f9fc;border-color:#cbd6e4;border-radius:10px;padding:1.5rem 1rem}
.estado,.job{font-weight:650}
.ux-hero{display:flex;justify-content:space-between;align-items:center;gap:1.5rem;padding:1.6rem 1.75rem;margin:0 0 1.25rem;border-radius:16px;background:linear-gradient(115deg,#102b4a,#21527d 80%,#17677b);color:#f8fafc;box-shadow:0 5px 18px #153a6225}
.ux-hero h1,.ux-hero h2{color:#fff;margin:.3rem 0 .45rem;font-size:clamp(1.55rem,2.6vw,2rem);font-weight:700;letter-spacing:-.04em;line-height:1.25}
.ux-hero p{margin:0;line-height:1.55;color:#dbe9f7;font-size:.9rem}
.ux-hero .ux-eyebrow,.ux-eyebrow{display:block;text-transform:uppercase;font-size:.7rem;letter-spacing:.11em;font-weight:700;margin:0 0 .4rem;color:#245ab4}
.ux-hero .ux-eyebrow{color:#b9d6f5}
.ux-hero a{color:#fff}
.ux-hero .btn{background:#fff;color:#17458f;border-color:#fff;text-decoration:none}
.ux-hero .btn:hover{background:#e7effc}
.ux-hero-icon{width:4rem;height:4rem;display:grid;place-items:center;flex:none;border-radius:16px;background:#ffffff12;border:1px solid #ffffff40}
.ux-hero-icon svg{width:2.3rem;height:2.3rem}
.ux-hero-side{display:flex;flex-direction:column;align-items:flex-end;gap:.55rem;flex:none;text-align:right;font-size:.8rem;color:#dbe9f7}
.ux-status{display:inline-flex;align-items:center;gap:.4rem;border:1px solid #ffffff55;border-radius:999px;padding:.3rem .7rem;font-weight:650;background:#ffffff13;color:#fff}
.ux-status-on::before,.ux-status-off::before{content:"";width:.55rem;height:.55rem;border-radius:50%;background:#8de4ae}
.ux-status-off::before{background:#ffd28c}
.ux-meta{display:flex;flex-wrap:wrap;gap:.5rem 1.4rem;margin:0 0 1rem;color:var(--muted);font-size:.8rem}
.ux-meta b{color:var(--texto)}
.shield-admin .card{overflow-x:auto}
.shield-admin .kpis{grid-template-columns:repeat(auto-fit,minmax(min(11rem,100%),1fr))}
.shield-admin .shield-primary .kpi{min-height:8.3rem}
.shield-admin .shield-primary .kpi-val{font-size:2.35rem}
.shield-admin .shield-primary li:nth-child(2) .kpi{border-top-color:#157347}
.shield-admin .shield-primary li:nth-child(3) .kpi{border-top-color:#5373b6}
.shield-admin .shield-filters{display:flex;gap:.4rem;flex-wrap:wrap;align-items:center}
.shield-admin .shield-filters a{display:inline-flex;padding:.35rem .65rem;border:1px solid var(--borda);border-radius:999px;text-decoration:none;color:var(--muted);background:#fff}
.shield-admin .shield-filters a[aria-current=page]{color:#17458f;background:#e7effc;border-color:#8daee1;font-weight:650}
.shield-admin .shield-rank{display:inline-grid;place-items:center;min-width:2rem;height:2rem;padding:0 .35rem;border-radius:9px;background:#edf2f8;font-weight:700;font-variant-numeric:tabular-nums}
.shield-admin .shield-rank.first{background:#f8edcb;color:#77520a}.shield-admin .shield-rank.second{background:#e5eaf0;color:#40546b}.shield-admin .shield-rank.third{background:#f4e6d8;color:#854d20}
.shield-admin .shield-rank-name{font-weight:650}
.shield-admin meter{width:6rem;max-width:100%;height:.85rem;vertical-align:middle}
.shield-admin meter::-webkit-meter-bar{background:#e6edf5;border:0;border-radius:999px}
.shield-admin meter::-webkit-meter-optimum-value{background:#157347;border-radius:999px}
.shield-admin meter::-moz-meter-bar{background:#157347;border-radius:999px}
.shield-admin .shield-percent{white-space:nowrap;font-variant-numeric:tabular-nums;font-weight:650}
.shield-admin details.lugar,.ux-locations details.lugar{border:1px solid var(--borda);border-radius:12px;margin:.8rem 0;padding:0;overflow:hidden;background:#fff}
.shield-admin details.lugar summary,.ux-locations details.lugar summary{padding:1rem;background:#f7f9fc;color:var(--texto);font-weight:600;line-height:1.5}
.shield-admin details.lugar[open] summary,.ux-locations details.lugar[open] summary{border-bottom:1px solid var(--borda)}
.shield-admin details.lugar h3{margin:1rem 1rem .5rem}
.shield-admin details.lugar table{margin-bottom:.7rem}
.ux-locations .locations-guide{display:flex;gap:.9rem;align-items:flex-start;background:#eff5fd;border:1px solid #c8daf2;border-radius:10px;padding:1rem;margin:0 0 1rem;color:#254568}
.ux-locations .locations-guide b{display:block;margin:0 0 .2rem}
.ux-locations .locations-guide p{margin:0;font-size:.86rem}
.ux-locations .locations-guide svg{width:1.45rem;height:1.45rem;flex:none}
.ux-locations .location-summary{display:inline-flex;align-items:baseline;flex-wrap:wrap;gap:.25rem .75rem}
.ux-locations .location-summary small{color:var(--muted);font-weight:400}
.ux-locations .location-table{overflow-x:auto}
.ux-locations .location-table table{min-width:29rem}
.ux-locations .location-table td{vertical-align:middle;padding:.75rem 1rem}
.ux-locations .location-table th{padding:.65rem 1rem}
.ux-locations .location-table td:last-child{width:45%}
.ux-locations .location-table input:not([type=hidden]){width:100%;min-width:10rem}
.ux-locations .location-table small{display:block;margin-top:.25rem}
.ux-locations .location-table code{color:#24466a;font-weight:600}
.ux-locations .location-footer{display:flex;justify-content:space-between;align-items:center;gap:.6rem;flex-wrap:wrap;background:#f7f9fc;padding:.85rem 1rem;border-top:1px solid var(--borda)}
.ux-locations .location-footer small{color:var(--muted)}
.dashboard .dash-cab.ux-hero h1{font-size:clamp(1.55rem,2.6vw,2rem)}
.dashboard .dash-cab.ux-hero .muted,.dashboard .dash-cab.ux-hero .dash-eyebrow{color:#dbe9f7}
.dashboard .dash-kpis .kpi{border-top-color:#8ea9ce}
.dashboard .dash-kpis .kpi:has(.v-verde){border-top-color:#157347}
.dashboard .dash-kpis .kpi:has(.v-vermelho){border-top-color:#b4232b}
.dashboard .dash-kpis .kpi:has(.v-laranja){border-top-color:#9a5900}
.dashboard .dash-trilho{height:.65rem}
@media(max-width:700px){.ux-hero{padding:1.1rem;flex-wrap:wrap;gap:.8rem}.ux-hero-side{text-align:left;align-items:flex-start;flex:1 1 100%}.ux-hero-icon{width:3rem;height:3rem}.ux-hero-icon svg{width:1.8rem;height:1.8rem}.shield-admin .shield-primary .kpi-val{font-size:1.7rem}.shield-admin .shield-primary .kpi{min-height:0}.abas a{padding:.45rem .6rem}.kpi-val{font-size:1.5rem}}
@media(prefers-reduced-motion:reduce){.lado,.area{transition:none}}

.deployment-summary{display:flex;flex-wrap:wrap;gap:.6rem 1.5rem;color:var(--muted);font-size:.85rem;margin:.75rem 0 1rem}.deployment-summary b{color:var(--texto)}
.deployment-buildings{display:grid;grid-template-columns:repeat(auto-fit,minmax(min(19rem,100%),1fr));gap:1rem;margin-bottom:1.25rem}.deployment-building{margin:0;min-width:0}.deployment-building .kpi-val{color:var(--azul)}.deployment-building .card-cab h2{overflow-wrap:anywhere}.deployment-note{display:block;color:var(--muted);margin:.3rem 0}
.deployment-building details summary,.deployment-legacy summary{cursor:pointer;color:var(--azul);font-weight:650}.deployment-groups{list-style:none;margin:.6rem 0 0;padding:0}.deployment-groups li{display:flex;justify-content:space-between;gap:.6rem;padding:.45rem 0;border-top:1px solid var(--borda)}.deployment-legacy[open]>summary{margin-bottom:1rem}

/* Physical locations: compact, collapsed-by-default building cards. */
.ux-locations .locations-help{background:#eff5fd;border:1px solid #c8daf2;border-radius:10px;padding:.8rem 1rem;color:#254568;margin-bottom:1rem}
.ux-locations .locations-help summary{cursor:pointer;font-weight:650}.ux-locations .locations-help p{margin:.6rem 0 0;font-size:.85rem}
.ux-locations .locations-list-hint{color:var(--muted);font-size:.85rem;margin:1rem 0 .5rem}
.ux-locations .locations-list{display:grid;gap:.65rem}
.ux-locations details.location-building{margin:0;box-shadow:0 1px 4px #172b4309}
.ux-locations details.location-building>summary{display:flex;align-items:center;gap:1rem;cursor:pointer;list-style:none;background:#fff;padding:1rem 1.15rem}
.ux-locations details.location-building>summary::-webkit-details-marker{display:none}
.ux-locations details.location-building>summary:hover{background:#f4f8fd}
.ux-locations details.location-building[open]>summary{background:#f1f6fc}
.ux-locations details.location-building>summary:focus-visible{outline:2px solid var(--azul);outline-offset:-3px}
.location-building-icon{display:grid;place-items:center;width:2.5rem;height:2.5rem;border-radius:10px;background:#e7effc;color:#245ab4;flex:none}
.location-building-icon svg{width:1.45rem;height:1.45rem}
.location-building-title{display:flex;flex-direction:column;gap:.18rem;flex:1;min-width:0;overflow-wrap:anywhere}
.location-building-title b{font-size:.95rem}.location-building-title small{font-size:.78rem;color:var(--muted);font-weight:400}
.location-building-stats{display:flex;align-items:center;flex-wrap:wrap;gap:.4rem}
.location-stat{border:1px solid #dce4ed;border-radius:6px;background:#f7f9fc;color:#455970;font-size:.75rem;padding:.22rem .5rem;font-weight:500}
.location-stat-warn{background:#fff7e8;border-color:#efd6a8;color:#895100}
.location-stat-danger{background:#fff0f0;border-color:#efc5c5;color:#a32128}
.location-chevron{width:1.2rem;height:1.2rem;flex:none;color:var(--muted)}
.location-building[open] .location-chevron{transform:rotate(90deg)}
.location-content{padding:1.25rem;display:grid;gap:1rem}
.location-section-heading{display:flex;flex-direction:column;gap:.2rem}
.location-section-heading h3{margin:0;font-size:1rem;color:var(--texto)}
.location-section-heading small{color:var(--muted);font-size:.8rem}
.ux-locations .location-table{border:1px solid var(--borda);border-radius:9px}
.ux-locations .location-table table{margin:0}.ux-locations .location-table code{overflow-wrap:anywhere;white-space:normal}
.location-empty{display:flex;flex-direction:column;gap:.25rem;text-align:center;background:#f7f9fc;border:1px dashed #cbd6e4;border-radius:9px;padding:1.2rem;color:var(--muted);font-size:.85rem}
.location-empty b{color:var(--texto)}
.location-editor{border:1px solid var(--borda);border-radius:10px;background:#f8fafc;padding:1.2rem}
.location-editor>.location-section-heading{flex-direction:row;align-items:center;gap:.75rem;margin-bottom:1rem}
.location-add-icon{display:grid;place-items:center;width:2rem;height:2rem;border-radius:8px;background:#e7effc;color:#245ab4;font-size:1.35rem;font-weight:600;flex:none}
.location-fields{display:grid;grid-template-columns:minmax(0,1fr) minmax(0,2fr);gap:1rem}
.location-fields label{display:flex;flex-direction:column;gap:.4rem;min-width:0;font-size:.85rem;font-weight:600;color:var(--texto)}
.ux-locations .location-fields input{width:100%;box-sizing:border-box;min-height:2.65rem;background:#fff}
.location-regex{font-family:ui-monospace,SFMono-Regular,Consolas,monospace}
.location-example{display:flex;flex-wrap:wrap;gap:.35rem .7rem;align-items:center;margin-top:.8rem;font-size:.78rem;color:var(--muted)}
.location-example code{background:#e8eef6;padding:.15rem .4rem;border-radius:4px;color:#24466a;overflow-wrap:anywhere}
@media(max-width:650px){.location-fields{grid-template-columns:1fr}.location-building-stats{flex-basis:100%;padding-left:3.5rem}.ux-locations details.location-building>summary{flex-wrap:wrap;gap:.6rem}.location-chevron{order:0}.location-building-stats{order:1}.location-content{padding:.85rem}.location-editor{padding:.9rem}.location-footer button{width:100%}}
`
