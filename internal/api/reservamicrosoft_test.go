package api_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
)

// A fonte reserva do Windows: o interruptor de Parâmetros vai no check-in (desligado por
// padrão), e a campanha do Windows ganha a opção, que a conferência explica.
func TestReservaMicrosoft(t *testing.T) {
	for valor, quer := range map[string]bool{"1": true, "0": false, "": false} {
		h, st, _ := ambientePaginas(t, api.WithSettings(parametrosFixos{api.MicrosoftFallbackKey: valor}))
		tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
		if _, err := st.CreateEnrollmentToken(context.Background(), hash, "t", time.Now().Add(time.Hour), 1); err != nil {
			t.Fatal(err)
		}
		m := registra(t, h, tok)
		corpo, _ := json.Marshal(protocol.CheckinRequest{AgentVersion: "v", SentAt: time.Now().UTC()})
		r := envia(h, "POST", "/api/v1/agent/checkin", m.Credential, corpo)
		var resp protocol.CheckinResponse
		_ = json.Unmarshal(r.Body.Bytes(), &resp)
		if resp.WinUpdateMicrosoft != quer {
			t.Errorf("%q: mandou %v", valor, resp.WinUpdateMicrosoft)
		}
		if !quer && strings.Contains(r.Body.String(), "winupdate_microsoft") {
			t.Errorf("%q: o campo vai mesmo desligado", valor)
		}
	}

	_, priv, _ := jobs.GenerateKey()
	for _, ligada := range []bool{false, true} {
		v := "0"
		if ligada {
			v = "1"
		}
		h, st, _ := ambientePaginas(t, api.WithJobKey(priv), api.WithSettings(parametrosFixos{api.MicrosoftFallbackKey: v}))
		b := pagina(h, "GET", "/painel/campanhas", sessaoAdmin, nil).Body.String()
		if !strings.Contains(b, `class="campo so-windows"`) || !strings.Contains(b, `name="microsoft" value="1"`) ||
			strings.Contains(b, "hoje sem efeito") == ligada {
			t.Fatalf("ligada %v: formulário", ligada)
		}
		f := formCampanha("tipo", "windows_update", "fontes", "KB5127648", "microsoft", "1", "nome", "outubro")
		b = pagina(h, "POST", "/painel/campanhas/nova", sessaoAdmin, f).Body.String()
		if !strings.Contains(b, "se a fonte da política falhar ou não oferecer o KB, direto da Microsoft") ||
			!strings.Contains(b, `<input type="hidden" name="microsoft" value="1">`) ||
			strings.Contains(b, "A fonte reserva está desligada em Configuração › Parâmetros") == ligada {
			t.Fatalf("ligada %v: conferência:\n%s", ligada, b)
		}
		f.Set("confirmar", "1")
		pagina(h, "POST", "/painel/campanhas/nova", sessaoAdmin, f)
		// Em outro tipo, a opção é ignorada.
		f = formCampanha("tipo", "rescan", "microsoft", "1", "confirmar", "1", "nome", "busca")
		pagina(h, "POST", "/painel/campanhas/nova", sessaoAdmin, f)
		l, _ := st.Campaigns(t.Context(), 5)
		if len(l) != 2 || !l[1].MicrosoftFallback || l[0].MicrosoftFallback {
			t.Fatalf("campanhas: %+v", l)
		}
		if b := pagina(h, "GET", "/painel/campanhas/"+itoa(l[1].ID), sessaoAdmin, nil).Body.String(); !strings.Contains(b, "<dt>Fonte do Windows</dt><dd>se a fonte da política falhar") {
			t.Errorf("detalhe:\n%s", b)
		}
	}
}
