// Package ui guarda o que o painel (internal/api) e a Central de software (internal/userapp)
// mostram igual (Aula 19.1b): o layout das aplicações do tribunal, os ícones de linha e a
// conferência do logotipo. Nada aqui carrega recurso de fora: as duas páginas têm CSP sem
// JavaScript e sem CDN.
package ui

import (
	"bytes"
	"errors"
	"fmt"
	"image/jpeg"
	"image/png"
)

// LayoutCSS é a barra lateral, a barra superior e a gaveta, com as cores. Cada página
// acrescenta o que é dela (cartões, tabelas, listas).
const LayoutCSS = `:root{--marinho:#1c2541;--marinho-2:#26305a;--marinho-3:#323d6b;--lilas:#a8a5f0;--rosa:#e5487c;--lado-texto:#c3c9de;
--destaque:#4352c9;--fundo:#f1f3f7;--borda:#e3e6ec;--texto:#111827;--muted:#6b7280;--azul:#066fd1;--azul-2:#0559a8;
--vermelho:#d63939;--laranja:#f59f00;--verde-ok:#2fb344;--raio:8px;--sombra:0 1px 2px rgba(17,24,39,.05);
--lado-w:16rem;--lado-mini:4.5rem;--barra-h:3.6rem}
*{box-sizing:border-box}
body{margin:0;font:14px/1.5 "Segoe UI",Inter,system-ui,-apple-system,Roboto,sans-serif;color:var(--texto);background:var(--fundo)}
a{color:var(--azul)}code{font-size:.88em}
.sr{position:absolute;width:1px;height:1px;overflow:hidden;clip:rect(0 0 0 0);white-space:nowrap}
/* Layout (Aulas 19.1 e 19.1b). O checkbox #menu guarda o estado: na tela larga recolhe a barra lateral
para só os ícones; na estreita, abre a gaveta. */
.menu-chk{position:fixed;top:0;left:0;width:1px;height:1px;opacity:0;margin:0}
.lado{position:fixed;inset:0 auto 0 0;z-index:30;width:var(--lado-w);display:flex;flex-direction:column;background:var(--marinho);
color:var(--lado-texto);overflow-x:hidden;overflow-y:auto;transition:width .15s ease,transform .2s ease}
.lado .marca{display:flex;align-items:center;gap:.7rem;min-height:var(--barra-h);padding:.6rem 1.1rem;color:#fff;text-decoration:none;
font-weight:700;font-size:1.12rem;letter-spacing:.01em;border-bottom:1px solid rgba(255,255,255,.07);white-space:nowrap}
.lado .marca small{display:block;font-size:.72rem;font-weight:500;color:var(--lado-texto);letter-spacing:0}
.lado .marca img{width:36px;height:36px;object-fit:contain;flex:none;border-radius:4px}
.lado .escudo{display:grid;place-items:center;flex:none;width:36px;height:36px;border-radius:8px;background:var(--destaque)}
.lado .escudo svg{width:22px;height:22px}
.lado .rot{overflow:hidden;text-overflow:ellipsis}
.lado .usuario{display:flex;align-items:center;gap:.7rem;padding:1rem 1.1rem;border-bottom:1px solid rgba(255,255,255,.07);white-space:nowrap}
.lado .usuario .rot{display:flex;flex-direction:column;line-height:1.25;min-width:0}
.lado .usuario strong{color:#fff;font-weight:600;font-size:.9rem;overflow:hidden;text-overflow:ellipsis}
.lado .usuario small{font-size:.74rem;color:var(--lado-texto)}
.avatar{display:grid;place-items:center;flex:none;width:2.2rem;height:2.2rem;border-radius:50%;background:var(--marinho-3);color:#fff;
font-size:.8rem;font-weight:600;letter-spacing:.03em}
.lado nav{display:flex;flex-direction:column;padding:.4rem 0 1.5rem}
.lado .grupo{margin:1.1rem 0 .3rem;padding:0 1.2rem;font-size:.68rem;font-weight:700;letter-spacing:.09em;text-transform:uppercase;color:var(--lilas);white-space:nowrap}
.lado nav a{position:relative;display:flex;align-items:center;gap:.8rem;margin:1px .6rem;padding:.55rem .65rem;border-radius:6px;color:var(--lado-texto);
text-decoration:none;white-space:nowrap;font-size:.9rem}
.lado nav a:hover{background:var(--marinho-2);color:#fff}
.lado nav a.ativo{background:var(--marinho-3);color:#fff;font-weight:600;box-shadow:inset 3px 0 0 var(--lilas)}
.lado .ic{width:20px;height:20px;flex:none}
.lado .cont{margin-left:auto;min-width:1.35rem;padding:.05rem .4rem;border-radius:999px;background:var(--rosa);color:#fff;font-size:.7rem;font-weight:700;
text-align:center;line-height:1.35}
.area{margin-left:var(--lado-w);min-height:100vh;transition:margin-left .15s ease}
.barra-topo{position:sticky;top:0;z-index:20;display:flex;align-items:center;gap:.9rem;min-height:var(--barra-h);padding:0 1.25rem;background:#fff;
border-bottom:1px solid var(--borda);box-shadow:var(--sombra)}
.hamb{display:grid;place-items:center;width:2.3rem;height:2.3rem;border-radius:6px;color:var(--texto);cursor:pointer;flex:none}
.hamb:hover{background:#f1f3f7}.hamb svg{width:22px;height:22px}
.menu-chk:focus-visible~.area .hamb{outline:2px solid var(--destaque);outline-offset:2px}
.trilha{display:flex;align-items:center;gap:.45rem;min-width:0;font-size:.95rem;white-space:nowrap}
.trilha .secao{color:var(--muted)}.trilha .sep{color:#b5bac4}.trilha .titulo{font-weight:600;overflow:hidden;text-overflow:ellipsis}
.selos{display:flex;gap:.4rem;margin-left:auto}
.selo{padding:.18rem .6rem;border-radius:999px;border:1px solid var(--borda);background:#f7f8fb;color:var(--muted);font-size:.74rem;white-space:nowrap}
.selo b{color:var(--texto);font-weight:600}
.conta{position:relative}
.conta summary{display:flex;align-items:center;gap:.5rem;padding:.25rem .4rem;border-radius:6px;cursor:pointer;list-style:none;white-space:nowrap}
.conta summary::-webkit-details-marker{display:none}.conta summary:hover{background:#f1f3f7}
.conta summary .avatar{width:2rem;height:2rem;background:var(--destaque)}.conta summary .ic{width:16px;height:16px;color:var(--muted)}
.conta .nome{max-width:12rem;overflow:hidden;text-overflow:ellipsis;font-weight:500}
.conta-menu{position:absolute;right:0;top:calc(100% + .4rem);min-width:15rem;background:#fff;border:1px solid var(--borda);border-radius:var(--raio);
box-shadow:0 8px 24px rgba(17,24,39,.12);padding:.4rem;z-index:40}
.conta-menu p{display:flex;flex-direction:column;margin:0;padding:.5rem .6rem .6rem;border-bottom:1px solid var(--borda);line-height:1.35}
.conta-menu p span{color:var(--muted);font-size:.8rem}
.conta-menu form{margin:.35rem 0 0}
.conta-menu button.sair{display:flex;align-items:center;gap:.55rem;width:100%;padding:.5rem .6rem;border:0;border-radius:6px;background:none;color:var(--vermelho);text-align:left}
.conta-menu button.sair:hover{background:#fbe9e9}.conta-menu .ic{width:18px;height:18px}
.veu{display:none}
@media (min-width:961px){
.menu-chk:checked~.lado{width:var(--lado-mini)}
.menu-chk:checked~.area{margin-left:var(--lado-mini)}
.menu-chk:checked~.lado .rot{display:none}
.menu-chk:checked~.lado .marca,.menu-chk:checked~.lado .usuario{justify-content:center;padding-left:0;padding-right:0}
.menu-chk:checked~.lado .grupo{height:1px;margin:.7rem 1rem;padding:0;background:rgba(255,255,255,.1)}
.menu-chk:checked~.lado nav a{justify-content:center;padding:.6rem 0}
.menu-chk:checked~.lado .cont{position:absolute;top:.1rem;right:.35rem;min-width:1.1rem;padding:0 .25rem;font-size:.62rem}
}
@media (max-width:960px){
.lado{transform:translateX(-100%);box-shadow:none}
.area{margin-left:0}
.menu-chk:checked~.lado{transform:none;box-shadow:0 0 40px rgba(0,0,0,.35)}
.menu-chk:checked~.veu{display:block;position:fixed;inset:0;z-index:25;background:rgba(15,23,42,.45)}
.selos{display:none}.conta{margin-left:auto}.conta .nome{display:none}
}
`

// ShieldSVG é o escudo, o ícone padrão quando não há logotipo.
const ShieldSVG = `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 3l8 3v6c0 4.5-3.4 8.3-8 9-4.6-.7-8-4.5-8-9V6l8-3z"/><path d="M8.5 12l2.5 2.5 4.5-5"/></svg>`

// Limites do logotipo (Aula 19.1).
const (
	MaxLogoSize = 256 << 10
	// MaxLogoSide: o maior lado, em pixels. Um PNG pequeno pode declarar 30000x30000 e pedir
	// gigabytes ao ser aberto: a conferência lê só o cabeçalho antes de decodificar.
	MaxLogoSide = 1024
)

// CheckLogo confere o arquivo pelos bytes, nunca pelo nome nem pelo tipo informado: PNG ou
// JPEG de verdade, inteiro, até MaxLogoSize e MaxLogoSide. O servidor confere no envio; o
// agente e a Central conferem de novo o que recebem.
func CheckLogo(b []byte) (contentType string, width, height int, err error) {
	switch {
	case len(b) == 0:
		return "", 0, 0, errors.New("o arquivo está vazio")
	case len(b) > MaxLogoSize:
		return "", 0, 0, fmt.Errorf("o arquivo tem %d KB; o limite é %d KB", (len(b)+1023)/1024, MaxLogoSize/1024)
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		contentType = "image/png"
	case bytes.HasPrefix(b, []byte{0xff, 0xd8, 0xff}):
		contentType = "image/jpeg"
	default:
		return "", 0, 0, errors.New("só PNG ou JPEG (o conteúdo não começa como nenhum dos dois)")
	}
	decodeConfig, decode := png.DecodeConfig, png.Decode
	if contentType == "image/jpeg" {
		decodeConfig, decode = jpeg.DecodeConfig, jpeg.Decode
	}
	cfg, err := decodeConfig(bytes.NewReader(b))
	if err != nil {
		return "", 0, 0, errors.New("a imagem está corrompida ou truncada")
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > MaxLogoSide || cfg.Height > MaxLogoSide {
		return "", 0, 0, fmt.Errorf("a imagem tem %dx%d pixels; o máximo é %dx%d", cfg.Width, cfg.Height, MaxLogoSide, MaxLogoSide)
	}
	if _, err := decode(bytes.NewReader(b)); err != nil {
		return "", 0, 0, errors.New("a imagem está corrompida ou truncada")
	}
	return contentType, cfg.Width, cfg.Height, nil
}

// Ícones de linha do menu (Aula 19.1), desenhados aqui: currentColor, sem arquivo de fora. Na
// Aula 19.1b vieram para cá, para a Central de software usar os mesmos.
func Icon(paths string) string {
	return `<svg class="ic" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">` + paths + `</svg>`
}

var (
	IcFrota       = Icon(`<rect x="3" y="4" width="18" height="12" rx="2"/><path d="M8 20h8M12 16v4"/>`)
	IcInventario  = Icon(`<rect x="5" y="3" width="14" height="18" rx="2"/><path d="M9 8h6M9 12h6M9 16h3"/>`)
	IcAposentadas = Icon(`<rect x="3" y="4" width="18" height="4" rx="1"/><path d="M5 8v11a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V8M10 12h4"/>`)
	IcGeral       = Icon(`<path d="M3 21h18M7 17v-6M12 17V5M17 17v-9"/>`)
	IcSistema     = Icon(`<rect x="3" y="3" width="18" height="18" rx="2"/><path d="M3 12h18M12 3v18"/>`)
	IcTerceiros   = Icon(`<path d="M21 8l-9-5-9 5v8l9 5 9-5z"/><path d="M3 8l9 5 9-5M12 13v8"/>`)
	IcCampanhas   = Icon(`<path d="M5 21V4"/><path d="M5 4h11l-2 4 2 4H5"/>`)
	IcJobs        = Icon(`<path d="M10 6h11M10 12h11M10 18h11"/><path d="M3 6l1.5 1.5L7 5M3 12l1.5 1.5L7 11M3 18l1.5 1.5L7 17"/>`)
	IcAneis       = Icon(`<circle cx="12" cy="12" r="9"/><circle cx="12" cy="12" r="5"/><circle cx="12" cy="12" r="1"/>`)
	IcLoja        = Icon(`<path d="M6 7h12l1 14H5z"/><path d="M9 10V6a3 3 0 0 1 6 0v4"/>`)
	IcConfig      = Icon(`<path d="M4 6h10M18 6h2M4 12h4M12 12h8M4 18h12"/><circle cx="16" cy="6" r="2"/><circle cx="10" cy="12" r="2"/><circle cx="18" cy="18" r="2"/>`)
	IcAuditoria   = Icon(`<path d="M14 3H6a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h5"/><path d="M14 3l5 5v3"/><circle cx="16.5" cy="16.5" r="3"/><path d="M21 21l-2.3-2.3"/>`)
	IcDescoberta  = Icon(`<circle cx="11" cy="11" r="7"/><path d="M21 21l-4.3-4.3"/><path d="M11 8v6M8 11h6"/>`)
	IcMenu        = Icon(`<path d="M4 6h16M4 12h16M4 18h16"/>`)
	IcSair        = Icon(`<path d="M9 21H6a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h3"/><path d="M16 17l5-5-5-5M21 12H9"/>`)
	IcAtualizar   = Icon(`<path d="M20 11a8 8 0 0 0-14.6-4.5L4 8"/><path d="M4 3v5h5"/><path d="M4 13a8 8 0 0 0 14.6 4.5L20 16"/><path d="M20 21v-5h-5"/>`)
	IcPedidos     = Icon(`<path d="M6 3h12v18l-3-2-3 2-3-2-3 2z"/><path d="M9 8h6M9 12h6"/>`)
	IcReinicio    = Icon(`<path d="M12 3v8"/><path d="M6.3 6.3a8 8 0 1 0 11.4 0"/>`)
	IcAbaixo      = Icon(`<path d="M6 9l6 6 6-6"/>`)
)
