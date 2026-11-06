package api_test

import (
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/jobs"
)

// Aula 16.6: o formulário da campanha em coluna, com os campos de cada tipo marcados (o CSS
// esconde os que não valem, por :has()) e os grupos de implantação vazios recolhidos.
func TestFormularioDaCampanha(t *testing.T) {
	_, priv, _ := jobs.GenerateKey()
	h, _, _ := ambientePaginas(t, api.WithJobKey(priv))
	b := pagina(h, "GET", "/painel/campanhas", sessaoAdmin, nil).Body.String()
	for _, quer := range []string{`<form class="campanha"`, `class="campo so-fontes"`, `class="campo so-reinicio"`,
		`<summary>Grupos de implantação sem máquinas</summary>`, `<script`} {
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
	i, j := strings.Index(b, `1 (padrão) <span class="muted">· 2 máq.`), strings.Index(b, "<summary>Grupos de implantação sem máquinas</summary>")
	k := strings.Index(b, `0 (piloto) <span class="muted">· 0 máq.`)
	if i < 0 || j < 0 || k < 0 || !(i < j && j < k) {
		t.Errorf("o grupo de implantação com máquinas fica à vista; o piloto vazio, recolhido (%d %d %d)", i, j, k)
	}
	// Um grupo de implantação vazio marcado (formulário de volta com erro) fica à vista.
	f := formCampanha("anel", "0", "nome", "")
	b = pagina(h, "POST", "/painel/campanhas/nova", sessaoAdmin, f).Body.String()
	k, j = strings.Index(b, `name="anel" value="0" checked`), strings.Index(b, "<summary>Grupos de implantação sem máquinas</summary>")
	if k < 0 || (j >= 0 && k > j) {
		t.Errorf("grupo de implantação vazio marcado recolhido (%d %d)", k, j)
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
	if css := pagina(h, "GET", "/painel/estilo.css", "", nil).Body.String(); !strings.Contains(css, `form.campanha:has(input[name=tipo]:checked:not([value=thirdparty]):not([value=terceiros_todos])) .so-fechar{display:none}`) {
		t.Error("CSS do :has()")
	}
}

// Campanha em horário fixo todo dia (de madrugada), sem depender da janela de cada grupo: a
// conferência diz o horário, e a campanha guarda a hora, a duração e o fuso do painel.
func TestCampanhaEmHorarioFixoPeloPainel(t *testing.T) {
	_, priv, _ := jobs.GenerateKey()
	h, st, _ := ambientePaginas(t, api.WithJobKey(priv), api.WithTimezone("America/Porto_Velho"))
	if b := pagina(h, "GET", "/painel/campanhas", sessaoAdmin, nil).Body.String(); !strings.Contains(b, `value="horario"`) ||
		!strings.Contains(b, `<input type="time" name="hora" value="02:00"`) || !strings.Contains(b, "fuso America/Porto_Velho") {
		t.Fatalf("formulário:\n%s", b)
	}
	f := formCampanha("tipo", "reboot", "aviso", "15", "quando", "horario", "hora", "02:30", "duracao", "180", "nome", "reinício de madrugada")
	b := pagina(h, "POST", "/painel/campanhas/nova", sessaoAdmin, f).Body.String()
	if !strings.Contains(b, "em horário fixo, todos 02:30-05:30 (America/Porto_Velho)") || !strings.Contains(b, `name="hora" value="02:30"`) ||
		strings.Contains(b, "Sem janela, cada máquina reinicia") {
		t.Fatalf("conferência:\n%s", b)
	}
	f.Set("confirmar", "1")
	pagina(h, "POST", "/painel/campanhas/nova", sessaoAdmin, f)
	l, _ := st.Campaigns(t.Context(), 5)
	if len(l) != 1 || l[0].Daily == nil || l[0].Daily.Start != 150*time.Minute || l[0].Daily.Duration != 3*time.Hour || l[0].UseWindow {
		t.Fatalf("campanha: %+v", l)
	}
	if b := pagina(h, "GET", "/painel/campanhas", sessaoAdmin, nil).Body.String(); !strings.Contains(b, "reiniciar, às 02:30") {
		t.Errorf("lista:\n%s", b)
	}
	// Hora fora do formato: volta com o erro.
	f = formCampanha("quando", "horario", "hora", "25:00", "duracao", "180")
	if b := pagina(h, "POST", "/painel/campanhas/nova", sessaoAdmin, f).Body.String(); !strings.Contains(b, "Horário fixo: a hora em HH:MM") {
		t.Error("hora inválida")
	}
}
