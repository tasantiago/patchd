package shield

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var agora = time.Date(2026, 11, 25, 15, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

// limpa: a máquina toda em dia, com evidência de tudo.
func limpa() Evidence {
	inv := agora.Add(-2 * time.Hour)
	return Evidence{
		LastSeen: agora.Add(-time.Minute), InventoryAt: &inv,
		OSEvaluated: true, TPKnown: true, Reboot: ptr(false),
		StdApplies: true, StdKnown: true,
		AgentOutdated: ptr(false), AgentKnown: true,
	}
}

func status(r Result, c Criterion) CriterionResult {
	for _, x := range r.Criteria {
		if x.Criterion == c {
			return x
		}
	}
	return CriterionResult{}
}

func TestPadraoValido(t *testing.T) {
	p := Default()
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if p.Enabled {
		t.Fatal("o módulo nasce desligado")
	}
	if len(p.Achievements) != 13 {
		t.Fatalf("conquistas iniciais: %d", len(p.Achievements))
	}
	if p.Criteria[CritAntivirus].Enabled {
		t.Fatal("antivírus sem evidência não pode nascer ligado")
	}
	if p.Ranking.Individual || !p.Ranking.Collective {
		t.Fatal("ranking individual desligado e coletivo ligado por padrão")
	}
}

func TestMaquinaLimpaTemSelo(t *testing.T) {
	p := Default()
	r := Evaluate(&p, limpa(), agora)
	if r.Score == nil || *r.Score != 100 || !r.Sealed || r.Display != 100 || r.Overall != Protected {
		t.Fatalf("esperado selo 100%%: %+v", r)
	}
	if len(r.Blockers) != 0 || r.UserFault {
		t.Fatalf("sem bloqueios: %+v", r)
	}
}

func TestFormulaDaPontuacao(t *testing.T) {
	p := Default()
	e := limpa()
	e.OSPending = 3
	e.Since = map[Criterion]time.Time{CritOS: agora.Add(-30 * 24 * time.Hour)} // prazo vencido
	r := Evaluate(&p, e, agora)
	// Pesos ligados: 30+25+15+15+5+5 = 95. SO aguardando a TI: 30×0,5 = 15. Obtidos: 80.
	want := 80.0 / 95.0 * 100
	if r.Score == nil || abs(*r.Score-want) > 0.01 {
		t.Fatalf("pontuação %v, esperado %.2f", r.Score, want)
	}
	if r.Sealed || r.Display != 84 {
		t.Fatalf("display %d (para baixo), selado %v", r.Display, r.Sealed)
	}
	if got := status(r, CritOS); got.Status != AguardandoTI || got.Blame != BlameIT {
		t.Fatalf("SO: %+v", got)
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func TestNuncaMostra100SemSelo(t *testing.T) {
	p := Default()
	// Peso enorme em tudo e um critério minúsculo parcial: 99,99... arredondaria para 100.
	for c, cp := range p.Criteria {
		cp.Weight = 100
		p.Criteria[c] = cp
	}
	cp := p.Criteria[CritInventory]
	cp.Weight = 0.01
	p.Criteria[CritInventory] = cp
	e := limpa()
	old := agora.Add(-5 * 24 * time.Hour)
	e.InventoryAt = &old // parcial
	r := Evaluate(&p, e, agora)
	if r.Sealed || r.Display == 100 {
		t.Fatalf("mostrou 100 sem selo: %+v", r)
	}
	if r.Display != 99 {
		t.Fatalf("display %d", r.Display)
	}
}

func TestDesconhecidoNaoSomaEImpedeSelo(t *testing.T) {
	p := Default()
	e := limpa()
	e.Reboot = nil
	r := Evaluate(&p, e, agora)
	if r.Sealed {
		t.Fatal("selo com reinício desconhecido")
	}
	if got := status(r, CritReboot); got.Status != Desconhecido || got.Obtained != 0 {
		t.Fatalf("reinício: %+v", got)
	}
	if *r.Score >= 100 {
		t.Fatal("desconhecido somou")
	}
}

func TestSemContatoTudoDesconhecido(t *testing.T) {
	p := Default()
	e := limpa()
	e.Stale = true
	e.LastSeen = agora.Add(-10 * 24 * time.Hour)
	r := Evaluate(&p, e, agora)
	if r.Sealed || r.Overall != NoData || *r.Score != 0 {
		t.Fatalf("sem contato: %+v", r)
	}
	if got := status(r, CritOS); got.Status != Desconhecido || got.Blame != BlameExternal || !strings.Contains(got.Detail, "10 dias") {
		t.Fatalf("SO: %+v", got)
	}
}

func TestCriticaBloqueiaSeloMesmoNoPrazo(t *testing.T) {
	p := Default()
	e := limpa()
	e.OSPending, e.OSCritical = 1, 1
	r := Evaluate(&p, e, agora)
	if r.Sealed {
		t.Fatal("selo com crítica pendente")
	}
	if got := status(r, CritOS); got.Status != DentroPrazo {
		t.Fatalf("no prazo das críticas: %+v", got)
	}
	e.Since = map[Criterion]time.Time{CritOS: agora.Add(-3 * 24 * time.Hour)} // passou dos 2 dias
	r = Evaluate(&p, e, agora)
	if got := status(r, CritOS); got.Status != NaoConforme {
		t.Fatalf("crítica vencida: %+v", got)
	}
}

func TestReinicioDoUsuario(t *testing.T) {
	p := Default()
	e := limpa()
	e.Reboot = ptr(true)
	r := Evaluate(&p, e, agora)
	if got := status(r, CritReboot); got.Status != DentroPrazo || got.Blame != BlameUser || got.Deadline == nil {
		t.Fatalf("reinício no prazo: %+v", got)
	}
	if r.UserFault {
		t.Fatal("no prazo não é falta do usuário")
	}
	e.Since = map[Criterion]time.Time{CritReboot: agora.Add(-2 * 24 * time.Hour)}
	r = Evaluate(&p, e, agora)
	if got := status(r, CritReboot); got.Status != AguardandoUsuario || !r.UserFault || r.Blame != BlameUser {
		t.Fatalf("reinício vencido: %+v / %+v", got, r)
	}
	// O "fora desde" passa para a próxima avaliação.
	if s := r.SinceMap()[CritReboot]; !s.Equal(agora.Add(-2 * 24 * time.Hour)) {
		t.Fatalf("since %v", s)
	}
}

func TestTerceirosNaCentral(t *testing.T) {
	p := Default()
	e := limpa()
	e.TPOutdated, e.TPUserFixable = 2, 1
	e.Since = map[Criterion]time.Time{CritThirdParty: agora.Add(-8 * 24 * time.Hour)}
	r := Evaluate(&p, e, agora)
	if got := status(r, CritThirdParty); got.Status != AguardandoUsuario || got.Blame != BlameUser {
		t.Fatalf("terceiros: %+v", got)
	}
	e.TPUserFixable = 0
	r = Evaluate(&p, e, agora)
	if got := status(r, CritThirdParty); got.Status != AguardandoTI {
		t.Fatalf("sem Central: %+v", got)
	}
	// Fora de suporte não conta: só ele, o critério fica conforme.
	e.TPOutdated, e.TPUnsupported, e.TPUnsupportedApps = 0, 3, []string{"Visual C++ 2008 Redistributable 9.0"}
	r = Evaluate(&p, e, agora)
	if got := status(r, CritThirdParty); got.Status != Conforme || got.Obtained != got.Weight || strings.Contains(got.Detail, "suporte") {
		t.Fatalf("fora de suporte: %+v", got)
	}
}

func TestExcecaoSaiDoDenominadorMasNaoDaSelo(t *testing.T) {
	p := Default()
	e := limpa()
	e.TPOutdated = 1
	e.Exceptions = map[Criterion]bool{CritThirdParty: true}
	r := Evaluate(&p, e, agora)
	if got := status(r, CritThirdParty); got.Status != Excecao || got.Obtained != 0 {
		t.Fatalf("exceção: %+v", got)
	}
	if *r.Score != 100 {
		t.Fatalf("exceção fora do denominador: %v", *r.Score)
	}
	if r.Sealed || r.Display != 99 {
		t.Fatalf("exceção em obrigatório não dá selo: %+v", r)
	}
	// Num critério não obrigatório, a exceção não impede o selo.
	e = limpa()
	e.StdFailed = 1
	e.Exceptions = map[Criterion]bool{CritStandard: true}
	r = Evaluate(&p, e, agora)
	if !r.Sealed {
		t.Fatalf("exceção em critério opcional: %+v", r)
	}
}

func TestNaoAplicavel(t *testing.T) {
	p := Default()
	e := limpa()
	e.StdApplies = false
	e.AgentOutdated = nil
	r := Evaluate(&p, e, agora)
	if !r.Sealed || *r.Score != 100 {
		t.Fatalf("não aplicável fica fora: %+v", r)
	}
}

func TestInventarioPorIdade(t *testing.T) {
	p := Default()
	for _, c := range []struct {
		dias int
		want Status
	}{{1, Conforme}, {5, Parcial}, {9, NaoConforme}} {
		e := limpa()
		at := agora.Add(-time.Duration(c.dias) * 24 * time.Hour)
		e.InventoryAt = &at
		if got := status(Evaluate(&p, e, agora), CritInventory); got.Status != c.want {
			t.Errorf("%d dias: %s, esperado %s", c.dias, got.Status, c.want)
		}
	}
}

func TestSemCriterioNenhum(t *testing.T) {
	p := Default()
	e := limpa()
	e.StdApplies, e.AgentOutdated = false, nil
	for c, cp := range p.Criteria {
		if c != CritStandard && c != CritAgent {
			cp.Enabled = false
			p.Criteria[c] = cp
		}
	}
	r := Evaluate(&p, e, agora)
	if r.Score != nil || r.Overall != NoData || r.Sealed {
		t.Fatalf("nada conta: %+v", r)
	}
}

func TestAntivirusLigadoSemEvidencia(t *testing.T) {
	p := Default()
	cp := p.Criteria[CritAntivirus]
	cp.Enabled = true
	p.Criteria[CritAntivirus] = cp
	r := Evaluate(&p, limpa(), agora)
	if r.Sealed {
		t.Fatal("antivírus sem evidência deu selo")
	}
}

func TestValidacao(t *testing.T) {
	casos := map[string]func(p *Policy){
		"conforme vale menos": func(p *Policy) { p.Credits[Conforme] = 0.9 },
		"desconhecido soma":   func(p *Policy) { p.Credits[Desconhecido] = 0.1 },
		"parcial igual a 1":   func(p *Policy) { p.Credits[Parcial] = 1 },
		"sem peso": func(p *Policy) {
			for c, cp := range p.Criteria {
				cp.Enabled = false
				p.Criteria[c] = cp
			}
		},
		"nível fora de ordem": func(p *Policy) { p.Levels[1].MinDays = 1 },
		"conquista repetida":  func(p *Policy) { p.Achievements = append(p.Achievements, p.Achievements[0]) },
		"medida desconhecida": func(p *Policy) { p.Achievements[0].Metric = "x" },
		"feriado inválido":    func(p *Policy) { p.Holidays = []string{"25/12/2026"} },
		"fuso inválido":       func(p *Policy) { p.Timezone = "Lua/Base" },
		"nome com controle":   func(p *Policy) { p.Brand.Name = "a\x00b" },
		"expediente invertido": func(p *Policy) {
			p.Notify.WorkStart, p.Notify.WorkEnd = 18, 8
		},
		"critério desconhecido": func(p *Policy) { p.Criteria["bitlocker"] = CriterionPolicy{Enabled: true, Weight: 1} },
	}
	for nome, muda := range casos {
		p := Default()
		muda(&p)
		if err := p.Validate(); err == nil {
			t.Errorf("%s: aceito", nome)
		}
	}
}

func TestParseCompletaComPadrao(t *testing.T) {
	p, err := Parse([]byte(`{"enabled":true,"pilot_rings":[0],"criteria":{"so":{"enabled":true,"weight":50,"mandatory":true,"grace_days":3}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !p.Enabled || p.Criteria[CritOS].Weight != 50 || p.Criteria[CritThirdParty].Weight != 25 {
		t.Fatalf("%+v", p.Criteria)
	}
	if !p.InScope(0) || p.InScope(1) {
		t.Fatal("piloto")
	}
	b, _ := json.Marshal(p)
	if q, err := Parse(b); err != nil || q.Criteria[CritOS].Weight != 50 {
		t.Fatal("ida e volta", err)
	}
	if _, err := Parse([]byte(`{"credits":{"conforme":0.5}}`)); err == nil {
		t.Fatal("crédito inválido aceito")
	}
}

func dia(s string) time.Time {
	d, _ := time.Parse(time.DateOnly, s)
	return d
}

func TestVeredito(t *testing.T) {
	p := Default()
	p.Holidays = []string{"2026-11-20"}
	sel := MachineDay{MachineID: "a", Sealed: true}
	casos := []struct {
		nome string
		dia  string
		ms   []MachineDay
		want DayVerdict
	}{
		{"sábado", "2026-11-21", []MachineDay{sel}, DaySkip},
		{"feriado", "2026-11-20", []MachineDay{sel}, DaySkip},
		{"não usou", "2026-11-24", nil, DaySkip},
		{"protegido", "2026-11-24", []MachineDay{sel}, DayProtected},
		{"uma de duas fora (TI)", "2026-11-24", []MachineDay{sel, {MachineID: "b"}}, DayPause},
		{"sem dado", "2026-11-24", []MachineDay{{MachineID: "a", NoData: true}}, DayPause},
		{"falta do usuário", "2026-11-24", []MachineDay{sel, {MachineID: "b", UserFault: true}}, DayBreak},
	}
	for _, c := range casos {
		if got := Verdict(&p, dia(c.dia), c.ms); got != c.want {
			t.Errorf("%s: %s, esperado %s", c.nome, got, c.want)
		}
	}
}

func TestSequencia(t *testing.T) {
	var s Streak
	seq := []struct {
		d string
		v DayVerdict
	}{
		{"2026-11-02", DayProtected}, {"2026-11-03", DayProtected}, {"2026-11-04", DayPause},
		{"2026-11-05", DayProtected}, {"2026-11-06", DayBreak}, {"2026-11-09", DayProtected},
	}
	for _, x := range seq {
		s.Step(dia(x.d), x.v)
	}
	if s.Current != 1 || s.Best != 3 || s.Protected != 4 || s.Breaks != 1 {
		t.Fatalf("%+v", s)
	}
	// Idempotente: o mesmo dia, ou um anterior, não muda nada.
	if s.Step(dia("2026-11-09"), DayProtected) || s.Step(dia("2026-11-01"), DayProtected) {
		t.Fatal("dia repetido mudou a sequência")
	}
	for _, d := range []string{"2026-11-10", "2026-11-11", "2026-11-12", "2026-11-13"} {
		s.Step(dia(d), DayProtected)
	}
	if s.Current != 5 || s.Comebacks != 1 {
		t.Fatalf("volta por cima: %+v", s)
	}
}

func TestXPdoDia(t *testing.T) {
	p := Default()
	ev := DayXP(&p, dia("2026-11-13"), DayProtected, Streak{Current: 5})
	if len(ev) != 2 || ev[0].Points != 10 || ev[1].Kind != XPStreakBonus || ev[1].Points != 20 {
		t.Fatalf("%+v", ev)
	}
	if ev[0].Ref == ev[1].Ref && ev[0].Kind == ev[1].Kind {
		t.Fatal("chaves iguais")
	}
	if DayXP(&p, dia("2026-11-13"), DayPause, Streak{}) != nil {
		t.Fatal("pausa deu XP")
	}
	capped := CapDay(&p, 55, ev)
	if capped[0].Points != 5 || capped[1].Points != 0 {
		t.Fatalf("teto: %+v", capped)
	}
}

func TestConquistasENivel(t *testing.T) {
	p := Default()
	got := Earned(&p, Counters{MetricProtectedDays: 1, MetricStreak: 5}, map[string]bool{"primeiro-escudo": true})
	if len(got) != 1 || got[0].Code != "semana-blindada" {
		t.Fatalf("%+v", got)
	}
	if cur, next := p.LevelFor(0); cur != -1 || next == nil || next.MinDays != 1 {
		t.Fatal("sem nível")
	}
	if cur, next := p.LevelFor(15); p.Levels[cur].MinDays != 15 || next.MinDays != 30 {
		t.Fatal("ouro")
	}
	if cur, next := p.LevelFor(400); cur != len(p.Levels)-1 || next != nil {
		t.Fatal("lendário")
	}
}

func FuzzEvaluate(f *testing.F) {
	f.Add(3, 1, 2, 1, 0, true, false, 5)
	f.Fuzz(func(t *testing.T, pend, crit, out, uns, fix int, reboot, stale bool, invDays int) {
		p := Default()
		e := limpa()
		e.OSPending, e.OSCritical = max(pend, 0), max(crit, 0)
		e.TPOutdated, e.TPUnsupported, e.TPUserFixable = max(out, 0), max(uns, 0), max(fix, 0)
		e.Reboot, e.Stale = &reboot, stale
		at := agora.Add(-time.Duration(invDays%1000) * time.Hour)
		e.InventoryAt = &at
		r := Evaluate(&p, e, agora)
		if r.Score != nil && (*r.Score < 0 || *r.Score > 100) {
			t.Fatalf("fora de 0..100: %v", *r.Score)
		}
		if r.Display == 100 && !r.Sealed {
			t.Fatal("100 sem selo")
		}
		if r.Sealed && (e.OSCritical > 0 || reboot || stale) {
			t.Fatal("selo com bloqueio")
		}
	})
}

// O detalhe diz quais programas estão desatualizados ou fora de suporte (a pessoa precisa
// saber o que falta), com no máximo maxAppNames nomes.
func TestTerceirosComNomes(t *testing.T) {
	p := Default()
	e := limpa()
	e.TPOutdated = 2
	e.TPOutdatedApps = []string{"Google Chrome 158.0 → 159.0", "7-Zip 24.08 → 26.00"}
	r := Evaluate(&p, e, agora)
	if got := status(r, CritThirdParty); got.Detail != "2 programa(s) desatualizado(s): Google Chrome 158.0 → 159.0, 7-Zip 24.08 → 26.00" {
		t.Fatalf("detalhe: %q", got.Detail)
	}
	e.TPUnsupported, e.TPUnsupportedApps = 1, []string{"Java 7"}
	if got := status(Evaluate(&p, e, agora), CritThirdParty); got.Detail != "2 programa(s) desatualizado(s): Google Chrome 158.0 → 159.0, 7-Zip 24.08 → 26.00" {
		t.Fatalf("fora de suporte não entra no detalhe: %q", got.Detail)
	}
	many := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	if got := appList(many); got != ": a, b, c, d, e, f e mais 2" {
		t.Fatalf("muitos: %q", got)
	}
}
