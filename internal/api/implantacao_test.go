package api_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/campaign"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/window"
)

// Um parque fictício por prédio: Sede (1º andar com duas redes, 2º andar, uma máquina em
// conflito e uma sem andar), Fórum Geral (sem andares) e uma máquina sem patrimônio.
func parquePorPredio(t *testing.T, st *store.Memory) map[string]int64 {
	t.Helper()
	ctx := context.Background()
	maq := map[string]string{"EQ1001": "10.12.1.10/24", "EQ1002": "10.40.1.7/24", "EQ1003": "10.12.2.20/24", "EQ1004": "10.12.9.9/24",
		"EQ1005": "10.99.0.5/24", "EQ2001": "10.20.0.11/24", "EQ2002": "10.20.0.12/24", "SEMTOMBO": "10.30.0.1/24"}
	for nome, ip := range maq {
		if _, err := st.SaveInventory(ctx, nome, protocol.InventoryReport{SchemaVersion: 1, Hash: "h-" + nome, OS: protocol.OSInfo{Family: "windows", Hostname: nome}}); err != nil {
			t.Fatal(err)
		}
		s := &protocol.MachineState{Interfaces: []protocol.NetInterface{{Name: "eth0", MAC: "00:11:22:33:44:55", IPv4: []string{ip}, Up: true}}}
		if err := st.SaveHostState(ctx, nome, s, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.ReplaceAssetLocations(ctx, []store.AssetLocation{
		{Tombo: "1001", Building: "PREDIO SEDE"}, {Tombo: "1002", Building: "PREDIO SEDE"}, {Tombo: "1003", Building: "PREDIO SEDE"},
		{Tombo: "1004", Building: "PREDIO SEDE"}, {Tombo: "1005", Building: "PREDIO SEDE"},
		{Tombo: "2001", Building: "FORUM GERAL"}, {Tombo: "2002", Building: "FORUM GERAL"}, {Tombo: "3001", Building: "ANEXO VAZIO"},
	}, store.AssetImport{By: "t", File: "p.csv"}); err != nil {
		t.Fatal(err)
	}
	// 1º andar: duas redes com o mesmo nome; 2º andar; e duas regras de nomes diferentes que
	// casam o 10.12.9.x (conflito). A mesma regex no Fórum não vale para a Sede.
	if err := st.SaveFloorLocations(ctx, []store.FloorLocation{
		{FloorKey: store.FloorKey{Building: "PREDIO SEDE", Spec: `regex:^10\.12\.1\.[0-9]+$`}, Name: "1º andar"},
		{FloorKey: store.FloorKey{Building: "PREDIO SEDE", Spec: `regex:^10\.40\.1\.[0-9]+$`}, Name: "1º andar"},
		{FloorKey: store.FloorKey{Building: "PREDIO SEDE", Spec: `regex:^10\.12\.2\.[0-9]+$`}, Name: "2º andar"},
		{FloorKey: store.FloorKey{Building: "PREDIO SEDE", Spec: `regex:^10\.12\.9\.`}, Name: "3º andar"},
		{FloorKey: store.FloorKey{Building: "PREDIO SEDE", Spec: `regex:^10\.12\.[89]\.`}, Name: "4º andar"},
		{FloorKey: store.FloorKey{Building: "FORUM GERAL", Spec: `regex:^10\.12\.2\.[0-9]+$`}, Name: "Térreo"},
	}, "t"); err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	bs, _ := st.Buildings(ctx)
	for _, b := range bs {
		ids[b.Name] = b.ID
	}
	return ids
}

func TestPrediosEImplantacaoPeloPainel(t *testing.T) {
	h, st, _ := ambientePaginas(t, api.WithTimezone("America/Porto_Velho"))
	ctx := context.Background()
	ids := parquePorPredio(t, st)
	sede := fmt.Sprint(ids["PREDIO SEDE"])

	b := pagina(h, "GET", "/painel/grupos-implantacao", sessaoTeste, nil).Body.String()
	for _, quer := range []string{"› Prédios e implantação", "<b>Predio Sede</b>", "<b>Forum Geral</b>", "<b>Anexo Vazio</b>", "sem equipamentos",
		"5 equipamento(s) · 2 andar(es) identificado(s)", "1 em conflito", "1 sem andar", `<details class="lugar location-building" id="predio-` + sede + `">`,
		"Sem localização identificada", `/painel/inventario?predio=PREDIO&#43;SEDE`} {
		if !strings.Contains(b, quer) {
			t.Errorf("página: faltou %q", quer)
		}
	}
	if strings.Contains(b, "<details open") || strings.Contains(b, `location-building" open`) || strings.Contains(b, "Grupos técnicos legados") ||
		strings.Contains(b, `action="/painel/grupos-implantacao/predios/janela"`) {
		t.Error("recolhidos, sem grupos legados e sem formulário para a leitura")
	}
	if b := pagina(h, "GET", "/painel/grupos-implantacao?q=forum", sessaoTeste, nil).Body.String(); strings.Contains(b, "<b>Predio Sede</b>") || !strings.Contains(b, "<b>Forum Geral</b>") {
		t.Error("pesquisa por nome")
	}

	// Janela de um prédio: atravessando a meia-noite, no fuso do painel; erros voltam com o motivo.
	jan := url.Values{"predio": {sede}, "op": {"gravar"}, "dia": {"seg", "ter", "qua", "qui", "sex"}, "inicio": {"22:00"}, "fim": {"04:00"}, "fuso": {"America/Porto_Velho"}}
	if r := pagina(h, "POST", "/painel/grupos-implantacao/predios/janela", sessaoTeste, jan); r.Code != http.StatusForbidden {
		t.Errorf("leitura grava janela: %d", r.Code)
	}
	for campo, v := range map[string]string{"inicio": "25:00", "fim": "22:00", "fuso": "Marte/Base", "dia": "segunda"} {
		f := url.Values{}
		for k, x := range jan {
			f[k] = x
		}
		f.Set(campo, v)
		if l := local(pagina(h, "POST", "/painel/grupos-implantacao/predios/janela", sessaoAdmin, f)); !strings.Contains(l, "erro=janela") {
			t.Errorf("%s=%s: %s", campo, v, l)
		}
	}
	if l := local(pagina(h, "POST", "/painel/grupos-implantacao/predios/janela", sessaoAdmin, url.Values{"predio": {"999"}, "op": {"gravar"}})); !strings.Contains(l, "erro=predio") {
		t.Errorf("prédio desconhecido: %s", l)
	}
	if l := local(pagina(h, "POST", "/painel/grupos-implantacao/predios/janela", sessaoAdmin, jan)); !strings.Contains(l, "ok=janela") {
		t.Fatalf("gravar: %s", l)
	}
	ws, _ := st.BuildingWindows(ctx)
	if w := ws[ids["PREDIO SEDE"]]; w.Window.String() != "seg-sex 22:00-04:00 do dia seguinte (America/Porto_Velho)" || !w.Enabled {
		t.Fatalf("janela: %+v", ws)
	}
	if b := pagina(h, "GET", "/painel/grupos-implantacao?janela=sem", sessaoAdmin, nil).Body.String(); strings.Contains(b, "<b>Predio Sede</b>") || !strings.Contains(b, "<b>Forum Geral</b>") {
		t.Error("filtro sem janela")
	}

	// Em lote: selecionar os sem janela; sobrescrever a Sede pede confirmação.
	b = pagina(h, "GET", "/painel/grupos-implantacao/janelas?sel=sem", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, `value="`+fmt.Sprint(ids["FORUM GERAL"])+`" checked`) || strings.Contains(b, `value="`+sede+`" checked`) {
		t.Errorf("selecionar os sem janela:\n%s", b)
	}
	lote := url.Values{"op": {"gravar"}, "predio": {sede, fmt.Sprint(ids["FORUM GERAL"])}, "dia": {"sab"}, "inicio": {"08:00"}, "fim": {"18:00"}, "fuso": {"America/Porto_Velho"}}
	r := pagina(h, "POST", "/painel/grupos-implantacao/janelas", sessaoAdmin, lote)
	if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), "Janelas que serão sobrescritas") || !strings.Contains(r.Body.String(), "<td>Predio Sede</td><td>seg-sex 22:00-04:00") {
		t.Fatalf("confirmação: %d\n%s", r.Code, r.Body.String())
	}
	if ws, _ := st.BuildingWindows(ctx); len(ws) != 1 {
		t.Fatal("a confirmação gravou")
	}
	lote.Set("confirmar", "1")
	if l := local(pagina(h, "POST", "/painel/grupos-implantacao/janelas", sessaoAdmin, lote)); !strings.Contains(l, "ok=lote&n=2") {
		t.Fatalf("lote: %s", l)
	}
	ws, _ = st.BuildingWindows(ctx)
	if len(ws) != 2 || ws[ids["FORUM GERAL"]].Window.String() != "sab 08:00-18:00 (America/Porto_Velho)" || ws[ids["PREDIO SEDE"]].Window.Days.String() != "sab" {
		t.Fatalf("lote gravado: %+v", ws)
	}
	if ev, _ := st.AuditEvents(ctx, store.AuditFilter{Action: "janela."}); len(ev) != 2 || ev[0].Action != "janela.lote" || !strings.Contains(ev[0].Detail, "1 sobrescrita(s)") {
		t.Errorf("auditoria: %+v", ev)
	}
	// Desativar: a configuração fica e deixa de valer.
	if l := local(pagina(h, "POST", "/painel/grupos-implantacao/predios/janela", sessaoAdmin, url.Values{"predio": {sede}, "op": {"desativar"}})); !strings.Contains(l, "ok=desativada") {
		t.Fatalf("desativar: %s", l)
	}
	if ws, _ := st.BuildingWindows(ctx); ws[ids["PREDIO SEDE"]].Enabled {
		t.Fatal("continua ativa")
	}
	pagina(h, "POST", "/painel/grupos-implantacao/predios/janela", sessaoAdmin, url.Values{"predio": {sede}, "op": {"ativar"}})
}

func TestCampanhaPorPredioPeloPainel(t *testing.T) {
	_, priv, _ := jobs.GenerateKey()
	h, st, _ := ambientePaginas(t, api.WithJobKey(priv), api.WithTimezone("America/Porto_Velho"))
	ctx := context.Background()
	ids := parquePorPredio(t, st)
	sede, forum := fmt.Sprint(ids["PREDIO SEDE"]), fmt.Sprint(ids["FORUM GERAL"])
	andar1 := sede + "|andar:1º andar"

	// O formulário: prédios recolhidos com os andares dentro; nenhum grupo numérico.
	b := pagina(h, "GET", "/painel/campanhas", sessaoAdmin, nil).Body.String()
	for _, quer := range []string{`<details class="escopo-predio"><summary>`, `name="predio" value="` + sede + `"`, `name="andar" value="` + andar1 + `"`,
		"1º andar <span class=\"muted\">· 2</span>", `<optgroup label="Predio Sede">`, "Incluir os 3 equipamento(s) sem localização"} {
		if !strings.Contains(b, quer) {
			t.Errorf("formulário: faltou %q", quer)
		}
	}
	if strings.Contains(b, `name="anel"`) || strings.Contains(b, `<details class="escopo-predio" open>`) {
		t.Error("grupo numérico ou prédio aberto no formulário")
	}

	base := func(kv ...string) url.Values {
		f := url.Values{"nome": {"busca por prédio"}, "tipo": {"rescan"}, "observacao": {"24"}, "quando": {"prazo"}, "prazo": {"24"}}
		for i := 0; i+1 < len(kv); i += 2 {
			f[kv[i]] = strings.Split(kv[i+1], ";;")
		}
		return f
	}
	for nome, c := range map[string]struct {
		f   url.Values
		msg string
	}{
		"andar inexistente":        {base("andar", sede+"|andar:9º andar"), "não existe mais"},
		"piloto fora do escopo":    {base("predio", forum, "piloto", "andar", "piloto_andar", andar1), "O piloto precisa ser um prédio ou um andar do escopo"},
		"piloto é o escopo todo":   {base("predio", forum, "piloto", "predio", "piloto_predio", forum), "O piloto cobre todo o escopo"},
		"andar piloto sem máquina": {base("predio", sede, "piloto", "andar", "piloto_andar", sede+"|andar:3º andar"), "O andar piloto não existe ou não tem equipamentos"},
		"janela faltando":          {base("todos", "1", "quando", "janela"), "sem janela de manutenção ativa: Forum Geral, Predio Sede"},
	} {
		r := pagina(h, "POST", "/painel/campanhas/nova", sessaoAdmin, c.f)
		if r.Code != http.StatusUnprocessableEntity || !strings.Contains(r.Body.String(), c.msg) {
			t.Errorf("%s: %d\n%s", nome, r.Code, r.Body.String())
		}
	}

	// Todos os prédios, piloto no 1º andar da Sede (duas redes), na janela de cada prédio.
	loc, _ := time.LoadLocation("America/Porto_Velho")
	_ = st.SetBuildingWindows(ctx, []int64{ids["PREDIO SEDE"]}, window.Window{Days: window.AllDays, Start: 12 * time.Hour, Duration: 2 * time.Hour, Location: loc}, "t")
	_ = st.SetBuildingWindows(ctx, []int64{ids["FORUM GERAL"]}, window.Window{Days: window.AllDays, Start: 19 * time.Hour, Duration: 4 * time.Hour, Location: loc}, "t")
	f := base("todos", "1", "predio", sede, "andar", andar1, "piloto", "andar", "piloto_andar", andar1, "quando", "janela", "ligar", "1")
	b = pagina(h, "POST", "/painel/campanhas/nova", sessaoAdmin, f).Body.String()
	for _, quer := range []string{"Conferir a campanha", "Piloto — Predio Sede, 1º andar (2 equipamento(s))", "7 · 2 no piloto, 5 depois",
		"<td>Piloto — Predio Sede, 1º andar <span class=\"estado em_dia\">piloto</span></td><td class=\"num\">2</td>", "<td>Predio Sede</td><td class=\"num\">5</td><td class=\"num\">2</td><td class=\"num\">3</td>",
		"<td>Forum Geral</td><td class=\"num\">2</td><td class=\"num\">0</td><td class=\"num\">2</td>", "na próxima janela de manutenção do prédio de cada equipamento",
		"só para as máquinas da etapa liberada"} {
		if !strings.Contains(b, quer) {
			t.Errorf("conferência: faltou %q", quer)
		}
	}
	if strings.Contains(b, "SEMTOMBO") {
		t.Error("o sem localização entrou sem ser pedido")
	}
	r := pagina(h, "POST", "/painel/campanhas/nova", sessaoAdmin, confirmado(t, h, f))
	if local(r) != "/painel/campanhas/1?criada=1" {
		t.Fatalf("criar: %d %s\n%s", r.Code, local(r), r.Body.String())
	}
	c, _, _ := st.Campaign(ctx, 1)
	if c.Scope == nil || c.Scope.Pilot == nil || c.Scope.Pilot.Kind != campaign.PilotFloor || len(c.Rings) != 0 || !c.UseWindow || !c.Wake {
		t.Fatalf("campanha: %+v", c)
	}
	ts, _ := st.CampaignTargets(ctx, 1, 0)
	if len(ts) != 2 || ts[0].Floor != "1º andar" || ts[1].Floor != "1º andar" {
		t.Fatalf("piloto: %+v", ts)
	}
	if all, _ := st.CampaignTargets(ctx, 1, -1); len(all) != 7 {
		t.Fatalf("alvos: %d", len(all))
	}

	// O detalhe: etapas, piloto e o progresso por prédio (o resto aguarda o piloto).
	b = pagina(h, "GET", "/painel/campanhas/1", sessaoTeste, nil).Body.String()
	for _, quer := range []string{"Piloto — Predio Sede, 1º andar → observação de 1 dia → Demais equipamentos do escopo", "Progresso por prédio",
		"<td>Predio Sede — 1º andar</td><td class=\"num\">2</td>", "0 de 7 equipamento(s) concluído(s)", "Etapa 2: Demais equipamentos do escopo",
		"aguardando a liberação do piloto"} {
		if !strings.Contains(b, quer) {
			t.Errorf("detalhe: faltou %q", quer)
		}
	}
	if strings.Contains(b, "legada (grupos de implantação)") {
		t.Error("a nova aparece como legada")
	}
	// A lista mostra as etapas, não os grupos.
	if b := pagina(h, "GET", "/painel/campanhas", sessaoTeste, nil).Body.String(); !strings.Contains(b, "[Piloto — Predio Sede, 1º andar] → Demais equipamentos") {
		t.Error("lista sem as etapas")
	}
	// CSV com etapa, prédio e andar.
	csv := pagina(h, "GET", "/painel/campanhas/1/resultado.csv", sessaoTeste, nil).Body.String()
	if !strings.Contains(csv, "campanha;etapa;grupo_de_implantacao;predio;andar") {
		t.Errorf("CSV: %s", csv)
	}
}

// As janelas dos grupos antigos: só o prédio sem janela e com todas as máquinas num grupo com
// janela recebe; o ambíguo fica pendente; o que já tem janela nunca muda.
func TestJanelasDosGruposAntigos(t *testing.T) {
	h, st, _ := ambientePaginas(t)
	ctx := context.Background()
	ids := parquePorPredio(t, st)
	w := window.Window{Days: window.AllDays, Start: 2 * time.Hour, Duration: time.Hour, Location: time.UTC}
	_ = st.SetWindow(ctx, 1, w, "t")
	_ = st.SetWindow(ctx, 2, window.Window{Days: 1, Start: 3 * time.Hour, Duration: time.Hour, Location: time.UTC}, "t")
	// O Fórum inteiro no grupo 1 (com janela); a Sede dividida entre o 1 e o 2.
	if ok, _ := st.SetMachineRing(ctx, "EQ1003", 2); !ok {
		t.Fatal("anel")
	}
	b := pagina(h, "GET", "/painel/grupos-implantacao/janelas-legadas", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, "<td>Forum Geral</td><td>1</td>") || !strings.Contains(b, "<td>Predio Sede</td><td>máquinas em grupos com janelas diferentes") {
		t.Fatalf("plano:\n%s", b)
	}
	if l := local(pagina(h, "POST", "/painel/grupos-implantacao/janelas-legadas", sessaoAdmin, url.Values{})); !strings.Contains(l, "ok=legadas&n=1") {
		t.Fatalf("aplicar: %s", l)
	}
	ws, _ := st.BuildingWindows(ctx)
	if len(ws) != 1 || ws[ids["FORUM GERAL"]].Window.String() != w.String() {
		t.Fatalf("janelas: %+v", ws)
	}
	if b := pagina(h, "GET", "/painel/grupos-implantacao", sessaoAdmin, nil).Body.String(); !strings.Contains(b, "1 prédio(s) têm máquinas em grupos antigos com janelas diferentes") {
		t.Error("o aviso da pendência")
	}
}
