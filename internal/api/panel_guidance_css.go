package api

// Exclusivo do servidor: o tema e o executável da Central não são alterados.
const panelGuidanceCSS = `
.lado nav .nav-context{display:grid;gap:.1rem;margin:.1rem .65rem .5rem 1.3rem;padding-left:.65rem;border-left:1px solid #ffffff35}
.lado nav .nav-context a{margin:0;padding:.4rem .55rem;min-height:2rem;font-size:.8rem;white-space:normal;line-height:1.4}
.lado nav .nav-context a.sub-ativo{background:#ffffff10;color:#fff;font-weight:650}
.lado .nav-tools{margin:.45rem .6rem;border:0;padding:0;background:transparent;color:var(--lado-texto)}
.nav-tools>summary{display:flex;align-items:center;gap:.8rem;cursor:pointer;border-radius:8px;padding:.6rem .65rem;font-size:.8rem;list-style:none;line-height:1.4;min-height:2.55rem}
.nav-tools>summary::-webkit-details-marker{display:none}.nav-tools>summary:hover{background:var(--marinho-2);color:#fff}.nav-tools>summary:focus-visible{outline:2px solid #a9c9f2;outline-offset:-2px}
.nav-chevron{margin-left:auto;font-size:1.2rem}.nav-tools[open] .nav-chevron{transform:rotate(90deg)}.lado nav .nav-tools .nav-context{margin-left:.7rem;margin-right:0}
.header-help{font-size:.8rem;color:var(--azul);text-decoration:none;border:1px solid var(--borda);border-radius:7px;padding:.3rem .6rem;white-space:nowrap}.header-help:hover{background:#eef4fc}
.page-guide{margin:0 0 .7rem;font-size:.84rem;box-shadow:none;border:0;background:none;padding:0}
.page-guide>summary{text-align:right;cursor:pointer;color:var(--azul);padding:.15rem 0;list-style-position:inside;font-weight:600}
.page-guide-body{margin-top:.6rem;padding:1rem 1.2rem;border:1px solid #cbdcf2;background:#f0f6fd;border-radius:10px;color:#294763}
.page-guide-body h2{font-size:1rem;margin:0 0 .35rem}.page-guide-body p{margin:.25rem 0}.page-guide-body ol{padding-left:1.3rem;margin:.65rem 0}.page-guide-body li{margin:.35rem 0}.page-guide-body a{font-weight:600}
.settings-navigation{margin-bottom:1.2rem}.settings-navigation>.abas{margin-bottom:.5rem}
.settings-more{padding:.35rem .6rem;border:1px solid var(--borda);border-radius:9px;background:#fff;font-size:.8rem}
.settings-more>summary{cursor:pointer;font-weight:600;color:#435970;padding:.25rem}.settings-more-links{display:flex;flex-wrap:wrap;gap:.35rem;padding:.6rem 0 .2rem}
.settings-more-links a{padding:.35rem .65rem;border-radius:6px;text-decoration:none;color:var(--muted)}.settings-more-links a:hover{background:#f0f5fb;color:var(--azul)}.settings-more-links a.ativa{background:#e7effc;color:#17458f;font-weight:650}
.logo-acoes{min-width:0}.logo-acoes input[type=file]{width:100%;min-width:0}
.settings-start{margin:1.25rem 0 1.5rem}.settings-start>h2{font-size:1.15rem;margin:0 0 .3rem}.settings-start>p{margin:.3rem 0 1rem;font-size:.86rem}
.settings-task-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(min(17rem,100%),1fr));gap:.75rem}
.settings-task-grid>a{display:flex;flex-direction:column;gap:.35rem;min-width:0;padding:1rem 1.15rem;background:#fff;border:1px solid var(--borda);border-radius:11px;text-decoration:none;color:var(--texto);box-shadow:0 2px 6px #172b4306}
.settings-task-grid>a:hover{border-color:#9ab7e2;background:#f6f9fe}.settings-task-grid strong{font-size:.9rem;color:#24558e}.settings-task-grid span{font-size:.8rem;color:var(--muted);line-height:1.5}
.settings-diagnostics,.settings-explanation{border:1px solid var(--borda);border-radius:10px;padding:.8rem 1rem;background:#fff;margin-bottom:1.25rem;font-size:.85rem}
.settings-diagnostics>summary,.settings-explanation>summary{cursor:pointer;color:#435970;font-weight:600}.settings-diagnostics[open]>summary{margin-bottom:1rem}.settings-diagnostics>.card{margin:0;box-shadow:none}.settings-explanation p{margin:.75rem 0 .2rem}
.param-source{font-size:.76rem;margin:.25rem 0}.param-source>summary{color:#4e6380;cursor:pointer}.param-source[open]{padding:.4rem .6rem;border-left:2px solid #c3d5ef;background:#f5f8fc}.param-source[open]>span{display:block;margin:.3rem 0}
.help-heading{margin:.25rem 0 1.25rem;max-width:46rem}.help-heading h1{font-size:1.9rem;letter-spacing:-.04em;margin:.25rem 0 .5rem}.help-heading p{color:var(--muted);font-size:.9rem;margin:0}
.help-search{padding:1rem 1.2rem;border:1px solid #cbdcf2;border-radius:12px;background:#edf4fd;margin-bottom:1.15rem}.help-search>label{display:block;font-size:.85rem;font-weight:650;color:#254568;margin-bottom:.5rem}.help-search>div{display:flex;align-items:center;flex-wrap:wrap;gap:.6rem}.help-search input{flex:1 1 20rem;min-width:0;max-width:100%;background:#fff}.help-search a{font-size:.8rem}
.help-index{display:flex;flex-wrap:wrap;gap:.4rem;margin:0 0 1.25rem}.help-index a{color:#315b91;border:1px solid var(--borda);background:#fff;border-radius:7px;font-size:.76rem;padding:.3rem .55rem;text-decoration:none}.help-index a:hover{background:#e9f1fc;border-color:#a9c3e6}
.help-tasks{display:grid;grid-template-columns:repeat(auto-fit,minmax(min(27rem,100%),1fr));gap:1rem}.help-task{margin:0;min-width:0;border:1px solid var(--borda);border-radius:11px;background:#fff;align-self:start;overflow:hidden;box-shadow:0 2px 6px #172b4307}.help-task>summary{cursor:pointer;padding:1rem 1.2rem;list-style-position:inside;color:#254568}.help-task>summary:hover{background:#f4f8fd}.help-task-title{font-weight:650;font-size:.95rem;line-height:1.5}.help-task-summary{display:block;font-size:.8rem;color:var(--muted);margin:.35rem 0 0 1rem;line-height:1.5}.help-task-body{padding:0 1.2rem 1.1rem;scroll-margin-top:5.5rem}.help-task:has(.help-task-body:target){border-color:#759ed7;box-shadow:0 0 0 3px #245ab418}.help-task:target{border-color:#759ed7;box-shadow:0 0 0 3px #245ab418}.help-task h2{font-size:1.05rem;line-height:1.4;margin:0 0 .35rem}.help-task>p{font-size:.84rem;color:var(--muted);margin:.2rem 0}.help-task ol{font-size:.84rem;padding-left:1.25rem;margin:.6rem 0 1rem;line-height:1.65}.help-task li{margin:.45rem 0}.help-actions{display:flex;flex-wrap:wrap;gap:.45rem;margin-top:auto}.help-actions .btn{font-size:.77rem;white-space:normal}
.help-support{padding:1rem 1.2rem;margin-top:1.2rem;border:1px dashed #b9cada;border-radius:11px;color:var(--muted);font-size:.82rem}.help-support h2{font-size:1rem;color:var(--texto);margin:0 0 .4rem}.help-support p{margin:.35rem 0}
@media(min-width:961px){.menu-chk:checked~.lado .nav-context,.menu-chk:checked~.lado .nav-chevron{display:none}.menu-chk:checked~.lado .nav-tools>summary{justify-content:center;padding:.6rem 0}}
@media(max-width:600px){.header-help{font-size:.75rem;padding:.25rem .45rem}.barra-topo .selos{display:none}.settings-task-grid{grid-template-columns:1fr}.settings-more-links{flex-direction:column}.help-heading h1{font-size:1.5rem}.help-search{padding:.9rem}.help-search>div{align-items:stretch}.help-search input{flex-basis:100%;width:100%}.help-tasks{grid-template-columns:1fr}.help-task>summary{padding:.9rem}.help-task-body{padding:0 .9rem 1rem}.help-actions{flex-direction:column}.help-actions a{text-align:center}.page-guide-body{padding:.8rem}.settings-diagnostics{padding:.8rem}.settings-diagnostics>.card{padding:.8rem}}
`
