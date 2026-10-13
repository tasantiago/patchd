package api_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

func TestAbasDoEquipamento(t *testing.T) {
	h, st, _ := ambientePaginas(t)
	ctx := context.Background()
	const id = "60ef97e4-0001"
	maq := "/painel/maquinas/" + id
	sw := []protocol.Software{{Name: "<b>Hostil</b>", Version: "1.0", Publisher: "ACME", InstallDate: "2026-09-30", Scope: "machine"}}
	for i := range 60 {
		sw = append(sw, protocol.Software{Name: fmt.Sprintf("pacote-%02d", i), Version: "2." + fmt.Sprint(i), Scope: "user", SystemComponent: i == 3})
	}
	_, _ = st.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: "h-abas", CollectedAt: time.Now().UTC(),
		OS: protocol.OSInfo{Family: "windows", Name: "Windows 11 Pro", Edition: "Professional", Version: "25H2", Build: "26200.1", Arch: "amd64", Hostname: "vm"},
		Hardware: &protocol.Hardware{Manufacturer: "Dell Inc.", Model: "OptiPlex 7010", SerialNumber: "To be filled by O.E.M.", ChassisType: "3",
			BIOSVendor: "Dell", BIOSVersion: "1.2.3", CPUModel: "Intel Core i5", CPUThreads: 8, MemoryBytes: 16 << 30, Sources: []string{"WMI"}},
		Software: sw})
	_ = st.SaveHostState(ctx, id, estadoTeste(), "192.0.2.1")

	// Resumo: abas com as contagens, os dois cartões, o sistema e as roscas dos discos.
	b := pagina(h, "GET", maq, sessaoTeste, nil).Body.String()
	for _, tr := range []string{
		`<a href="/painel/maquinas/60ef97e4-0001" class="ativa" aria-current="page">Resumo</a>`,
		`<a href="/painel/maquinas/60ef97e4-0001?aba=software">Software <span class="cont">61</span></a>`,
		`<a href="/painel/maquinas/60ef97e4-0001?aba=pendencias">Pendências <span class="cont">1</span></a>`,
		"<h2>Equipamento</h2>", "<h2>Conformidade</h2>", "<dt>Categoria</dt><dd>Desktop</dd>",
		"<dt>Fabricante e modelo</dt><dd>Dell Inc. OptiPlex 7010</dd>", "<dt>Edição</dt><dd>Professional</dd>",
		`aria-label="/: 95% ocupado"`, `class="cheio" stroke-dasharray="214.88 226.19"`, "5,0 GiB livres de 100,0 GiB",
	} {
		if !strings.Contains(b, tr) {
			t.Errorf("resumo: faltou %q", tr)
		}
	}
	if strings.Contains(b, "<h2>Software instalado</h2>") || strings.Contains(b, "<code>curl</code>") {
		t.Error("o resumo não mostra as outras abas")
	}

	// Hardware: parâmetros; serial genérico aparece marcado.
	b = pagina(h, "GET", maq+"?aba=hardware", sessaoTeste, nil).Body.String()
	for _, tr := range []string{"<dt>Categoria</dt><dd>Desktop (chassi SMBIOS 3)</dd>", "<code>— (genérico: To be filled by O.E.M.)</code>",
		"<dt>Memória</dt><dd>16,0 GiB</dd>", "<dt>Processadores lógicos</dt><dd>8</dd>", "Fontes: WMI", "<h3>Interfaces</h3>"} {
		if !strings.Contains(b, tr) {
			t.Errorf("hardware: faltou %q", tr)
		}
	}

	// Software: 50 por página, em ordem de nome, com busca; o nome sai escapado.
	b = pagina(h, "GET", maq+"?aba=software", sessaoTeste, nil).Body.String()
	if !strings.Contains(b, "1–50 de 61") || !strings.Contains(b, "&lt;b&gt;Hostil&lt;/b&gt;") || strings.Contains(b, "<b>Hostil") ||
		!strings.Contains(b, `href="/painel/maquinas/60ef97e4-0001?aba=software&amp;pagina=2"`) || !strings.Contains(b, "<td>30/09/2026</td>") {
		t.Errorf("software, página 1:\n%s", b)
	}
	b = pagina(h, "GET", maq+"?aba=software&q=PACOTE-0&pagina=1", sessaoTeste, nil).Body.String()
	if !strings.Contains(b, "1–10 de 10") || !strings.Contains(b, "pacote-03 <span class=\"muted\">· componente do sistema</span>") || !strings.Contains(b, "<td>usuário</td>") {
		t.Errorf("software com busca:\n%s", b)
	}
	if b := pagina(h, "GET", maq+"?aba=software&pagina=99", sessaoTeste, nil).Body.String(); !strings.Contains(b, "51–61 de 61") {
		t.Error("página além da última: a última")
	}

	// Aba desconhecida: Resumo. Aviso de um formulário: a aba do formulário.
	if b := pagina(h, "GET", maq+"?aba=%3Cx%3E", sessaoTeste, nil).Body.String(); !strings.Contains(b, `class="ativa" aria-current="page">Resumo</a>`) {
		t.Error("aba desconhecida")
	}
	if b := pagina(h, "GET", maq+"?erro=nada-marcado", sessaoTeste, nil).Body.String(); !strings.Contains(b, `class="ativa" aria-current="page">Pendências`) {
		t.Error("o aviso do formulário de atualização abre Pendências")
	}
	if b := pagina(h, "GET", maq+"?criado=1&job=7", sessaoTeste, nil).Body.String(); !strings.Contains(b, `class="ativa" aria-current="page">Tarefas`) || !strings.Contains(b, "Job 7 criado") {
		t.Error("job criado abre Tarefas")
	}
}
