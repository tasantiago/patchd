package api_test

import (
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

func TestProgramasDeTerceiros(t *testing.T) {
	agora := time.Now().UTC()
	chrome := func(v, st string) protocol.ThirdPartyItem {
		return protocol.ThirdPartyItem{AppID: "google-chrome", Program: "Google Chrome", Name: "Google Chrome", State: st,
			Installed: v, Reference: "156.0.8078.12", Origin: "chrome:win64, 2026-10-13", Scope: "machine"}
	}
	vc := protocol.ThirdPartyItem{AppID: "vcredist-2008", Program: "Visual C++ 2008", Name: "Microsoft Visual C++ 2008 Redistributable - x86",
		State: "FORA DE SUPORTE", Installed: "9.0.30729.6161", Origin: "fim do suporte estendido em 2018-04-10", Scope: "machine"}
	ps := protocol.ThirdPartyItem{AppID: "powershell", Program: "PowerShell 7", Name: "PowerShell 7.6.6.0-x64", State: "em dia", Installed: "7.6.6.0", Reference: "7.6.6.0"}
	src := &complianceFalso{
		fleet: protocol.ComplianceFleet{EvaluatedAt: agora, Machines: []protocol.ComplianceStatus{
			{MachineID: "w1", Hostname: "pc-a", State: "desconhecido"}, {MachineID: "w2", Hostname: "<b>pc-b</b>", State: "em_dia"}}},
		terceiros: map[string][]protocol.ThirdPartyItem{
			"w1": {chrome("155.0.8059.40", "DESATUALIZADO"), vc, ps},
			"w2": {chrome("156.0.8078.12", "em dia"), ps},
		},
	}
	st := store.NewMemory()
	abreSessao(t, st, sessaoTeste, agora)
	h := api.New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), api.WithCompliance(src))

	b := pagina(h, "GET", "/painel/patch?aba=terceiros", sessaoTeste, nil).Body.String()
	for _, tr := range []string{
		`<span class="kpi-rot">Programas com problema</span><span class="kpi-val">2</span><span class="kpi-sub">em 1 sistemas</span>`,
		`<span class="kpi-rot">Instalações desatualizadas</span><span class="kpi-val v-laranja">1</span>`,
		`<span class="kpi-rot">Instalações fora de suporte</span><span class="kpi-val v-vermelho">1</span>`,
		`<a href="/painel/patch?aba=terceiros" class="ativa" aria-current="page">Programas de terceiros</a>`,
		`<a href="/painel/patch/programa?app=google-chrome">Google Chrome</a>`, "155.0.8059.40, 156.0.8078.12",
	} {
		if !strings.Contains(b, tr) {
			t.Errorf("lista: faltou %q", tr)
		}
	}
	// Fora de suporte primeiro; o que está em dia em todas só aparece com "todos".
	if strings.Index(b, "Visual C++ 2008") > strings.Index(b, "Google Chrome") || strings.Contains(b, "PowerShell 7") {
		t.Errorf("ordem e filtro:\n%s", b)
	}
	if b := pagina(h, "GET", "/painel/patch?aba=terceiros&todos=1", sessaoTeste, nil).Body.String(); !strings.Contains(b, "PowerShell 7") {
		t.Error("todos os identificados")
	}

	// O programa: as instalações, o problema primeiro; o nome da máquina escapado.
	b = pagina(h, "GET", "/painel/patch/programa?app=google-chrome", sessaoTeste, nil).Body.String()
	if !strings.Contains(b, `<span class="estado reboot_pendente">desatualizado</span>`) || !strings.Contains(b, "&lt;b&gt;pc-b&lt;/b&gt;") ||
		strings.Index(b, "pc-a") > strings.Index(b, "pc-b") {
		t.Errorf("programa:\n%s", b)
	}
	if r := pagina(h, "GET", "/painel/patch/programa?app=nao-existe", sessaoTeste, nil); r.Code != http.StatusNotFound {
		t.Errorf("programa desconhecido: %d", r.Code)
	}

	// Na máquina: o cartão com os dois com problema, e quantos em dia.
	b = pagina(h, "GET", "/painel/maquinas/w1?aba=pendencias", sessaoTeste, nil).Body.String()
	if !strings.Contains(b, "<h2>Programas de terceiros</h2><span class=\"muted\">1 em dia</span>") ||
		!strings.Contains(b, `<span class="estado faltando">fora de suporte</span><div class="muted nota">fim do suporte estendido em 2018-04-10</div>`) ||
		!strings.Contains(b, `Pendências <span class="cont">2</span>`) {
		t.Errorf("máquina:\n%s", b)
	}
}
