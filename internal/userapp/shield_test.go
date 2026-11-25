package userapp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/localapi"
	"github.com/tasantiago/patchd/internal/protocol"
)

func vistaShield(sealed bool, display int) *protocol.ShieldView {
	score := float64(display)
	prazo := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	v := &protocol.ShieldView{Enabled: true, Brand: "Atende+ Shield", Mission: "Missão 100%", Slogan: "Sua máquina protegida. Nosso ambiente mais seguro.",
		Notify: protocol.ShieldNotify{Enabled: true, MinHours: 4, WorkStart: 8, WorkEnd: 18},
		Machine: &protocol.ShieldMachineView{Score: &score, Display: display, Sealed: sealed, Overall: "quase", OverallLabel: "quase lá",
			Blockers: []string{"Sem reinício pendente: aguardando você"}, EvaluatedAt: prazo.Add(-time.Hour),
			Criteria: []protocol.ShieldCriterionView{
				{Criterion: "so", Label: "Atualizações do sistema", Status: "conforme", StatusLabel: "conforme", Weight: 30, Obtained: 30, Mandatory: true},
				{Criterion: "reinicio", Label: "Sem reinício pendente", Status: "aguardando_usuario", StatusLabel: "aguardando você",
					Blame: "usuario", BlameLabel: "usuário", Detail: "uma atualização espera o reinício do computador", Deadline: &prazo,
					Weight: 15, Obtained: 3.75, Mandatory: true, Action: "reiniciar"},
			}},
		Player: &protocol.ShieldPlayerView{Streak: 3, Best: 9, Protected: 12, XP: 340, Level: "Prata — Sentinela", NextLevel: "Ouro — Guardião", NextLevelDays: 3,
			Badges: []protocol.ShieldBadgeView{{Code: "primeiro-escudo", Name: "Primeiro escudo", Description: "Primeiro dia."}},
			Locked: []protocol.ShieldBadgeView{{Code: "semana-blindada", Name: "Semana blindada", Description: "5 dias."}},
			Recent: []protocol.ShieldXPView{{Reason: "dia útil 100% protegido", Points: 10, Day: prazo}}},
		Missions: []protocol.ShieldMissionView{{ID: 1, Title: "Semana do reinício", Scope: "coletiva", Status: "ativa", Progress: "80% das máquinas", Percent: 80,
			StartsAt: prazo.AddDate(0, 0, -2), EndsAt: prazo.AddDate(0, 0, 5)}},
		Places: []protocol.ShieldPlaceView{
			{Kind: "predio", Title: "Prédios", Mine: "Edifício Sede", Pos: 2, Of: 7, Rows: []protocol.ShieldGroupView{{Pos: 1, Name: "Anexo", Machines: 40, Percent: 90}, {Pos: 2, Name: "Edifício Sede", Machines: 120, Percent: 70, Mine: true}}},
			{Kind: "andar", Title: "Andares do Edifício Sede", Mine: "2º andar", Pos: 1, Of: 3, Rows: []protocol.ShieldGroupView{{Pos: 1, Name: "2º andar", Machines: 30, Percent: 80, Mine: true}}},
			{Kind: "sala", Title: "Salas do Edifício Sede", Mine: "201", Pos: 4, Of: 9, Rows: []protocol.ShieldGroupView{{Pos: 1, Name: "101", Machines: 5, Percent: 100}, {Pos: 4, Name: "201", Machines: 6, Percent: 50, Mine: true}}},
		},
		Groups:  []protocol.ShieldGroupView{{Pos: 1, Name: "3 · Prédio Sede", Machines: 12, Percent: 75, Mine: true}},
		Notices: []protocol.ShieldNotice{{ID: "acao:reinicio:1", Title: "Sua proteção precisa de você", Text: "Reinicie."}, {ID: "missao:1", Title: "Missão", Text: "x"}},
	}
	if sealed {
		v.Machine.Blockers, v.Machine.Overall, v.Machine.OverallLabel = nil, "protegida", "100% protegida"
		v.Machine.Criteria = v.Machine.Criteria[:1]
	}
	return v
}

func appComShield(v *protocol.ShieldView) *App {
	c := &canal{loja: lojaDeTeste(0)}
	a := appDeTeste(c)
	base := a.Call
	a.Call = func(ctx context.Context, r localapi.Request) (localapi.Response, error) {
		if r.Op == localapi.OpShield {
			if v == nil {
				return localapi.Response{Code: localapi.CodeInvalid, Error: `operação desconhecida "shield"`}, nil
			}
			return localapi.Response{OK: true, Account: `EXEMPLO\teste`, Shield: v}, nil
		}
		return base(ctx, r)
	}
	return a
}

func TestAbaMinhaProtecao(t *testing.T) {
	a := appComShield(vistaShield(false, 93))
	b := get(a, a.base()+"?aba=protecao").Body.String()
	for _, want := range []string{
		"Minha Proteção", `aria-label="proteção 93%"`, "93%", "quase lá", "Escolher a hora do reinício", "?aba=reinicio",
		"aguardando você", "responsável: usuário", "3,75 de 15 pontos", "Prata — Sentinela", "faltam 3 dia(s) para Ouro — Guardião",
		"<meter", "<progress", "Semana do reinício", "Primeiro escudo", "Semana blindada", "+10 XP", "Sua máquina protegida.", "1º 3 · Prédio Sede · seu grupo", "75% no selo",
		"Ranking: Prédios", "<b>Edifício Sede</b> está em <b>2º</b> de 7", "Ranking: Andares do Edifício Sede", "<b>2º andar</b> está em <b>1º</b> de 3",
		"Ranking: Salas do Edifício Sede", "4º 201 · o seu", "ninguém vê o seu nome",
	} {
		if !strings.Contains(b, want) {
			t.Errorf("faltou %q", want)
		}
	}
	if strings.Contains(b, "Selo 100%") || strings.Contains(b, " style=") || strings.Contains(b, "<script") {
		t.Error("selo sem estar selado, ou estilo/script inline")
	}
	// O anel: o trecho preenchido proporcional ao número mostrado.
	if !strings.Contains(b, `stroke-dasharray="303.9 326.7"`) {
		t.Errorf("anel: %s", b[strings.Index(b, "stroke-dasharray"):][:40])
	}
	// O menu de qualquer aba mostra a Minha Proteção com o alerta da ação pendente.
	b = get(a, a.base()).Body.String()
	if !strings.Contains(b, "?aba=protecao") || !strings.Contains(b, "ação sua pendente") {
		t.Error("menu sem a aba")
	}
}

func TestAbaMinhaProtecaoSelada(t *testing.T) {
	a := appComShield(vistaShield(true, 100))
	b := get(a, a.base()+"?aba=protecao").Body.String()
	if !strings.Contains(b, "Selo 100%") || strings.Contains(b, "O que você pode fazer agora") {
		t.Error("selado")
	}
}

func TestSemShieldSemAba(t *testing.T) {
	a := appComShield(nil) // agente antigo: operação desconhecida
	b := get(a, a.base()+"?aba=protecao").Body.String()
	if strings.Contains(b, "?aba=protecao") || !strings.Contains(b, "Atualizações para você") {
		t.Error("sem Shield, a aba não existe e a página cai nas atualizações")
	}
	off := vistaShield(false, 50)
	off.Enabled = false
	a = appComShield(off)
	if b := get(a, a.base()).Body.String(); strings.Contains(b, "?aba=protecao") {
		t.Error("Shield desligado no servidor e a aba apareceu")
	}
}

func TestAvisosDoShield(t *testing.T) {
	v := vistaShield(false, 80)
	var n shieldNotifier
	dez := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	if s := n.pick(v, dez.Add(-3*time.Hour)); s != nil {
		t.Fatal("aviso fora do expediente")
	}
	s := n.pick(v, dez)
	if s == nil || s.ID != "acao:reinicio:1" {
		t.Fatalf("primeiro aviso: %+v", s)
	}
	if s := n.pick(v, dez.Add(time.Hour)); s != nil {
		t.Fatal("dois avisos em menos de 4 horas")
	}
	s = n.pick(v, dez.Add(5*time.Hour))
	if s == nil || s.ID != "missao:1" {
		t.Fatalf("o próximo fato: %+v", s)
	}
	if s := n.pick(v, dez.Add(30*time.Hour)); s != nil {
		t.Fatal("o mesmo fato avisado duas vezes")
	}
	v.Notify.Enabled = false
	v.Notices = append(v.Notices, protocol.ShieldNotice{ID: "novo"})
	if s := n.pick(v, dez.Add(40*time.Hour)); s != nil {
		t.Fatal("avisos desligados")
	}
	if tip := shieldTip(vistaShield(false, 80)); !strings.Contains(tip, "proteção 80% (quase lá)") {
		t.Fatal(tip)
	}
	if tip := shieldTip(vistaShield(true, 100)); !strings.Contains(tip, "100% protegido") {
		t.Fatal(tip)
	}
	if got := (Notice{Shield: &protocol.ShieldNotice{Title: "T", Text: "x"}}).Tab(); got != tabShield {
		t.Fatal(got)
	}
	if ti, tx := noticeText(Notice{Shield: &protocol.ShieldNotice{Title: "T", Text: "x"}}); ti != "T" || tx != "x" {
		t.Fatal(ti, tx)
	}
}
