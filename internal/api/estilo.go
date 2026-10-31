package api

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
)

// Folha de estilo do painel num arquivo só (Aula 20.4). Até a 20.3 o CSS ia dentro de cada
// página, e a CSP precisava de style-src 'unsafe-inline': com ela, quem conseguisse pôr
// HTML numa página (o html/template impede, mas é a segunda linha de defesa) mudaria a
// aparência à vontade, ou vazaria dados por seletores de CSS. Agora a CSP pede style-src
// 'self', e o arquivo vem com a versão (o hash do conteúdo) na URL: cache longo, e uma
// versão nova do servidor muda o endereço.

// panelCSS: o tema (layout das aplicações, theme.go) e o que antes ficava em cada tela.
var panelCSS = themeCSS + loginCSS + paramCSS + selfServiceCSS + dashboardCSS + widthCSS()

// cssVersion: os primeiros 16 hexadecimais do SHA-256 do CSS.
var cssVersion = func() string {
	s := sha256.Sum256([]byte(panelCSS))
	return hex.EncodeToString(s[:8])
}()

// styleLink vai no <head> de todas as páginas do painel.
var styleLink = `<link rel="stylesheet" href="/painel/estilo.css?v=` + cssVersion + `">`

// styleFile: GET /painel/estilo.css. Público: a tela de login também usa.
func styleFile(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "text/css; charset=utf-8")
	h.Set("ETag", `"`+cssVersion+`"`)
	if r.URL.Query().Get("v") == cssVersion {
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "no-cache")
	}
	if r.Header.Get("If-None-Match") == `"`+cssVersion+`"` {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = w.Write([]byte(panelCSS))
}

// widthCSS: as larguras das barras (.w0 a .w100). Eram style="width:N%", e atributo style
// é CSS inline: a CSP sem 'unsafe-inline' o bloqueia.
func widthCSS() string {
	var b strings.Builder
	for i := 0; i <= 100; i++ {
		fmt.Fprintf(&b, ".w%d{width:%d%%}", i, i)
	}
	return b.String() + "\n"
}

// loginCSS: a tela de login e a de volta do RHBK (Aula 11.3; era o pageStyle).
const loginCSS = `
body.entrar{min-height:100vh;background:linear-gradient(180deg,var(--marinho) 0 15rem,var(--fundo) 15rem)}
.entrar-marca{display:flex;flex-direction:column;align-items:center;gap:.6rem;padding:3rem 1rem 1.6rem;color:#fff;font-weight:700;font-size:1.3rem}
.entrar-marca img{max-width:5.5rem;max-height:5.5rem;object-fit:contain}
.entrar-marca .escudo{display:grid;place-items:center;width:3.4rem;height:3.4rem;border-radius:12px;background:var(--destaque)}
.entrar-marca .escudo svg{width:2rem;height:2rem}
main.entrar{max-width:26rem;padding-top:0}main.entrar h1{margin-top:0}main.entrar .card{box-shadow:0 10px 30px rgba(17,24,39,.12)}
main.entrar label{display:block;margin-top:.9rem;font-weight:500}main.entrar input{display:block;width:100%;margin-top:.3rem}
main.entrar button{margin-top:1.3rem;width:100%;padding:.55rem}main.entrar small{color:var(--muted)}
a.sso{display:block;text-align:center;padding:.55rem;border:1px solid var(--azul);border-radius:6px;text-decoration:none;color:var(--azul);font-weight:500}
a.sso:hover{background:#eef5fd}.ou{text-align:center;color:var(--muted);margin:1rem 0 0}
main.entrar.volta{padding-top:4rem}
`

// paramCSS: Configuração › Parâmetros (Aula 19.2).
const paramCSS = `
.param{display:grid;grid-template-columns:minmax(12rem,16rem) minmax(0,1fr);gap:.4rem 1.2rem;padding:.9rem 0;border-top:1px solid var(--borda)}.param:first-of-type{border-top:0;padding-top:.2rem}
.param label{font-weight:600}.param .campo{display:flex;flex-wrap:wrap;align-items:center;gap:.5rem}.param .campo input[type=text]{flex:1 1 18rem;max-width:32rem}.param .campo input[type=number]{width:8rem}
.param .info{grid-column:2;font-size:.82rem;color:var(--muted);display:flex;flex-direction:column;gap:.15rem}.param .origem b{color:var(--texto);font-weight:500}
.fonte{display:inline-block;padding:0 .45rem;border-radius:999px;font-size:.72rem;font-weight:600;margin-right:.3rem}.fonte.tela{background:#eef0fd;color:var(--destaque)}.fonte.env{background:#eef1f4;color:#4b5563}.fonte.padrao{background:#f6f7f9;color:var(--muted)}
.param.problema{background:#fdf6f6}.param .erro-campo{color:var(--vermelho);font-size:.85rem}.param button.link{background:none;border:0;color:var(--azul);padding:0;font-size:.82rem;text-decoration:underline}
.param input:disabled,.param select:disabled{background:#f3f4f6;color:var(--muted);cursor:not-allowed}.param-salvar{display:flex;flex-wrap:wrap;gap:.7rem;align-items:center;margin-top:.6rem}.param label.caixa{font-weight:400;display:inline-flex;gap:.35rem;align-items:center;margin-right:.8rem}@media (max-width:700px){.param{grid-template-columns:1fr}.param .info{grid-column:1}}
`

// selfServiceCSS: a oferta em lote do autoatendimento e a decisão dos pedidos (Aulas 17.x).
const selfServiceCSS = `
.ss-lote{display:flex;flex-wrap:wrap;gap:.6rem 1rem;align-items:center;border:1px solid var(--borda);border-radius:var(--raio);padding:.7rem .9rem;margin:.9rem 0 0}.ss-lote legend{font-weight:600;padding:0 .3rem}.ss-lote label{display:flex;gap:.35rem;align-items:center}.ss-lote input[type=text]{width:16rem}tr.ss-erro td{background:#fdf2f2}.ss-erro-msg{color:var(--vermelho);font-size:.85rem;margin-top:.25rem}.ss-salvar{display:flex;gap:.6rem;align-items:center;margin:.9rem 0 0}
.decisao{display:flex;flex-wrap:wrap;gap:.4rem;align-items:center}.decisao form{display:flex;gap:.3rem;margin:0}.decisao input{width:13rem}
`
