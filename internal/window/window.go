// Package window define as janelas de manutenção (RF-25, Aula 8.3): em que dias, a que
// horas e por quanto tempo um anel aceita jobs. A janela é escrita no horário local de um
// fuso (America/Porto_Velho, por exemplo) e calculada sempre nesse fuso, para que "12:00"
// continue sendo meio-dia mesmo num fuso com horário de verão.
package window

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Limites de uma janela.
const (
	MinDuration = 10 * time.Minute
	MaxDuration = 24 * time.Hour
)

// Days é o conjunto de dias da semana: bit 0 = segunda ... bit 6 = domingo.
type Days uint8

// AllDays: todos os dias.
const AllDays Days = 0x7f

var dayNames = [7]string{"seg", "ter", "qua", "qui", "sex", "sab", "dom"}

// bit devolve o bit do dia da semana do Go (Sunday = 0).
func bit(d time.Weekday) Days { return 1 << ((int(d) + 6) % 7) }

// Has diz se o dia está no conjunto.
func (d Days) Has(w time.Weekday) bool { return d&bit(w) != 0 }

// ParseDays lê "seg-sex", "sab,dom", "seg,qua-sex" ou "todos" (com ou sem acento).
func ParseDays(s string) (Days, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer("á", "a", "ç", "c").Replace(s)
	if s == "todos" {
		return AllDays, nil
	}
	index := func(n string) (int, error) {
		for i, d := range dayNames {
			if n == d {
				return i, nil
			}
		}
		return 0, fmt.Errorf("dia desconhecido %q (use seg, ter, qua, qui, sex, sab, dom)", n)
	}
	var out Days
	for _, part := range strings.Split(s, ",") {
		a, b, isRange := strings.Cut(strings.TrimSpace(part), "-")
		i, err := index(a)
		if err != nil {
			return 0, err
		}
		j := i
		if isRange {
			if j, err = index(b); err != nil {
				return 0, err
			}
			if j < i {
				return 0, fmt.Errorf("intervalo %q ao contrário (escreva de segunda para domingo, ex.: sex-dom ou sab,dom,seg)", part)
			}
		}
		for k := i; k <= j; k++ {
			out |= 1 << k
		}
	}
	if out == 0 {
		return 0, errors.New("nenhum dia")
	}
	return out, nil
}

// String escreve o conjunto do jeito mais curto: "todos", "seg-sex", "sab,dom".
func (d Days) String() string {
	if d&AllDays == AllDays {
		return "todos"
	}
	var parts []string
	for i := 0; i < 7; {
		if d&(1<<i) == 0 {
			i++
			continue
		}
		j := i
		for j+1 < 7 && d&(1<<(j+1)) != 0 {
			j++
		}
		switch {
		case j == i:
			parts = append(parts, dayNames[i])
		case j == i+1:
			parts = append(parts, dayNames[i], dayNames[j])
		default:
			parts = append(parts, dayNames[i]+"-"+dayNames[j])
		}
		i = j + 1
	}
	return strings.Join(parts, ",")
}

// Window é uma janela semanal: começa às Start (desde a meia-noite local) nos dias Days e
// dura Duration. Uma janela que passa da meia-noite pertence ao dia em que começa.
type Window struct {
	Days     Days
	Start    time.Duration
	Duration time.Duration
	Location *time.Location
}

// ParseStart lê "HH:MM".
func ParseStart(s string) (time.Duration, error) {
	h, m, ok := strings.Cut(strings.TrimSpace(s), ":")
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if !ok || err1 != nil || err2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 || len(m) != 2 {
		return 0, fmt.Errorf("hora de início %q inválida (use HH:MM, de 00:00 a 23:59)", s)
	}
	return time.Duration(hh)*time.Hour + time.Duration(mm)*time.Minute, nil
}

// Validate confere os limites.
func (w Window) Validate() error {
	switch {
	case w.Days&AllDays == 0:
		return errors.New("janela sem dias")
	case w.Start < 0 || w.Start >= 24*time.Hour || w.Start%time.Minute != 0:
		return errors.New("início da janela fora de 00:00 a 23:59")
	case w.Duration < MinDuration || w.Duration > MaxDuration || w.Duration%time.Minute != 0:
		return fmt.Errorf("duração da janela fora de %v a %v (em minutos inteiros)", MinDuration, MaxDuration)
	case w.Location == nil:
		return errors.New("janela sem fuso horário")
	}
	return nil
}

// String: "seg-sex 12:00-14:00 (America/Porto_Velho)".
func (w Window) String() string {
	end := (w.Start + w.Duration) % (24 * time.Hour)
	next := ""
	if w.Start+w.Duration >= 24*time.Hour {
		next = " do dia seguinte"
	}
	loc := "?"
	if w.Location != nil {
		loc = w.Location.String()
	}
	return fmt.Sprintf("%s %s-%s%s (%s)", w.Days, clock(w.Start), clock(end), next, loc)
}

func clock(d time.Duration) string {
	return fmt.Sprintf("%02d:%02d", int(d.Hours()), int(d.Minutes())%60)
}

// Next devolve a janela em curso ou a próxima que ainda tenha pelo menos minLeft pela
// frente, a partir de now. start pode ser anterior a now (janela já aberta).
func (w Window) Next(now time.Time, minLeft time.Duration) (start, end time.Time) {
	local := now.In(w.Location)
	// As datas do calendário são contadas em UTC: a meia-noite local pode não existir no
	// dia em que começa o horário de verão (o relógio pula de 00:00 para 01:00), e o Go
	// normaliza esse horário para o dia anterior. Começa um dia antes: uma janela que
	// passou da meia-noite ainda pode estar aberta.
	cal := time.Date(local.Year(), local.Month(), local.Day()-1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 9; i++ {
		d := cal.AddDate(0, 0, i)
		if !w.Days.Has(d.Weekday()) {
			continue
		}
		// time.Date com a hora local (e não meia-noite + Start) segue o relógio local num
		// dia de troca de horário.
		s := time.Date(d.Year(), d.Month(), d.Day(), int(w.Start.Hours()), int(w.Start.Minutes())%60, 0, 0, w.Location)
		e := s.Add(w.Duration)
		if e.Sub(now) >= minLeft && e.After(now) {
			return s, e
		}
	}
	return time.Time{}, time.Time{} // só sem dias (Validate recusa)
}
