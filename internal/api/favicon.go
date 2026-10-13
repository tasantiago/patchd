package api

import "net/http"

// Favicon (Aula 13.2): o escudo do topo, branco num quadrado verde, para a aba do navegador
// e para os favoritos. SVG servido pelo próprio servidor (a CSP ganha img-src 'self'): sem
// arquivo externo, nítido em qualquer tamanho.
const faviconSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32">` +
	`<rect width="32" height="32" rx="7" fill="#0b7a45"/>` +
	`<g transform="translate(4 4)" fill="none" stroke="#fff" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round">` +
	`<path d="M12 3l8 3v6c0 4.5-3.4 8.3-8 9-4.6-.7-8-4.5-8-9V6l8-3z"/><path d="M8.5 12l2.5 2.5 4.5-5"/></g></svg>`

// faviconLink vai no <head> de todas as páginas do painel.
const faviconLink = `<link rel="icon" href="/painel/favicon.svg" type="image/svg+xml">`

// favicon é público (a tela de login também tem o ícone) e pode ficar em cache um dia.
func favicon(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "image/svg+xml")
	h.Set("Cache-Control", "public, max-age=86400")
	h.Set("X-Content-Type-Options", "nosniff")
	// Um SVG aberto direto no navegador é um documento: sem script, como as páginas.
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	_, _ = w.Write([]byte(faviconSVG))
}
