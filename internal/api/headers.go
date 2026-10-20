package api

import "net/http"

// Cabeçalhos de segurança de toda resposta da API e do painel (Aula 20.4). Antes, cada
// página punha os seus (pageHeaders) e o resto (o JSON da API, os erros, os redirecionamentos)
// saía sem nada; a varredura do ZAP olha todas as respostas. Agora o valor seguro é o padrão,
// e quem precisa de outro (a página, com a CSP dela; o CSS e o ícone, com cache) troca.
const (
	// defaultCSP: uma resposta que não é página não carrega nada nem vai para frame. As
	// diretivas sem herança do default-src (frame-ancestors, form-action, base-uri) vêm
	// explícitas: sem elas, a regra 10055 do ZAP reclama, com razão.
	defaultCSP = "default-src 'none'; frame-ancestors 'none'; form-action 'none'; base-uri 'none'"
	// permissionsPolicy: o painel não usa nenhum recurso do navegador; nega os principais.
	permissionsPolicy = "accelerometer=(), camera=(), geolocation=(), gyroscope=(), magnetometer=(), microphone=(), payment=(), usb=()"
)

// SecurityHeaders põe os cabeçalhos antes do handler, que pode trocá-los.
//   - nosniff: o navegador não "adivinha" o tipo (um JSON lido como HTML);
//   - X-Frame-Options e frame-ancestors: nada dentro de frame de outro site (clickjacking);
//   - Cross-Origin-*: a página fica num processo só dela (Spectre) e os arquivos não são
//     carregados por outro site;
//   - Cache-Control no-store: nada do painel ou da API em cache, salvo quem disser.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Permissions-Policy", permissionsPolicy)
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Embedder-Policy", "require-corp")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Content-Security-Policy", defaultCSP)
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}
