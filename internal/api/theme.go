package api

// Aparência do painel (Aula 11.3): o mesmo padrão do painel de conformidade que a equipe já
// usa (barra verde, menu com ícone sobre o rótulo, cartões de indicadores, tabelas em
// cartões). Tudo inline: a CSP do painel não carrega nada de fora (sem CDN, sem fonte
// externa, sem JavaScript). Os ícones são emoji e um SVG inline, que não são recursos
// buscados e passam pela CSP.

// WithBrand define o nome no topo do painel e na tela de login (PATCHD_PANEL_TITLE).
func WithBrand(name string) Option {
	return func(a *api) {
		if name != "" && len(name) <= 40 {
			a.brand = name
		}
	}
}

// shieldSVG é o escudo da marca, desenhado em linha (currentColor: herda o branco da barra).
const shieldSVG = `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 3l8 3v6c0 4.5-3.4 8.3-8 9-4.6-.7-8-4.5-8-9V6l8-3z"/><path d="M8.5 12l2.5 2.5 4.5-5"/></svg>`

// themeCSS: as cores são as do painel de referência (verde da barra, cinzas, azul,
// vermelho e laranja dos indicadores).
const themeCSS = `
:root{--verde:#0b7a45;--fundo:#f9fafb;--borda:#e5e7eb;--texto:#111827;--muted:#6b7280;--azul:#066fd1;--azul-2:#0559a8;
--vermelho:#d63939;--laranja:#f59f00;--verde-ok:#2fb344;--raio:8px;--sombra:0 1px 2px rgba(17,24,39,.05)}
*{box-sizing:border-box}
body{margin:0;font:14px/1.5 "Segoe UI",Inter,system-ui,-apple-system,Roboto,sans-serif;color:var(--texto);background:var(--fundo)}
a{color:var(--azul)}code{font-size:.88em}
header.topo{display:flex;flex-wrap:wrap;align-items:center;gap:.5rem 1.5rem;padding:0 1.25rem;min-height:56px;background:var(--verde);color:#fff}
.marca{display:flex;align-items:center;gap:.55rem;color:#fff;text-decoration:none;font-weight:700;font-size:1.25rem;white-space:nowrap}
.marca svg{width:24px;height:24px}
header nav{display:flex;flex-wrap:wrap;align-items:stretch;margin-left:auto}
header nav a,header nav button{display:flex;flex-direction:column;align-items:center;justify-content:center;gap:1px;padding:.4rem .7rem .25rem;
color:#fff;text-decoration:none;font:inherit;font-size:.82rem;background:none;border:0;border-bottom:3px solid transparent;border-radius:0;cursor:pointer}
header nav .ic{font-size:1.1rem;line-height:1.2}
header nav a:hover,header nav button:hover{background:rgba(255,255,255,.1)}
header nav a.ativo{border-bottom-color:#fff;font-weight:600}
header nav form{display:flex;margin:0}
.quem{color:#d5efe0;font-size:.78rem;white-space:nowrap}
main{max-width:90rem;margin:0 auto;padding:1.5rem 1.25rem 3rem}
h1{font-size:1.3rem;font-weight:600;margin:.25rem 0 1.1rem}h2{font-size:1rem;font-weight:600;margin:0 0 .75rem}
.card,section{background:#fff;border:1px solid var(--borda);border-radius:var(--raio);box-shadow:var(--sombra);padding:1rem 1.25rem;margin:0 0 1.25rem}
.card-cab{display:flex;flex-wrap:wrap;align-items:center;justify-content:space-between;gap:.5rem 1rem;margin:-1rem -1.25rem 1rem;padding:.8rem 1.25rem;border-bottom:1px solid var(--borda)}
.card-cab h2{margin:0}.card-cab .muted{font-size:.8rem}
.kpis{display:grid;grid-template-columns:repeat(auto-fill,minmax(14.5rem,1fr));gap:1rem;margin:0 0 1.5rem;padding:0;list-style:none}
.kpi{display:block;height:100%;background:#fff;border:1px solid var(--borda);border-radius:var(--raio);box-shadow:var(--sombra);padding:1.1rem 1.25rem 1rem;color:var(--texto);text-decoration:none}
a.kpi:hover{border-color:#c4cad3}.kpi.ativo{border-color:var(--azul);box-shadow:0 0 0 3px rgba(6,111,209,.12)}
.kpi-rot{display:block;font-size:.74rem;font-weight:500;letter-spacing:.035em;text-transform:uppercase;color:var(--muted)}
.kpi-val{display:block;font-size:1.65rem;font-weight:600;line-height:1.25;margin-top:.2rem}
.kpi-sub{display:block;font-size:.8rem;color:var(--muted)}
.v-vermelho{color:var(--vermelho)}.v-laranja{color:var(--laranja)}.v-verde{color:var(--verde-ok)}.v-cinza{color:var(--muted)}
table{border-collapse:collapse;width:100%;font-size:.9rem}
th,td{text-align:left;padding:.55rem .6rem;border-bottom:1px solid var(--borda);vertical-align:top}
th{font-weight:500;color:var(--muted);font-size:.74rem;text-transform:uppercase;letter-spacing:.035em;background:#f9fafb}
tbody tr:hover td{background:#fbfcfd}td.num{text-align:right;font-variant-numeric:tabular-nums}
.tabela{overflow-x:auto;margin:0 -1.25rem}.card-cab+.tabela,.card-cab+form>.tabela:first-child{margin-top:-1rem}
.card>.tabela:last-child,section>.tabela:last-child{margin-bottom:-1rem}.tabela tbody tr:last-child td{border-bottom:0}.tabela th:first-child,.tabela td:first-child{padding-left:1.25rem}.tabela th:last-child,.tabela td:last-child{padding-right:1.25rem}
.estado,.job{display:inline-block;padding:.12rem .55rem;border-radius:999px;font-size:.76rem;font-weight:600;white-space:nowrap;border:1px solid transparent}
.faltando{color:#a12424;background:#fbe9e9;border-color:#f3c4c4}.reboot_pendente{color:#8a5a00;background:#fff4dc;border-color:#f6d68f}
.sem_dado_recente{color:#4b5563;background:#f1f3f5;border-color:#d7dbe0}.desconhecido{color:#3b4a6b;background:#edf2fb;border-color:#cdd8ee}
.em_dia{color:#16692f;background:#e7f6ec;border-color:#b4e1c1}
.job.concluido,.job.concluida{color:#16692f;background:#e7f6ec}.job.falhou,.job.rejeitado{color:#a12424;background:#fbe9e9}
.job.pendente,.job.entregue,.job.em_andamento{color:#0b4f96;background:#e6f0fb}.job.pausada{color:#8a5a00;background:#fff4dc}
.job.cancelado,.job.cancelada,.job.expirado{color:#4b5563;background:#f1f3f5}
.motivos{margin:.15rem 0 0;padding-left:1.1rem;color:var(--muted);font-size:.84rem}
.aviso{padding:.7rem .9rem;border-radius:var(--raio);margin:0 0 1rem;border:1px solid transparent}
.ok{background:#e7f6ec;border-color:#b4e1c1;color:#14532d}.erro{background:#fbe9e9;border-color:#f3c4c4;color:#8a1c1c}.atencao{background:#fff4dc;border-color:#f6d68f;color:#7a4a00}
dl.dados{display:grid;grid-template-columns:max-content 1fr;gap:.45rem 1.4rem;margin:0}dl.dados dt{color:var(--muted)}dl.dados dd{margin:0}
.expl{color:var(--vermelho);font-weight:600}.muted{color:var(--muted)}
.nota{font-size:.78rem;max-width:34rem;margin-top:.15rem}
.abas{display:flex;flex-wrap:wrap;gap:.25rem;border-bottom:1px solid var(--borda);margin:0 0 1.2rem}
.abas a{padding:.55rem .95rem;color:var(--muted);text-decoration:none;border-bottom:2px solid transparent;margin-bottom:-1px;font-weight:500}
.abas a:hover{color:var(--texto)}.abas a.ativa{color:var(--verde);border-bottom-color:var(--verde)}
.abas .cont{display:inline-block;min-width:1.3rem;padding:0 .35rem;border-radius:999px;background:#eef1f4;color:var(--muted);font-size:.72rem;text-align:center}
.grade2{display:grid;grid-template-columns:repeat(auto-fit,minmax(22rem,1fr));gap:0 1.2rem}
.roscas{list-style:none;padding:0;margin:0;display:flex;flex-wrap:wrap;gap:1rem 2.2rem}.roscas li{display:flex;align-items:center;gap:.8rem}
.rosca .fundo{stroke:var(--borda)}.rosca .ok{stroke:var(--verde-ok)}.rosca .alto{stroke:var(--laranja)}.rosca .cheio{stroke:var(--vermelho)}
.rosca text{font-size:18px;font-weight:600;fill:var(--texto)}.fontes{font-size:.8rem;margin:.9rem 0 0}
th a{color:inherit;text-decoration:none}th a:hover{color:var(--azul)}h1 .muted{font-weight:400}
form.busca{display:flex;flex-wrap:wrap;gap:.5rem;align-items:center;margin-bottom:1rem}form.busca input[type=search]{flex:1 1 16rem}
.paginas{display:flex;flex-wrap:wrap;gap:.35rem .7rem;align-items:center;margin-top:1rem;font-size:.88rem}.paginas .muted{margin-right:auto}
.paginas a,.paginas span.atual{padding:.15rem .5rem;border-radius:6px}.paginas span.atual{background:var(--azul);color:#fff}
.uso{display:inline-block;vertical-align:middle;width:8rem;height:.5rem;border-radius:4px;background:var(--borda);overflow:hidden}
.uso>span{display:block;height:100%;background:var(--verde-ok)}.uso.alto>span{background:var(--laranja)}.uso.cheio>span{background:var(--vermelho)}
.card h3{font-size:.9rem;margin:1.1rem 0 .5rem;color:var(--muted);font-weight:600}
button{font:inherit;font-size:.88rem;padding:.4rem .95rem;border-radius:6px;border:1px solid var(--azul);background:var(--azul);color:#fff;cursor:pointer;white-space:nowrap}
button:hover{background:var(--azul-2);border-color:var(--azul-2)}
button.sec{background:#fff;color:var(--azul)}button.sec:hover{background:#eef5fd}
button.perigo{background:#fff;color:var(--vermelho);border-color:#ecb3b3}button.perigo:hover{background:#fbe9e9}
a.btn{display:inline-block;padding:.32rem .75rem;border:1px solid var(--azul);border-radius:6px;color:var(--azul);text-decoration:none;font-size:.84rem;background:#fff}
a.btn:hover{background:#eef5fd}.grupo-btn{display:inline-flex}.grupo-btn a.btn{border-radius:0}.grupo-btn a.btn:first-child{border-radius:6px 0 0 6px}.grupo-btn a.btn:last-child{border-radius:0 6px 6px 0;margin-left:-1px}
input,select{font:inherit;font-size:.9rem;padding:.38rem .6rem;border:1px solid #d5dae1;border-radius:6px;background:#fff;color:var(--texto)}
input:focus,select:focus{outline:2px solid rgba(6,111,209,.25);border-color:var(--azul)}
input[type=checkbox],input[type=radio]{padding:0}
form.acao{margin-top:.6rem;display:flex;flex-wrap:wrap;gap:.5rem;align-items:center}form.acao input{flex:1 1 20rem}
td form{margin:0}form.inline{display:inline;margin-left:.6rem}.dias label{margin-right:.7rem}
.lote{margin-top:.8rem;display:flex;flex-wrap:wrap;gap:.6rem .9rem;align-items:center}
th.col-lote,td.col-lote{width:1%;text-align:center}div.acao{display:flex}div.acao input{flex:1 1 20rem}
form.grade{display:grid;grid-template-columns:max-content minmax(0,1fr);gap:.65rem 1rem;align-items:center;max-width:46rem}
form.grade input[type=text]{width:100%}
`
