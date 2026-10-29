package api

import "github.com/tasantiago/patchd/internal/ui"

// Aparência do painel (Aula 11.3, refeita na 19.1): o padrão das aplicações do tribunal. Barra
// lateral azul-marinho com o logotipo, o usuário e os grupos do menu; barra superior branca;
// conteúdo em cinza-claro com os cartões brancos. Tudo inline: a CSP do painel não carrega
// nada de fora (sem CDN, sem fonte externa, sem JavaScript). Os ícones são SVG de linha
// desenhados no próprio HTML (layout.go).

// WithBrand define o nome no topo do painel e na tela de login (PATCHD_PANEL_TITLE).
func WithBrand(name string) Option {
	return func(a *api) {
		if n := len([]rune(name)); n > 0 && n <= 40 { // letras, não bytes: "Justiça" tem 7 (Aula 18.2)
			a.brand = name
		}
	}
}

// shieldSVG é o escudo da marca, desenhado em linha (Aula 19.1b: em internal/ui, o mesmo da Central).
const shieldSVG = ui.ShieldSVG

// themeCSS: as cores das aplicações do tribunal (marinho da barra lateral, lilás dos títulos
// dos grupos, rosa dos contadores) e as do painel de conformidade nos indicadores.
const themeCSS = `
.mescla-escolha select{flex:1 1 20rem;min-width:0;max-width:100%}
.descoberta-acoes{display:flex;flex-wrap:wrap;align-items:center;gap:.75rem;margin:.8rem 0 1.2rem}
.descoberta-estado{display:inline-block;padding:.15rem .5rem;border:1px solid transparent;border-radius:999px;font-size:.8rem}
.descoberta-credencial{max-width:52rem;margin-top:1.25rem}
.descoberta-campos{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:1.25rem}
.descoberta-campo{display:flex;flex-direction:column;gap:.4rem}
.descoberta-campo label{font-weight:600}.descoberta-campo input{width:100%;min-width:0}
.descoberta-opcao{display:flex;align-items:flex-start;gap:.65rem;padding:1rem;margin:1.25rem 0;border:1px solid var(--borda);border-radius:6px}
.descoberta-opcao input{margin-top:.3rem}.descoberta-opcao small{display:block;margin-top:.3rem}
.descoberta-credencial input:disabled,.descoberta-credencial button:disabled{opacity:.55;cursor:not-allowed}
.descoberta-ajuda{margin-top:1rem}.descoberta-ajuda summary{cursor:pointer;font-weight:600}
.descoberta-status progress{display:block;width:100%;height:1rem;margin:.6rem 0;accent-color:var(--azul)}
.descoberta-atividade{list-style:none;padding:0;margin:.75rem 0;max-height:22rem;overflow-y:auto}
.descoberta-atividade li{display:grid;grid-template-columns:10rem minmax(0,1fr);gap:.75rem;padding:.6rem 0;border-bottom:1px solid var(--borda)}
.descoberta-atividade time{color:var(--muted);font-size:.8rem}
@media(max-width:700px){.descoberta-campos{grid-template-columns:1fr}.descoberta-atividade li{grid-template-columns:1fr;gap:.2rem}}
` + ui.LayoutCSS + `
main{max-width:90rem;margin:0 auto;padding:1.5rem 1.5rem 3rem}
@media (max-width:600px){main{padding:1rem .9rem 2rem}.barra-topo{padding:0 .8rem}}
h1{font-size:1.3rem;font-weight:600;margin:.25rem 0 1.1rem}h2{font-size:1rem;font-weight:600;margin:0 0 .75rem}
ul.usuarios{margin:0;padding:0;list-style:none}
.logo-linha{display:flex;flex-wrap:wrap;gap:1.25rem;align-items:flex-start}.logo-acoes{flex:1 1 20rem}.logo-acoes p{margin:0 0 .4rem}
.logo-previa{display:grid;place-items:center;flex:none;width:6.5rem;height:6.5rem;border-radius:var(--raio);background:var(--marinho);color:#fff}
.logo-previa img{max-width:5rem;max-height:5rem;object-fit:contain}.logo-previa svg{width:2.6rem;height:2.6rem}
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
.job.pendente,.job.entregue,.job.em_andamento{color:#0b4f96;background:#e6f0fb}.job.pausada,.job.adiado{color:#8a5a00;background:#fff4dc}.job.dispensado{color:#4b5563;background:#f1f3f5}
.job.cancelado,.job.cancelada,.job.expirado{color:#4b5563;background:#f1f3f5}
td.nowrap{white-space:nowrap}.barra{display:inline-block;vertical-align:middle;width:4.5rem;height:.45rem;background:#eef0f3;border-radius:999px;overflow:hidden}.barra>span{display:block;height:100%;background:var(--verde-ok)}
.atencao-lista{list-style:none;margin:0;padding:0}.atencao-lista li{padding:.45rem .7rem;border-left:3px solid var(--laranja);background:#fffaf0;margin:0 0 .4rem;border-radius:4px}
.atencao-lista li.erro{border-left-color:var(--vermelho);background:#fdf2f2}.atencao-lista a{color:var(--texto);text-decoration:none}.atencao-lista a:hover{text-decoration:underline}
pre.comando{margin:0;padding:.75rem 1rem;background:#0f172a;color:#e2e8f0;border-radius:6px;overflow-x:auto;font-size:.82rem;white-space:pre}
dd.nivel-atencao,td.nivel-atencao,span.nivel-atencao{color:#8a5a00}dd.nivel-erro,td.nivel-erro,span.nivel-erro{color:var(--vermelho)}dd.nivel-ok{color:var(--verde-ok)}.senha-bios{font-size:1.5rem;letter-spacing:.12em;padding:.2rem .6rem}
.duas-col{display:grid;grid-template-columns:repeat(auto-fit,minmax(28rem,1fr));gap:0 1.25rem}tr.total td{font-weight:600}
.motivos{margin:.15rem 0 0;padding-left:1.1rem;color:var(--muted);font-size:.84rem}
.aviso{padding:.7rem .9rem;border-radius:var(--raio);margin:0 0 1rem;border:1px solid transparent}
.ok{background:#e7f6ec;border-color:#b4e1c1;color:#14532d}.erro{background:#fbe9e9;border-color:#f3c4c4;color:#8a1c1c}.atencao{background:#fff4dc;border-color:#f6d68f;color:#7a4a00}
dl.dados{display:grid;grid-template-columns:max-content 1fr;gap:.45rem 1.4rem;margin:0}dl.dados dt{color:var(--muted)}dl.dados dd{margin:0}
.expl{color:var(--vermelho);font-weight:600}.muted{color:var(--muted)}
.nota{font-size:.78rem;max-width:34rem;margin-top:.15rem}
.abas{display:flex;flex-wrap:wrap;gap:.25rem;border-bottom:1px solid var(--borda);margin:0 0 1.2rem}
.abas a{padding:.55rem .95rem;color:var(--muted);text-decoration:none;border-bottom:2px solid transparent;margin-bottom:-1px;font-weight:500}
.abas a:hover{color:var(--texto)}.abas a.ativa{color:var(--destaque);border-bottom-color:var(--destaque)}
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
td form{margin:0}form.inline{display:inline;margin-left:.6rem}details.excluir{margin-top:.4rem}details.excluir summary{cursor:pointer;color:var(--vermelho);font-size:.84rem}details.excluir form{margin-top:.4rem}pre.andamento{margin-top:.6rem;max-height:18rem;overflow-y:auto}.dias label{margin-right:.7rem}
details.host summary{cursor:pointer;color:var(--azul);font-size:.84rem}details.host .metodo{margin-top:.7rem}p.rot-metodo{margin:.2rem 0 .25rem;font-weight:600;font-size:.84rem}form.linha-faixa{display:flex;gap:.5rem;flex-wrap:wrap;margin:.6rem 0}form.linha-faixa input[type=text]{flex:1;min-width:11rem}.mt{margin-top:.8rem}
.lote{margin-top:.8rem;display:flex;flex-wrap:wrap;gap:.6rem .9rem;align-items:center}
th.col-lote,td.col-lote{width:1%;text-align:center}div.acao{display:flex}div.acao input{flex:1 1 20rem}
form.grade{display:grid;grid-template-columns:max-content minmax(0,1fr);gap:.65rem 1rem;align-items:center;max-width:46rem}
form.grade input[type=text]{width:100%}
/* Formulário da campanha (Aula 16.6): campos em coluna; o que não vale para o tipo
escolhido some, por :has(), sem JavaScript. Navegador sem :has() mostra tudo, como antes. */
form.campanha{display:flex;flex-direction:column;gap:1.1rem;max-width:46rem}
form.campanha .campo{display:flex;flex-direction:column;gap:.3rem;margin:0;padding:0;border:0}
form.campanha .campo>label:not(.opcao):not(.anel),form.campanha legend{font-weight:600;font-size:.88rem;padding:0;margin-bottom:.2rem}
form.campanha legend small{font-weight:400}
form.campanha small{font-size:.8rem}
form.campanha .opcao{display:flex;align-items:flex-start;gap:.6rem;padding:.55rem .75rem;border:1px solid var(--borda);border-radius:6px;cursor:pointer;background:#fff;margin-bottom:.35rem}
form.campanha .opcao span{display:flex;flex-direction:column;gap:.1rem}
form.campanha .opcao small{color:var(--muted)}
form.campanha .opcao input{margin-top:.25rem}
form.campanha .opcao:has(input:checked){border-color:var(--azul);background:#eef5fd}
form.campanha .quando .opcao span{flex-direction:row;flex-wrap:wrap;align-items:center;gap:.5rem}
form.campanha .quando .opcao:last-child span{flex-direction:column;align-items:flex-start;gap:.1rem}
form.campanha .chips{display:flex;flex-wrap:wrap;gap:.4rem}
form.campanha .anel{display:inline-flex;align-items:center;gap:.4rem;padding:.35rem .7rem;border:1px solid var(--borda);border-radius:999px;cursor:pointer;background:#fff}
form.campanha .anel:has(input:checked){border-color:var(--azul);background:#eef5fd}
form.campanha details.vazios{margin-top:.5rem}form.campanha details.vazios summary{cursor:pointer;color:var(--muted);font-size:.84rem;margin-bottom:.4rem}
form.campanha:has(input[name=tipo]:checked:not([value=thirdparty])) .so-terceiros,
form.campanha:has(input[name=tipo]:checked:not([value=update]):not([value=windows_update])) .so-fontes,
form.campanha:has(input[name=tipo]:checked:not([value=reboot])) .so-reinicio{display:none}
form.campanha:has(input[value=janela]:checked) select[name=prazo]{opacity:.45}
form.campanha .acoes{display:flex;align-items:center;gap:.75rem}
`
