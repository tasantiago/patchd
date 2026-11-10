package api_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/biospw"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/machinebox"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

var listaDoLote = regexp.MustCompile(`name="lista" value="([0-9a-f]{32})"`)

// TestSenhaDaBIOSEmLote (docs/bios-senha.md, seção 12): o lote define a senha nas máquinas sem
// senha, troca a das que têm o marcador do .bat (o agente lê a atual) e a das que têm uma
// padrão (a cadastrada ou a digitada), com conferência, motivo e trilha; uma falha tira a
// máquina dos lotes seguintes até alguém pedir, e duas a tiram de vez.
func TestSenhaDaBIOSEmLote(t *testing.T) {
	ctx := context.Background()
	const lote = "/painel/bios/lote"
	const padrao, digitada = "Frota#2024!", "Outra#2023"
	guardaPriv, _ := machinebox.Generate()
	guardaPub, _ := machinebox.Public(guardaPriv)
	_, jobPriv, _ := jobs.GenerateKey()
	caixaPriv, _ := machinebox.Generate()
	caixaPub, _ := machinebox.Public(caixaPriv)
	h, st, log := ambientePaginas(t, api.WithBIOSEscrow(guardaPriv), api.WithJobKey(jobPriv))

	maquina := func(id, nome, iface string, senha, caixa, marcador bool, grupo int) {
		t.Helper()
		_, err := st.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: id,
			OS: protocol.OSInfo{Family: "windows", Hostname: nome}, Hardware: &protocol.Hardware{Sources: []string{},
				Firmware: &protocol.Firmware{Interface: iface, AdminPassword: &senha}}})
		if err != nil {
			t.Fatal(err)
		}
		e := estadoTeste()
		if caixa {
			e.BoxKey = machinebox.EncodePublic(caixaPub)
		}
		e.BIOSLegacy, e.BIOSCopy = marcador, true
		if err := st.SaveHostState(ctx, id, e, "192.0.2.1"); err != nil {
			t.Fatal(err)
		}
		if _, err := st.SetMachineRing(ctx, id, grupo); err != nil {
			t.Fatal(err)
		}
	}
	const a, b, c, d, e = "aaaaaaaa-000a", "bbbbbbbb-000b", "cccccccc-000c", "dddddddd-000d", "eeeeeeee-000e"
	maquina(a, "pc-a", "hp", false, true, false, 1)    // sem senha
	maquina(b, "pc-b", "hp", true, true, true, 1)      // com a senha do .bat no Registro
	maquina(c, "pc-c", "lenovo", true, true, false, 1) // com a padrão da Lenovo (cadastrada)
	maquina(d, "pc-d", "dell", true, true, false, 2)   // com senha, e a Dell sem padrão cadastrada
	maquina(e, "pc-e", "hp", false, false, false, 1)   // sem a caixa: fora do alcance
	env, _ := biospw.SealFleet(guardaPub, "lenovo", padrao)
	if err := st.SetBIOSFleetPassword(ctx, store.BIOSFleetPassword{Vendor: "lenovo", Escrow: env, EscrowKeyID: machinebox.KeyID(guardaPub),
		SetAt: time.Now(), SetBy: "thyago"}); err != nil {
		t.Fatal(err)
	}

	if r := pagina(h, "GET", lote, sessaoTeste, nil); r.Code != http.StatusForbidden {
		t.Errorf("leitura vê o lote: %d", r.Code)
	}
	pg := pagina(h, "GET", lote, sessaoAdmin, nil).Body.String()
	for _, quer := range []string{"Sem senha na BIOS", `id="sit-definir"`, `id="sit-marcador"`, `id="sit-padrao"`, "7 máquina(s)", `href="/painel/bios/lote?ver=bloqueada">3</a>`} {
		if !strings.Contains(pg, quer) {
			t.Errorf("tela: faltou %q", quer)
		}
	}
	if strings.Contains(pg, `id="conferencia"`) {
		t.Error("a conferência aparece sem lote escolhido")
	}
	if pg := pagina(h, "GET", lote+"?ver=bloqueada", sessaoAdmin, nil).Body.String(); !strings.Contains(pg, "pc-e") || !strings.Contains(pg, "chave de cifragem") {
		t.Error("lista das fora do alcance: faltou a pc-e com o motivo")
	}

	todas := url.Values{"sit": {"definir", "marcador", "padrao"}, "limite": {"50"}}
	confere := func(f url.Values) (string, string) {
		t.Helper()
		pg := pagina(h, "GET", lote+"?"+f.Encode(), sessaoAdmin, nil).Body.String()
		m := listaDoLote.FindStringSubmatch(pg)
		if m == nil {
			return pg, ""
		}
		return pg, m[1]
	}
	envia := func(f url.Values, lista, motivo, senha string) string {
		t.Helper()
		g := url.Values{"lista": {lista}, "motivo": {motivo}, "senha_atual": {senha}}
		for k, v := range f {
			g[k] = v
		}
		return local(pagina(h, "POST", lote, sessaoAdmin, g))
	}
	pg, lista := confere(todas)
	for _, quer := range []string{"4 máquina(s) neste lote", "pc-a", "pc-b", "pc-c", "pc-d", "a do Registro, lida pelo agente", "a padrão da Lenovo",
		"só com a senha digitada abaixo", "nenhuma (a BIOS está sem senha)", `name="senha_atual"`, "Gravar a senha em 4 máquina(s)"} {
		if !strings.Contains(pg, quer) {
			t.Errorf("conferência: faltou %q", quer)
		}
	}
	if strings.Contains(pg, "pc-e") {
		t.Error("conferência: a máquina sem a caixa entrou")
	}

	// Recusas: nada sai.
	for nome, c := range map[string]struct{ lista, motivo, senha, quer string }{
		"sem motivo":            {lista, "curto", "", "erro=motivo"},
		"lista diferente":       {strings.Repeat("0", 32), "chamado 4700, primeira troca", "", "erro=mudou"},
		"dell sem a digitada":   {lista, "chamado 4700, primeira troca", "", "erro=falta-senha"},
		"digitada com controle": {lista, "chamado 4700, primeira troca", "a\tb", "erro=senha"},
		"vírgula na lenovo":     {lista, "chamado 4700, primeira troca", "a,b", "erro=senha"},
	} {
		if l := envia(todas, c.lista, c.motivo, c.senha); !strings.Contains(l, c.quer) {
			t.Errorf("%s: %s", nome, l)
		}
	}
	if l := local(pagina(h, "POST", lote, sessaoTeste, url.Values{"sit": {"definir"}, "lista": {lista}, "motivo": {"chamado 4700, primeira troca"}})); l != "" {
		t.Errorf("leitura grava em lote: %s", l)
	}
	for _, id := range []string{a, b, c, d} {
		if l, _ := st.BIOSPasswords(ctx, id); len(l) != 0 {
			t.Fatalf("%s: uma recusa criou senha na guarda", id)
		}
	}

	job := func(id string) (jobs.BIOSPasswordParams, store.JobRow) {
		t.Helper()
		jl, _ := st.Jobs(ctx, id, 1)
		if len(jl) == 0 {
			t.Fatalf("%s: nenhum job", id)
		}
		j, _, _ := st.JobForMachine(ctx, id, jl[0].ID)
		var payload jobs.Job
		_ = json.Unmarshal([]byte(j.Payload), &payload)
		p, err := jobs.ParseBIOSPasswordParams(payload.Params)
		if err != nil {
			t.Fatal(err)
		}
		return p, j
	}
	atual := func(p jobs.BIOSPasswordParams) string {
		raw, _ := base64.StdEncoding.DecodeString(p.Current)
		cur, err := machinebox.Open(caixaPriv, machinebox.PurposeBIOSCurrent, raw)
		if err != nil {
			t.Fatalf("a senha atual do job não abre: %v", err)
		}
		return string(cur)
	}

	// O piloto: uma máquina (a primeira pelo nome), sem senha.
	piloto := url.Values{"sit": {"definir", "marcador"}, "limite": {"1"}}
	_, lista = confere(piloto)
	if l := envia(piloto, lista, "chamado 4700, piloto da guarda", ""); l != lote+"?feitas=1&puladas=0" {
		t.Fatalf("piloto: %s", l)
	}
	if p, _ := job(a); p.Current != "" || p.CurrentFrom != "" || p.Copy == "" || p.Vendor != "hp" {
		t.Errorf("job da pc-a (definição): %+v", p)
	}
	// O resto do grupo 1: a do marcador e a da padrão cadastrada.
	grupo1 := url.Values{"sit": {"definir", "marcador", "padrao"}, "limite": {"50"}, "grupo": {"1"}}
	pg, lista = confere(grupo1)
	if !strings.Contains(pg, "2 máquina(s) neste lote") || strings.Contains(pg, `"senha_atual" maxlength="64" autocomplete="off" required`) {
		t.Errorf("conferência do grupo 1 (a pc-a já está a caminho, e a padrão dispensa a digitada)")
	}
	if l := envia(grupo1, lista, "chamado 4700, grupo 1", ""); l != lote+"?feitas=2&puladas=0" {
		t.Fatalf("grupo 1: %s", l)
	}
	if p, _ := job(b); p.CurrentFrom != jobs.BIOSCurrentMarker || p.Current != "" {
		t.Errorf("job da pc-b (marcador): %+v", p)
	}
	if p, _ := job(c); atual(p) != padrao || p.Vendor != "lenovo" {
		t.Errorf("job da pc-c (padrão cadastrada): %+v", p)
	}
	// O grupo 2: a Dell, com a senha digitada no lote.
	grupo2 := url.Values{"sit": {"padrao"}, "limite": {"50"}, "grupo": {"2"}}
	_, lista = confere(grupo2)
	if l := envia(grupo2, lista, "chamado 4700, grupo 2", digitada); l != lote+"?feitas=1&puladas=0" {
		t.Fatalf("grupo 2: %s", l)
	}
	if p, _ := job(d); atual(p) != digitada {
		t.Errorf("job da pc-d (digitada): %+v", p)
	}

	// A trilha: um registro por lote, com o motivo, e a de cada máquina. Nenhuma senha.
	ev, _ := st.AuditEvents(ctx, store.AuditFilter{Action: "bios.senha.lote"})
	if len(ev) != 3 || !strings.Contains(ev[0].Detail, "motivo: chamado 4700, grupo 2") || !strings.Contains(ev[0].Detail, "digitada pelo admin") ||
		!strings.Contains(ev[1].Detail, "1 com a senha do .bat no Registro, 1 com senha padrão; senha atual das com padrão: a cadastrada do fabricante") {
		t.Errorf("trilha do lote: %+v", ev)
	}
	gr, _ := st.AuditEvents(ctx, store.AuditFilter{Action: "bios.senha.gravar"})
	pr, _ := st.AuditEvents(ctx, store.AuditFilter{Action: "bios.senha.preparar"})
	if len(gr) != 4 || len(pr) != 4 || !strings.Contains(gr[0].Detail, "· lote ") {
		t.Errorf("trilha das máquinas: %d gravar, %d preparar", len(gr), len(pr))
	}
	todosEv, _ := st.AuditEvents(ctx, store.AuditFilter{})
	for _, e := range todosEv {
		if strings.Contains(e.Detail, padrao) || strings.Contains(e.Detail, digitada) {
			t.Fatalf("senha na trilha: %+v", e)
		}
	}
	if strings.Contains(log.String(), padrao) || strings.Contains(log.String(), digitada) {
		t.Error("senha no log do servidor")
	}

	// Com os jobs a caminho, as quatro saem dos lotes.
	pg = pagina(h, "GET", lote, sessaoAdmin, nil).Body.String()
	if !strings.Contains(pg, `href="/painel/bios/lote?ver=andamento">4</a>`) {
		t.Error("contagem: as 4 gravações a caminho")
	}
	if _, lista := confere(todas); lista != "" {
		t.Error("máquina com gravação a caminho voltou para o lote")
	}

	// A pc-a falhou (a BIOS recusou a senha nova): só volta a pedido, e duas falhas a tiram.
	falha := func(id, detalhe string) {
		t.Helper()
		_, j := job(id)
		if _, err := st.FinishJob(ctx, j.ID, jobs.StateFailed, detalhe, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	falha(a, "o agente informou falha: hp respondeu 5 (parâmetro inválido)")
	falha(b, "o agente informou falha: hp respondeu 6 (acesso negado)")
	if pg, lista := confere(todas); lista != "" && strings.Contains(pg, "pc-a") {
		t.Error("a máquina que falhou voltou sem pedir")
	}
	comFalhas := url.Values{"sit": {"definir", "marcador", "padrao"}, "limite": {"50"}, "falhas": {"1"}}
	pg, lista = confere(comFalhas)
	if !strings.Contains(pg, "1 máquina(s) neste lote") || !strings.Contains(pg, "pc-a") || strings.Contains(pg, ">pc-b<") {
		t.Errorf("com as falhas: só a pc-a (a do marcador recusada fica para uma pessoa)")
	}
	if pg := pagina(h, "GET", lote+"?ver=pessoa", sessaoAdmin, nil).Body.String(); !strings.Contains(pg, "pc-b") || !strings.Contains(pg, "recusou a senha do marcador") {
		t.Error("a pc-b não está em 'precisa de uma pessoa'")
	}
	if l := envia(comFalhas, lista, "chamado 4701, nova tentativa", ""); l != lote+"?feitas=1&puladas=0" {
		t.Fatalf("nova tentativa: %s", l)
	}
	falha(a, "o agente informou falha: hp respondeu 5 (parâmetro inválido)")
	if _, lista := confere(comFalhas); lista != "" {
		t.Error("com duas falhas em 24 h a máquina ainda entra em lote")
	}

	// A pc-c recusou a padrão da Lenovo: repetir a mesma é errar de novo; só com outra, digitada.
	falha(c, "o agente informou falha: lenovo respondeu Access Denied")
	soPadrao := url.Values{"sit": {"padrao"}, "limite": {"50"}, "falhas": {"1"}, "grupo": {"1"}}
	pg, lista = confere(soPadrao)
	if !strings.Contains(pg, "pc-c") || !strings.Contains(pg, "só com a senha digitada abaixo") {
		t.Error("conferência: a pc-c pede a senha digitada")
	}
	if l := envia(soPadrao, lista, "chamado 4702, segunda padrão", ""); !strings.Contains(l, "erro=falta-senha") {
		t.Errorf("padrão recusada, sem a digitada: %s", l)
	}
	if l := envia(soPadrao, lista, "chamado 4702, segunda padrão", digitada); l != lote+"?feitas=1&puladas=0" {
		t.Fatalf("padrão recusada, com a digitada: %s", l)
	}
	if p, _ := job(c); atual(p) != digitada {
		t.Errorf("job da pc-c (segunda padrão): %+v", p)
	}
}
