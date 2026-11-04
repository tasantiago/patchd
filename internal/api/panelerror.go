package api

import (
	"html/template"
	"net/http"
	"strings"
)

// isPanel: o pedido é de uma tela do painel (o navegador espera HTML, não o JSON da API).
func isPanel(r *http.Request) bool {
	return r.URL.Path == "/painel" || strings.HasPrefix(r.URL.Path, "/painel/")
}

// panelErrorPage: a falha interna numa tela do painel. Sem o layout (montá-lo também lê o
// banco, que pode ser o que falhou); o código do pedido acha a linha "falha interna" no log.
var panelErrorPage = template.Must(template.New("erro").Parse(`<!doctype html><html lang="pt-BR"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1"><title>Erro · patchd</title>` + faviconLink + styleLink + `</head>
<body class="entrar"><main class="entrar"><div class="card"><h1>Algo deu errado</h1>
<p>O servidor não conseguiu montar esta página. Tente de novo em instantes; se continuar, procure no log do servidor a linha <code>falha interna</code> com o código abaixo.</p>
<p>Código do pedido: <code>{{.}}</code></p>
<p><a href="/painel">Voltar ao início</a></p></div></main></body></html>`))

func panelError(w http.ResponseWriter, requestID string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusInternalServerError)
	_ = panelErrorPage.Execute(w, requestID)
}
