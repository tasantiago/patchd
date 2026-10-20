package api_test

import (
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/jobs"
)

// Aula 16.6: o formulário da campanha em coluna, com os campos de cada tipo marcados (o CSS
// esconde os que não valem, por :has()) e os anéis vazios recolhidos.
func TestFormularioDaCampanha(t *testing.T) {
	_, priv, _ := jobs.GenerateKey()
	h, _, _ := ambientePaginas(t, api.WithJobKey(priv))
	b := pagina(h, "GET", "/painel/campanhas", sessaoAdmin, nil).Body.String()
	for _, quer := range []string{`<form class="campanha"`, `class="campo so-fontes"`, `class="campo so-reinicio"`,
		`<summary>Anéis sem máquinas</summary>`, `<script`} {
		if quer == `<script` {
			if strings.Contains(b, quer) {
				t.Error("o formulário não usa JavaScript")
			}
			continue
		}
		if !strings.Contains(b, quer) {
			t.Errorf("sem %q", quer)
		}
	}
	i, j := strings.Index(b, `1 (padrão) <span class="muted">· 2 máq.`), strings.Index(b, "<summary>Anéis sem máquinas</summary>")
	k := strings.Index(b, `0 (piloto) <span class="muted">· 0 máq.`)
	if i < 0 || j < 0 || k < 0 || !(i < j && j < k) {
		t.Errorf("o anel com máquinas fica à vista; o piloto vazio, recolhido (%d %d %d)", i, j, k)
	}
	// Um anel vazio marcado (formulário de volta com erro) fica à vista.
	f := formCampanha("anel", "0", "nome", "")
	b = pagina(h, "POST", "/painel/campanhas/nova", sessaoAdmin, f).Body.String()
	k, j = strings.Index(b, `name="anel" value="0" checked`), strings.Index(b, "<summary>Anéis sem máquinas</summary>")
	if k < 0 || (j >= 0 && k > j) {
		t.Errorf("anel vazio marcado recolhido (%d %d)", k, j)
	}
	// Trocou de tipo depois de digitar KBs: o campo some da tela e o texto é ignorado.
	f = formCampanha("fontes", "KB5127648", "tipo", "rescan")
	if b := pagina(h, "POST", "/painel/campanhas/nova", sessaoAdmin, f).Body.String(); !strings.Contains(b, `name="confirmar" value="1"`) {
		t.Errorf("rescan com o campo de fontes escondido preenchido:\n%s", b)
	}
	if b := pagina(h, "GET", "/painel/campanhas?tipo=reboot", sessaoAdmin, nil).Body.String(); !strings.Contains(b, `value="reboot" checked`) || !strings.Contains(b, `value="Reinício pendente"`) {
		t.Error("?tipo=reboot")
	}
	// Aula 20.4: o CSS saiu da página para /painel/estilo.css.
	if css := pagina(h, "GET", "/painel/estilo.css", "", nil).Body.String(); !strings.Contains(css, `form.campanha:has(input[name=tipo]:checked:not([value=reboot])) .so-reinicio{display:none}`) {
		t.Error("CSS do :has()")
	}
}
