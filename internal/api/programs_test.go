package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/export"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

// Inventário › Programas instalados: por sistema, junto por programa e fabricante, com os
// componentes do sistema escondidos; a página do programa mostra as versões e as máquinas.
func TestProgramasInstalados(t *testing.T) {
	h, st, _ := ambientePaginas(t)
	ctx := context.Background()
	win := func(id, host string, sw ...protocol.Software) {
		t.Helper()
		if _, err := st.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: "h-" + id,
			OS: protocol.OSInfo{Family: "windows", ID: "windows", Name: "Windows 11 Pro", Arch: "amd64", Hostname: host}, Software: sw}); err != nil {
			t.Fatal(err)
		}
	}
	lo := func(v, un string) protocol.Software {
		return protocol.Software{Name: "LibreOffice " + v, Version: v, Publisher: "The Document Foundation", Scope: "machine", Uninstall: un}
	}
	win("92fc3b93-0003", "PC-A", lo("25.2.1.1", "msi"), protocol.Software{Name: "Edge Update", Version: "1.3", Publisher: "Microsoft", Scope: "machine", SystemComponent: true})
	win("92fc3b93-0004", "PC-B", lo("26.8.3.1", "msi"), protocol.Software{Name: "Ferramenta", Version: "2.0", Publisher: "X", Scope: "user"})

	r := pagina(h, "GET", "/painel/inventario/programas", sessaoAdmin, nil)
	b := r.Body.String()
	if r.Code != http.StatusOK || !strings.Contains(b, "Programas instalados</a>") || !strings.Contains(b, ">LibreOffice</a>") ||
		!strings.Contains(b, "<td>The Document Foundation</td><td class=\"num\">2</td>") || strings.Contains(b, ">Edge Update</a>") ||
		!strings.Contains(b, "1 componente(s) do sistema") || !strings.Contains(b, `class="ativa" aria-current="page">Windows <span class="muted">2</span>`) {
		t.Fatalf("lista: %d\n%s", r.Code, b)
	}
	if !strings.Contains(b, `<td class="muted">silenciosa</td>`) || !strings.Contains(b, `<td class="muted">sem desinstalação silenciosa</td>`) {
		t.Error("remoção na lista")
	}
	if b := pagina(h, "GET", "/painel/inventario/programas?sistema=1", sessaoAdmin, nil).Body.String(); !strings.Contains(b, ">Edge Update</a> <span class=\"muted\">(sistema)</span>") {
		t.Error("com os componentes do sistema")
	}
	if b := pagina(h, "GET", "/painel/inventario/programas?q=ferr", sessaoAdmin, nil).Body.String(); !strings.Contains(b, ">Ferramenta</a>") || strings.Contains(b, ">LibreOffice</a>") {
		t.Error("busca")
	}
	if b := pagina(h, "GET", "/painel/inventario/programas?so=linux", sessaoAdmin, nil).Body.String(); !strings.Contains(b, "Nenhum programa no Linux") {
		t.Error("linux vazio")
	}
	// A página do programa: as duas versões e as duas máquinas.
	b = pagina(h, "GET", "/painel/inventario/programa?so=windows&nome=libreoffice&fab=The+Document+Foundation", sessaoAdmin, nil).Body.String()
	for _, quer := range []string{"<h1>LibreOffice</h1>", "<code>26.8.3.1</code></td><td class=\"num\">1</td>", ">PC-A</a>", ">PC-B</a>",
		"<code>LibreOffice 25.2.1.1</code>", "MSI (msiexec /x)"} {
		if !strings.Contains(b, quer) {
			t.Errorf("programa: faltou %q", quer)
		}
	}
	if r := pagina(h, "GET", "/painel/inventario/programa?so=windows&nome=nada&fab=", sessaoAdmin, nil); r.Code != http.StatusNotFound {
		t.Errorf("programa que não existe: %d", r.Code)
	}
	// Os CSVs: a lista com o filtro da tela, e as máquinas do programa.
	if !strings.Contains(b, `href="/painel/inventario/programa.csv?fab=The&#43;Document&#43;Foundation&amp;nome=libreoffice&amp;so=windows"`) {
		t.Error("link do CSV do programa")
	}
	if b := pagina(h, "GET", "/painel/inventario/programas?q=libre", sessaoAdmin, nil).Body.String(); !strings.Contains(b, `href="/painel/inventario/programas.csv?q=libre&amp;so=windows"`) {
		t.Error("link do CSV da lista")
	}
	r = pagina(h, "GET", "/painel/inventario/programas.csv?so=windows", sessaoTeste, nil)
	c := r.Body.String()
	if r.Code != http.StatusOK || !strings.HasPrefix(r.Header().Get("Content-Disposition"), `attachment; filename="patchd-programas-windows-`) ||
		!strings.Contains(c, "sistema;programa;fabricante;instalacoes;qtd_versoes;versoes;por_usuario;componente_do_sistema;nomes_no_inventario;com_desinstalacao_silenciosa;remocao\r\n") ||
		!strings.Contains(c, "Windows;LibreOffice;The Document Foundation;2;2;26.8.3.1 (1), 25.2.1.1 (1);0;não;LibreOffice 25.2.1.1 | LibreOffice 26.8.3.1;2;silenciosa\r\n") ||
		!strings.Contains(c, "Windows;Ferramenta;X;1;1;2.0 (1);1;não;Ferramenta;0;sem desinstalação silenciosa\r\n") || strings.Contains(c, "Edge Update") {
		t.Errorf("CSV da lista: %d %q\n%s", r.Code, r.Header().Get("Content-Disposition"), c)
	}
	if c := pagina(h, "GET", "/painel/inventario/programas.csv?so=windows&q=ferr&sistema=1", sessaoTeste, nil).Body.String(); strings.Contains(c, "LibreOffice") || !strings.Contains(c, "Ferramenta") {
		t.Errorf("CSV com a busca:\n%s", c)
	}
	r = pagina(h, "GET", "/painel/inventario/programa.csv?so=windows&nome=libreoffice&fab=The+Document+Foundation", sessaoTeste, nil)
	c = r.Body.String()
	if r.Code != http.StatusOK || !strings.HasPrefix(r.Header().Get("Content-Disposition"), `attachment; filename="patchd-programa-windows-libreoffice-`) ||
		!strings.HasPrefix(c, export.BOM+"maquina;id;programa;fabricante;nome_no_inventario;versao;escopo;usuario;remocao;removivel_pelo_patchd;product_code;chave\r\n") ||
		!strings.Contains(c, "PC-A;92fc3b93-0003;LibreOffice;The Document Foundation;LibreOffice 25.2.1.1;25.2.1.1;máquina;;MSI (msiexec /x);") ||
		strings.Count(c, "\r\n") != 3 {
		t.Errorf("CSV do programa: %d\n%s", r.Code, c)
	}
	if r := pagina(h, "GET", "/painel/inventario/programa.csv?so=windows&nome=nada&fab=", sessaoTeste, nil); r.Code != http.StatusNotFound {
		t.Errorf("CSV de programa que não existe: %d", r.Code)
	}
	if ev, _ := st.AuditEvents(ctx, store.AuditFilter{Action: "exportar.csv"}); len(ev) < 3 {
		t.Errorf("auditoria da exportação: %d", len(ev))
	}
	// O Inventário tem as duas abas.
	if b := pagina(h, "GET", "/painel/inventario", sessaoAdmin, nil).Body.String(); !strings.Contains(b, `href="/painel/inventario/programas">Programas instalados</a>`) {
		t.Error("aba no Inventário")
	}
}

// Remover um programa pelo painel: só com o agente que remove; o job leva a entrada do
// inventário do servidor (o formulário diz só o nome e a chave).
func TestRemoverProgramaPeloPainel(t *testing.T) {
	_, priv, _ := jobs.GenerateKey()
	h, st, _ := ambientePaginas(t, api.WithJobKey(priv))
	ctx := context.Background()
	const id = "92fc3b93-0003" // uma máquina do ambiente de páginas
	checkin := func(caps ...string) {
		if err := st.SetMachineCapabilities(ctx, id, caps); err != nil {
			t.Fatal(err)
		}
	}
	chave := `HKLM\SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\{11111111-2222-3333-4444-555555555555}`
	_, _ = st.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: "w",
		OS: protocol.OSInfo{Family: "windows", ID: "windows", Name: "Windows 11", Arch: "amd64", Hostname: "pc-x"},
		Software: []protocol.Software{
			{Name: "Programa MSI", Version: "2.0", Publisher: "X", Scope: "machine", Uninstall: "msi", ProductCode: "{11111111-2222-3333-4444-555555555555}", Source: chave},
			{Name: "Por usuário", Version: "1", Scope: "user", Uninstall: "quiet", Source: `HKU\S-1-5-21\Software\Microsoft\Windows\CurrentVersion\Uninstall\U`},
		}})
	aba := "/painel/maquinas/" + id + "?aba=software"
	remover := func(nome, ch string) string {
		return local(pagina(h, "POST", "/painel/maquinas/"+id+"/software/remover", sessaoAdmin, url.Values{"nome": {nome}, "chave": {ch}}))
	}

	// Agente antigo: sem o botão, com o aviso; o pedido forjado é recusado.
	checkin(protocol.CapabilityJobs)
	if b := pagina(h, "GET", aba, sessaoAdmin, nil).Body.String(); strings.Contains(b, "/software/remover") || !strings.Contains(b, "atualize o agente") {
		t.Error("agente antigo com o botão")
	}
	if l := remover("Programa MSI", chave); !strings.Contains(l, "erro=remover") {
		t.Errorf("agente antigo: %s", l)
	}
	// Agente novo: o botão só na entrada de máquina com desinstalação silenciosa.
	checkin(protocol.CapabilityJobs, protocol.CapabilityUninstall)
	b := pagina(h, "GET", aba, sessaoAdmin, nil).Body.String()
	if strings.Count(b, `action="/painel/maquinas/`+id+`/software/remover"`) != 1 || !strings.Contains(b, "Remover Programa MSI") {
		t.Errorf("botões:\n%s", b)
	}
	if l := remover("Por usuário", `HKU\S-1-5-21\Software\Microsoft\Windows\CurrentVersion\Uninstall\U`); !strings.Contains(l, "erro=remover") || !strings.Contains(l, "usu%C3%A1rio") {
		t.Errorf("por usuário: %s", l)
	}
	if l := remover("Programa MSI", chave+"x"); !strings.Contains(l, "erro=remover") {
		t.Errorf("chave forjada: %s", l)
	}
	l := remover("Programa MSI", chave)
	if !strings.Contains(l, "aba=tarefas&criado=1&job=") {
		t.Fatalf("remover: %s", l)
	}
	n, _ := strconv.ParseInt(l[strings.LastIndex(l, "job=")+4:], 10, 64)
	job, _, _ := st.JobForMachine(ctx, id, n)
	var payload jobs.Job
	_ = json.Unmarshal([]byte(job.Payload), &payload)
	p, err := jobs.ParseUninstallParams(payload.Params)
	if err != nil || p.Method != "msi" || p.ProductCode != "{11111111-2222-3333-4444-555555555555}" || p.Key != chave || job.CreatedBy != "thyago" {
		t.Fatalf("job: %+v %v", p, err)
	}
	if ev, _ := st.AuditEvents(ctx, store.AuditFilter{Action: "programa.remover"}); len(ev) != 1 || !strings.Contains(ev[0].Detail, "Programa MSI 2.0 · msi") {
		t.Errorf("auditoria: %+v", ev)
	}
	// Na página do programa: o Remover de cada máquina volta para ela.
	prog := "/painel/inventario/programa?so=windows&nome=programa+msi&fab=X"
	b = pagina(h, "GET", prog, sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, "Remover desta máquina") || !strings.Contains(b, `name="voltar" value="/painel/inventario/programa?fab=X&amp;nome=programa&#43;msi&amp;pagina=&amp;so=windows"`) {
		t.Errorf("página do programa:\n%s", b)
	}
	l = local(pagina(h, "POST", "/painel/maquinas/"+id+"/software/remover", sessaoAdmin,
		url.Values{"nome": {"Programa MSI"}, "chave": {chave}, "voltar": {"/painel/inventario/programa?fab=X&nome=programa+msi&so=windows"}}))
	if !strings.HasPrefix(l, "/painel/inventario/programa?") || !strings.Contains(l, "criado=1&job=") {
		t.Errorf("voltar: %s", l)
	}
	if b := pagina(h, "GET", l, sessaoAdmin, nil).Body.String(); !strings.Contains(b, "Remoção pedida (job") {
		t.Error("aviso na página do programa")
	}
}

// A campanha de remoção nasce na página do programa: o formulário (só admin), a conferência
// com a previsão por grupo e a criação com o programa e as versões.
func TestCampanhaDeRemocaoPeloPainel(t *testing.T) {
	_, priv, _ := jobs.GenerateKey()
	h, st, _ := ambientePaginas(t, api.WithJobKey(priv))
	ctx := context.Background()
	const id = "92fc3b93-0003"
	chave := `HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\{11111111-2222-3333-4444-555555555555}`
	_, _ = st.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: "w",
		OS: protocol.OSInfo{Family: "windows", ID: "windows", Name: "Windows 11", Arch: "amd64", Hostname: "pc-x"},
		Software: []protocol.Software{{Name: "Programa MSI", Version: "2.0", Publisher: "X", Scope: "machine", Uninstall: "msi",
			ProductCode: "{11111111-2222-3333-4444-555555555555}", Source: chave}}})
	st.SetMachineRing(ctx, id, 0)
	if err := st.SetMachineCapabilities(ctx, id, []string{protocol.CapabilityJobs, protocol.CapabilityUninstall}); err != nil {
		t.Fatal(err)
	}
	prog := "/painel/inventario/programa?so=windows&nome=programa+msi&fab=X"
	b := pagina(h, "GET", prog, sessaoAdmin, nil).Body.String()
	for _, tr := range []string{`<div class="card" id="campanha">`, `name="tipo" value="desinstalar"`, `name="remover_nome" value="programa msi"`,
		`name="remover_fab" value="X"`, `name="versao" value="2.0" checked`, `value="Remover Programa MSI"`} {
		if !strings.Contains(b, tr) {
			t.Errorf("formulário: faltou %q", tr)
		}
	}
	if strings.Contains(pagina(h, "GET", prog, sessaoTeste, nil).Body.String(), `id="campanha"`) {
		t.Error("leitura vê a campanha")
	}

	form := formCampanha("nome", "remover programa msi", "tipo", "desinstalar", "remover_so", "windows", "remover_nome", "programa msi",
		"remover_fab", "X", "versao", "2.0", "anel", "0")
	// Nenhuma versão marcada, ou programa forjado: volta à página do programa com o motivo.
	sem := formCampanha("tipo", "desinstalar", "remover_so", "windows", "remover_nome", "programa msi", "remover_fab", "X", "anel", "0")
	l := local(pagina(h, "POST", "/painel/campanhas/nova", sessaoAdmin, sem))
	if !strings.HasPrefix(l, "/painel/inventario/programa?") || !strings.Contains(l, "erro=campanha") || !strings.HasSuffix(l, "#campanha") {
		t.Fatalf("sem versão: %s", l)
	}
	if b := pagina(h, "GET", strings.TrimSuffix(l, "#campanha"), sessaoAdmin, nil).Body.String(); !strings.Contains(b, "A campanha de remoção não foi criada: Marque ao menos uma versão a remover.") {
		t.Error("o motivo na página do programa")
	}
	forjado := formCampanha("tipo", "desinstalar", "remover_so", "windows", "remover_nome", "outro", "remover_fab", "X", "versao", "2.0", "anel", "0")
	if l := local(pagina(h, "POST", "/painel/campanhas/nova", sessaoAdmin, forjado)); !strings.Contains(l, "erro=campanha") {
		t.Errorf("forjado: %s", l)
	}
	if l := local(pagina(h, "POST", "/painel/campanhas/nova", sessaoAdmin, formCampanha("tipo", "desinstalar", "remover_so", "linux",
		"remover_nome", "programa msi", "remover_fab", "X", "versao", "2.0", "anel", "0"))); !strings.Contains(l, "erro=campanha") {
		t.Errorf("linux: %s", l)
	}

	// Conferência: a previsão; nada criado.
	b = pagina(h, "POST", "/painel/campanhas/nova", sessaoAdmin, form).Body.String()
	for _, tr := range []string{"remover Programa MSI (X), todas as versões", "1 com o programa · 1 removível(is) pelo patchd",
		`name="remover_nome" value="programa msi"`, `name="versao" value="2.0"`, "Na remoção, cada máquina"} {
		if !strings.Contains(b, tr) {
			t.Errorf("conferência: faltou %q\n%s", tr, b)
		}
	}
	if l, _ := st.Campaigns(ctx, 5); len(l) != 0 {
		t.Fatal("a conferência criou campanha")
	}
	form.Set("confirmar", "1")
	if l := local(pagina(h, "POST", "/painel/campanhas/nova", sessaoAdmin, form)); l != "/painel/campanhas/1?criada=1" {
		t.Fatalf("criar: %s", l)
	}
	c, _, _ := st.Campaign(ctx, 1)
	if c.Type != jobs.TypeUninstall || c.Remove == nil || c.Remove.Program != "Programa MSI" || c.Remove.Publisher != "X" ||
		len(c.Remove.Names) != 1 || c.Remove.Names[0] != "Programa MSI" || c.Remove.Versions != nil || c.CreatedBy != "thyago" {
		t.Fatalf("criada: %+v %+v", c, c.Remove)
	}
	if b := pagina(h, "GET", "/painel/campanhas/1", sessaoAdmin, nil).Body.String(); !strings.Contains(b, "remover Programa MSI (X), todas as versões") {
		t.Errorf("página da campanha:\n%s", b)
	}
	// Corrigir volta à página do programa.
	form.Del("confirmar")
	form.Set("corrigir", "1")
	if l := local(pagina(h, "POST", "/painel/campanhas/nova", sessaoAdmin, form)); !strings.HasPrefix(l, "/painel/inventario/programa?") || strings.Contains(l, "erro=") {
		t.Errorf("corrigir: %s", l)
	}
}
