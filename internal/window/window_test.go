package window

import (
	"testing"
	"time"
)

func TestDias(t *testing.T) {
	for in, quer := range map[string]string{
		"seg-sex": "seg-sex", "SÁB,dom": "sab,dom", "seg,qua-sex": "seg,qua-sex", "todos": "todos",
		"seg-dom": "todos", "ter": "ter", "seg,ter": "seg,ter", "sex-dom": "sex-dom",
	} {
		d, err := ParseDays(in)
		if err != nil || d.String() != quer {
			t.Errorf("%q: %q %v (quer %q)", in, d.String(), err, quer)
		}
	}
	for _, ruim := range []string{"", "sex-seg", "segunda", "seg,,ter", "x-y"} {
		if _, err := ParseDays(ruim); err == nil {
			t.Errorf("%q aceito", ruim)
		}
	}
	if d, _ := ParseDays("sab,dom"); !d.Has(time.Saturday) || !d.Has(time.Sunday) || d.Has(time.Monday) {
		t.Error("Has")
	}
}

func TestInicio(t *testing.T) {
	if d, err := ParseStart("09:05"); err != nil || d != 9*time.Hour+5*time.Minute {
		t.Errorf("09:05: %v %v", d, err)
	}
	for _, ruim := range []string{"24:00", "9", "9:5", "12:60", "-1:00", "ab:cd"} {
		if _, err := ParseStart(ruim); err == nil {
			t.Errorf("%q aceito", ruim)
		}
	}
}

func TestProximaJanela(t *testing.T) {
	pvh, err := time.LoadLocation("America/Porto_Velho")
	if err != nil {
		t.Fatal(err)
	}
	semana, _ := ParseDays("seg-sex")
	almoco := Window{Days: semana, Start: 12 * time.Hour, Duration: 2 * time.Hour, Location: pvh}
	if err := almoco.Validate(); err != nil || almoco.String() != "seg-sex 12:00-14:00 (America/Porto_Velho)" {
		t.Fatalf("%v %s", err, almoco)
	}
	em := func(dia, hora string) time.Time {
		tm, err := time.ParseInLocation("2006-01-02 15:04", dia+" "+hora, pvh)
		if err != nil {
			t.Fatal(err)
		}
		return tm
	}
	for _, c := range []struct {
		agora     time.Time
		ini, fim  time.Time
		descricao string
	}{
		{em("2026-10-09", "10:00"), em("2026-10-09", "12:00"), em("2026-10-09", "14:00"), "sexta antes da janela"},
		{em("2026-10-09", "13:00"), em("2026-10-09", "12:00"), em("2026-10-09", "14:00"), "sexta dentro da janela"},
		{em("2026-10-09", "13:55"), em("2026-10-12", "12:00"), em("2026-10-12", "14:00"), "menos de 10 min: a próxima (segunda)"},
		{em("2026-10-10", "13:00"), em("2026-10-12", "12:00"), em("2026-10-12", "14:00"), "sábado"},
	} {
		i, f := almoco.Next(c.agora.UTC(), MinDuration)
		if !i.Equal(c.ini) || !f.Equal(c.fim) {
			t.Errorf("%s: %v a %v", c.descricao, i, f)
		}
	}

	// Janela que passa da meia-noite: sexta 22:00 por 6 h ainda está aberta no sábado às 02:00.
	sexta, _ := ParseDays("sex")
	noite := Window{Days: sexta, Start: 22 * time.Hour, Duration: 6 * time.Hour, Location: pvh}
	if noite.String() != "sex 22:00-04:00 do dia seguinte (America/Porto_Velho)" {
		t.Errorf("%s", noite)
	}
	if i, f := noite.Next(em("2026-10-10", "02:00"), MinDuration); !i.Equal(em("2026-10-09", "22:00")) || !f.Equal(em("2026-10-10", "04:00")) {
		t.Errorf("madrugada: %v %v", i, f)
	}
	// Só domingo, logo depois da janela: a próxima é daqui a quase 7 dias.
	dom, _ := ParseDays("dom")
	d := Window{Days: dom, Start: 8 * time.Hour, Duration: time.Hour, Location: pvh}
	if i, _ := d.Next(em("2026-10-11", "09:30"), MinDuration); !i.Equal(em("2026-10-18", "08:00")) {
		t.Errorf("domingo seguinte: %v", i)
	}

	// Horário de verão (São Paulo, 2018: o relógio pulou de 00:00 para 01:00 em 4/11): a
	// janela das 09:00 continua às 09:00 do relógio local.
	sp, _ := time.LoadLocation("America/Sao_Paulo")
	todos := Window{Days: AllDays, Start: 9 * time.Hour, Duration: time.Hour, Location: sp}
	i, _ := todos.Next(time.Date(2018, 11, 3, 20, 0, 0, 0, sp), MinDuration)
	if i.In(sp).Hour() != 9 || i.In(sp).Day() != 4 {
		t.Errorf("verão: %v", i)
	}
	i, _ = todos.Next(time.Date(2018, 11, 4, 10, 30, 0, 0, sp), MinDuration)
	if i.In(sp).Hour() != 9 || i.In(sp).Day() != 5 {
		t.Errorf("dia seguinte à troca: %v", i)
	}

	for _, ruim := range []Window{
		{Days: 0, Start: 0, Duration: time.Hour, Location: pvh},
		{Days: AllDays, Start: 24 * time.Hour, Duration: time.Hour, Location: pvh},
		{Days: AllDays, Start: 0, Duration: 5 * time.Minute, Location: pvh},
		{Days: AllDays, Start: 0, Duration: 25 * time.Hour, Location: pvh},
		{Days: AllDays, Start: 0, Duration: time.Hour},
	} {
		if ruim.Validate() == nil {
			t.Errorf("aceita: %+v", ruim)
		}
	}
}
