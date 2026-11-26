package api_test

import (
	"context"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/shield"
	"github.com/tasantiago/patchd/internal/store"
)

// formPolitica: o formulário da Política com os valores da política p.
func formPolitica(p shield.Policy) url.Values {
	f := url.Values{}
	if p.Enabled {
		f.Set("ligado", "1")
	}
	for _, r := range p.PilotRings {
		f.Add("piloto", strconv.Itoa(r))
	}
	for _, c := range shield.Criteria {
		cp := p.Criteria[c]
		if cp.Enabled {
			f.Set(string(c)+"_on", "1")
		}
		if cp.Mandatory {
			f.Set(string(c)+"_obrig", "1")
		}
		f.Set(string(c)+"_peso", strconv.FormatFloat(cp.Weight, 'f', -1, 64))
		f.Set(string(c)+"_prazo", strconv.Itoa(cp.GraceDays))
	}
	for _, s := range []shield.Status{shield.Parcial, shield.DentroPrazo, shield.AguardandoTI, shield.AguardandoUsuario} {
		f.Set("cred_"+string(s), strings.Replace(strconv.FormatFloat(p.Credits[s], 'f', -1, 64), ".", ",", 1))
	}
	for k, v := range map[string]int{"inv_dias": p.InventoryDays, "inv_limite": p.InventoryHardDays, "criticas_prazo": p.CriticalGraceDays,
		"quase": p.NearMin, "atencao": p.AttentionMin, "xp_dia": p.XP.ProtectedDay, "xp_reinicio": p.XP.Reboot, "xp_atualizacao": p.XP.Update,
		"xp_teto": p.XP.MaxPerDay, "rank_min": p.Ranking.MinGroup, "avisos_horas": p.Notify.MinHours, "exp_inicio": p.Notify.WorkStart, "exp_fim": p.Notify.WorkEnd} {
		f.Set(k, strconv.Itoa(v))
	}
	for _, d := range p.Workdays {
		f.Add("dias", strconv.Itoa(d))
	}
	var lv []string
	for _, l := range p.Levels {
		lv = append(lv, strconv.Itoa(l.MinDays)+";"+l.Name)
	}
	f.Set("niveis", strings.Join(lv, "\n"))
	f.Set("bonus", "5;20\n10;40")
	f.Set("marca", p.Brand.Name)
	f.Set("missao", p.Brand.Mission)
	f.Set("slogan", p.Brand.Slogan)
	f.Set("rank_coletivo", "1")
	f.Set("rank_predios", "1")
	f.Set("rank_andares", "1")
	f.Set("rank_salas", "1")
	f.Set("rank_min_sala", strconv.Itoa(p.Ranking.MinRoom))
	f.Set("avisos", "1")
	return f
}

func TestPainelDoShield(t *testing.T) {
	st := postgresDeTeste(t)
	ctx := context.Background()
	agora := time.Now().UTC()
	for cookie, role := range map[string]string{sessaoAdmin: panel.RoleAdmin, sessaoTeste: panel.RoleRead} {
		if err := st.CreatePanelSession(ctx, identity.Hash(cookie), panel.Session{Username: "u-" + role, Role: role, Source: panel.SourceLocal,
			CreatedAt: agora, ExpiresAt: agora.Add(time.Hour), LastSeenAt: agora}); err != nil {
			t.Fatal(err)
		}
	}
	h := api.New(st, slog.New(slog.DiscardHandler), api.WithCompliance(&complianceFalso{}))

	b := pagina(h, "GET", "/painel/shield", sessaoTeste, nil).Body.String()
	// O "+" sai escapado no HTML (o navegador mostra "Atende+ Shield").
	if !strings.Contains(b, "Desligado") || !strings.Contains(b, "Atende&#43; Shield") {
		t.Fatalf("visão geral desligada: %s", b)
	}
	for _, aba := range []string{"geral", "equipamentos", "pessoas", "missoes", "ranking", "excecoes", "politica", "auditoria", "xyz"} {
		if r := pagina(h, "GET", "/painel/shield?aba="+aba, sessaoTeste, nil); r.Code != 200 {
			t.Errorf("aba %s: %d", aba, r.Code)
		}
	}
	// Só o admin muda; de outra origem, ninguém.
	pol := shield.Default()
	pol.Enabled, pol.PilotRings = true, []int{0}
	f := formPolitica(pol)
	if r := pagina(h, "POST", "/painel/shield/politica", sessaoTeste, f); r.Code != 403 {
		t.Fatalf("leitura mudou a política: %d", r.Code)
	}
	if r := pagina(h, "POST", "/painel/shield/politica", sessaoAdmin, f, "Origin", "https://atacante.exemplo"); r.Code != 403 {
		t.Fatalf("outra origem: %d", r.Code)
	}
	r := pagina(h, "POST", "/painel/shield/politica", sessaoAdmin, f)
	if r.Code != 303 || !strings.Contains(r.Header().Get("Location"), "politica=1") {
		t.Fatalf("gravar: %d %s", r.Code, r.Header().Get("Location"))
	}
	row, ok, _ := st.ShieldPolicy(ctx)
	got, err := shield.Parse(row.Body)
	if !ok || err != nil || !got.Enabled || len(got.PilotRings) != 1 || got.Criteria[shield.CritOS].Weight != 30 || len(got.XP.StreakBonus) != 2 {
		t.Fatalf("política gravada: %+v %v", got, err)
	}
	// Peso fora da faixa: nada gravado, e o motivo volta.
	f.Set("so_peso", "300")
	r = pagina(h, "POST", "/painel/shield/politica", sessaoAdmin, f)
	if loc := r.Header().Get("Location"); !strings.Contains(loc, "erro=1") || !strings.Contains(loc, "peso") {
		t.Fatalf("peso inválido: %s", loc)
	}
	f.Set("so_peso", "30")
	f.Set("conquistas", `[{"code":"x","name":"X"}]`)
	if loc := pagina(h, "POST", "/painel/shield/politica", sessaoAdmin, f).Header().Get("Location"); !strings.Contains(loc, "erro=1") {
		t.Fatalf("conquista inválida aceita: %s", loc)
	}
	if h2, _ := st.ShieldPolicyHistory(ctx, 10); len(h2) != 1 {
		t.Fatalf("versões: %d", len(h2))
	}

	// Missão coletiva: rascunho, publicar.
	m := url.Values{"titulo": {"Semana do reinício"}, "tipo": {"coletiva"}, "percentual": {"90"}, "criterio": {"reinicio"},
		"inicio": {agora.Format(time.DateOnly)}, "fim": {agora.AddDate(0, 0, 7).Format(time.DateOnly)}, "xp": {"50"}}
	if loc := pagina(h, "POST", "/painel/shield/missoes", sessaoAdmin, m).Header().Get("Location"); !strings.Contains(loc, "missao=1") {
		t.Fatalf("missão: %s", loc)
	}
	ms, _ := st.ShieldMissions(ctx, 10)
	if len(ms) != 1 || ms[0].State != "rascunho" || ms[0].Goal.Percent != 90 {
		t.Fatalf("%+v", ms)
	}
	if loc := pagina(h, "POST", "/painel/shield/missoes/"+strconv.FormatInt(ms[0].ID, 10)+"/estado", sessaoAdmin, url.Values{"estado": {"publicada"}}).Header().Get("Location"); !strings.Contains(loc, "estado=1") {
		t.Fatalf("publicar: %s", loc)
	}
	m.Set("fim", agora.AddDate(0, 0, 400).Format(time.DateOnly))
	if loc := pagina(h, "POST", "/painel/shield/missoes", sessaoAdmin, m).Header().Get("Location"); !strings.Contains(loc, "erro=1") {
		t.Fatalf("missão longa demais aceita: %s", loc)
	}

	// Exceção para máquina que não existe: recusada.
	if loc := pagina(h, "POST", "/painel/shield/excecoes", sessaoAdmin, url.Values{"maquina": {"nao-existe"}, "criterio": {"so"}, "motivo": {"x"}, "dias": {"10"}}).Header().Get("Location"); !strings.Contains(loc, "erro=1") {
		t.Fatalf("exceção sem máquina: %s", loc)
	}

	// Ajuste de XP: com motivo, uma vez por formulário; o CSV não deixa passar fórmula.
	ids, _ := st.ShieldTouchAccounts(ctx, map[string]string{`EXEMPLO\ana`: "=HYPERLINK(\"x\")"})
	pid := strconv.FormatInt(ids[`exemplo\ana`], 10)
	aj := url.Values{"ref": {strings.Repeat("ab", 12)}, "pontos": {"25"}, "motivo": {"XP perdido na indisponibilidade"}}
	if loc := pagina(h, "POST", "/painel/shield/pessoas/"+pid+"/ajuste", sessaoAdmin, aj).Header().Get("Location"); !strings.Contains(loc, "ajuste=1") {
		t.Fatalf("ajuste: %s", loc)
	}
	if loc := pagina(h, "POST", "/painel/shield/pessoas/"+pid+"/ajuste", sessaoAdmin, aj).Header().Get("Location"); !strings.Contains(loc, "repetido=1") {
		t.Fatalf("reenvio: %s", loc)
	}
	aj.Set("ref", strings.Repeat("cd", 12))
	aj.Set("motivo", "")
	if loc := pagina(h, "POST", "/painel/shield/pessoas/"+pid+"/ajuste", sessaoAdmin, aj).Header().Get("Location"); !strings.Contains(loc, "erro=1") {
		t.Fatalf("ajuste sem motivo: %s", loc)
	}
	pl, _, _ := st.ShieldPlayerByAccount(ctx, `exemplo\ana`)
	if pl.XP != 25 {
		t.Fatalf("XP: %d", pl.XP)
	}
	// Zerar a pontuação: só o admin, com motivo e a palavra de confirmação.
	z := url.Values{"motivo": {"início do programa"}, "confirmar": {"zerar"}}
	if r := pagina(h, "POST", "/painel/shield/zerar", sessaoTeste, z); r.Code != 403 {
		t.Fatalf("leitura zerou: %d", r.Code)
	}
	if loc := pagina(h, "POST", "/painel/shield/zerar", sessaoAdmin, z).Header().Get("Location"); !strings.Contains(loc, "erro=1") || !strings.Contains(loc, "ZERAR") {
		t.Fatalf("zerar sem a confirmação: %s", loc)
	}
	if pl, _, _ := st.ShieldPlayerByAccount(ctx, `exemplo\ana`); pl.XP != 25 {
		t.Fatalf("a confirmação errada zerou: %d", pl.XP)
	}
	z.Set("confirmar", "ZERAR")
	if loc := pagina(h, "POST", "/painel/shield/zerar", sessaoAdmin, z).Header().Get("Location"); !strings.Contains(loc, "zerada=1") {
		t.Fatalf("zerar: %s", loc)
	}
	if pl, _, _ := st.ShieldPlayerByAccount(ctx, `exemplo\ana`); pl.XP != 0 || pl.Streak.LastDay == "" {
		t.Fatalf("depois de zerar: %+v", pl)
	}
	if b := pagina(h, "GET", "/painel/shield?aba=pessoas", sessaoAdmin, nil).Body.String(); !strings.Contains(b, "Pontuação zerada em") || !strings.Contains(b, "25 XP antes") {
		t.Fatalf("aba Pessoas sem o último zerar: %s", b)
	}
	// Grupos de competição: só o admin; grupo de prédios com dois prédios; o grupo por andar
	// recusa prédio com 3 andares ou menos; excluir solta os prédios.
	if err := st.ReplaceAssetLocations(ctx, []store.AssetLocation{{Tombo: "1", Building: "PREDIO SEDE"}, {Tombo: "2", Building: "FORUM GERAL"}},
		store.AssetImport{By: "t", File: "p.csv"}); err != nil {
		t.Fatal(err)
	}
	pr := map[string]string{}
	bl, _ := st.Buildings(ctx)
	for _, b := range bl {
		pr[b.Name] = strconv.FormatInt(b.ID, 10)
	}
	cg := url.Values{"tipo": {"predios"}, "nome": {"Fóruns do interior"}, "predio": {pr["PREDIO SEDE"], pr["FORUM GERAL"]}}
	if r := pagina(h, "POST", "/painel/shield/competicoes", sessaoTeste, cg); r.Code != 403 {
		t.Fatalf("leitura criou grupo de competição: %d", r.Code)
	}
	if loc := pagina(h, "POST", "/painel/shield/competicoes", sessaoAdmin, cg).Header().Get("Location"); !strings.Contains(loc, "competicao=1") {
		t.Fatalf("grupo de prédios: %s", loc)
	}
	if loc := pagina(h, "POST", "/painel/shield/competicoes", sessaoAdmin, cg).Header().Get("Location"); !strings.Contains(loc, "erro=1") {
		t.Fatalf("nome e prédios repetidos aceitos: %s", loc)
	}
	ca := url.Values{"tipo": {"andares"}, "predio": {pr["FORUM GERAL"]}}
	if loc := pagina(h, "POST", "/painel/shield/competicoes", sessaoAdmin, ca).Header().Get("Location"); !strings.Contains(loc, "erro=1") || !strings.Contains(loc, "mais+de+3+andares") {
		t.Fatalf("grupo por andar num prédio sem andares: %s", loc)
	}
	if b := pagina(h, "GET", "/painel/shield?aba=ranking", sessaoAdmin, nil).Body.String(); !strings.Contains(b, "Grupos de competição") ||
		!strings.Contains(b, "<b>Fóruns do interior</b>") || !strings.Contains(b, "Novo grupo por andar") || !strings.Contains(b, "Ranking de todos os prédios") {
		t.Fatalf("aba Ranking sem o grupo: %s", b)
	}
	if b := pagina(h, "GET", "/painel/shield?aba=ranking", sessaoTeste, nil).Body.String(); strings.Contains(b, "/painel/shield/competicoes") {
		t.Error("leitura vê os formulários dos grupos de competição")
	}
	comps, _ := st.ShieldCompetitions(ctx)
	if len(comps) != 1 || len(comps[0].Buildings) != 2 || comps[0].Separate {
		t.Fatalf("grupos: %+v", comps)
	}
	cid := strconv.FormatInt(comps[0].ID, 10)
	// Ranking geral à parte: marcado na edição, aparece na aba.
	cg.Set("id", cid)
	cg.Set("nome", "Fóruns digitais")
	cg.Set("a_parte", "1")
	if loc := pagina(h, "POST", "/painel/shield/competicoes", sessaoAdmin, cg).Header().Get("Location"); !strings.Contains(loc, "competicao=1") {
		t.Fatalf("grupo à parte: %s", loc)
	}
	if comps, _ := st.ShieldCompetitions(ctx); len(comps) != 1 || !comps[0].Separate || comps[0].Name != "Fóruns digitais" {
		t.Fatalf("à parte: %+v", comps)
	}
	if b := pagina(h, "GET", "/painel/shield?aba=ranking", sessaoAdmin, nil).Body.String(); !strings.Contains(b, "com ranking geral à parte") {
		t.Fatal("aba Ranking sem o grupo à parte")
	}
	if loc := pagina(h, "POST", "/painel/shield/competicoes/"+cid+"/excluir", sessaoAdmin, url.Values{}).Header().Get("Location"); !strings.Contains(loc, "competicao_excluida=1") {
		t.Fatalf("excluir: %s", loc)
	}
	if comps, _ := st.ShieldCompetitions(ctx); len(comps) != 0 {
		t.Fatalf("grupo continua: %+v", comps)
	}
	csv := pagina(h, "GET", "/painel/shield/pessoas.csv", sessaoTeste, nil)
	if !strings.HasPrefix(csv.Body.String(), "\xef\xbb\xbf") || !strings.Contains(csv.Body.String(), `'=HYPERLINK`) {
		t.Fatalf("CSV: %q", csv.Body.String())
	}
	if pagina(h, "GET", "/painel/shield/equipamentos.csv", sessaoTeste, nil).Code != 200 {
		t.Fatal("CSV das máquinas")
	}
	// Nomes das faixas como andares: só o admin; nome vazio volta ao padrão.
	andar := url.Values{"faixa": {"10.1.2.0/24", "10.1.3.0/24"}, "nome": {"2º andar", ""}}
	if r := pagina(h, "POST", "/painel/shield/andares", sessaoTeste, andar); r.Code != 403 {
		t.Fatalf("leitura nomeou andar: %d", r.Code)
	}
	if loc := pagina(h, "POST", "/painel/shield/andares", sessaoAdmin, andar).Header().Get("Location"); !strings.Contains(loc, "andares=1") {
		t.Fatalf("andares: %s", loc)
	}
	row, _, _ = st.ShieldPolicy(ctx)
	got, _ = shield.Parse(row.Body)
	if got.Ranking.FloorNames["10.1.2.0/24"] != "2º andar" || len(got.Ranking.FloorNames) != 1 || !got.Ranking.Rooms || got.Ranking.MinRoom != 3 {
		t.Fatalf("nomes dos andares: %+v", got.Ranking)
	}
	andar.Set("nome", strings.Repeat("x", 41))
	andar.Del("faixa")
	andar.Set("faixa", "10.1.2.0/24")
	if loc := pagina(h, "POST", "/painel/shield/andares", sessaoAdmin, andar).Header().Get("Location"); !strings.Contains(loc, "erro=1") {
		t.Fatalf("nome longo demais: %s", loc)
	}
	if b := pagina(h, "GET", "/painel/shield?aba=ranking", sessaoTeste, nil).Body.String(); !strings.Contains(b, "Ranking de todos os prédios") {
		t.Fatal("aba Ranking sem os prédios")
	}
	// A auditoria do Shield mostra tudo isso.
	b = pagina(h, "GET", "/painel/shield?aba=auditoria", sessaoAdmin, nil).Body.String()
	for _, want := range []string{"shield.politica", "shield.missao", "shield.missao_estado", "shield.ajuste_xp", "XP perdido", "shield.andares", "shield.zerar", "início do programa", "shield.competicao", "shield.competicao_excluir"} {
		if !strings.Contains(b, want) {
			t.Errorf("auditoria sem %q", want)
		}
	}
	// A visão de leitura não tem os formulários de admin.
	b = pagina(h, "GET", "/painel/shield?aba=pessoas", sessaoTeste, nil).Body.String()
	if strings.Contains(b, "/ajuste") || strings.Contains(b, "/zerar") {
		t.Error("leitura vê o ajuste de XP ou o zerar")
	}
}
