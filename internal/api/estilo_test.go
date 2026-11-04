package api_test

import (
	"strings"
	"testing"
)

// A tela de entrar centraliza o cartão: o main dela tem largura máxima e margem automática
// (o main do painel ocupa a largura toda e não centraliza mais nada).
func TestEntrarCentralizado(t *testing.T) {
	h, _, _ := ambientePaginas(t)
	css := pagina(h, "GET", "/painel/estilo.css", "", nil).Body.String()
	if !strings.Contains(css, "main.entrar{max-width:26rem;margin:0 auto") {
		t.Fatal("o cartão de entrar não está centralizado (main.entrar sem margin:0 auto)")
	}
}
