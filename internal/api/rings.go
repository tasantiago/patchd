package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/compliance"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/window"
)

// Anéis e janelas pelo painel (Aula 11.1). Até aqui era só "patchd-server ring": agora o
// admin põe máquinas num anel (uma a uma no detalhe, ou várias de uma vez na Frota) e
// define ou remove a janela de manutenção de cada anel. O perfil leitura só vê.

// maxBatch: máquinas por pedido na Frota (a página mostra a frota inteira; 2000 cobre o
// tribunal com folga e limita o tamanho do formulário).
const maxBatch = 2000

// windowDurations: as durações oferecidas, em minutos (de 30 min a 24 h).
var windowDurations = []int{30, 60, 90, 120, 180, 240, 360, 480, 720, 1440}

var dayValues = []struct{ Value, Label string }{
	{"seg", "seg"}, {"ter", "ter"}, {"qua", "qua"}, {"qui", "qui"}, {"sex", "sex"}, {"sab", "sáb"}, {"dom", "dom"},
}

// WithTimezone define o fuso sugerido no formulário da janela (PATCHD_TIMEZONE).
func WithTimezone(tz string) Option {
	return func(a *api) { a.timezone = tz }
}

type ringRow struct {
	Ring                      int
	Label                     string
	Machines                  int
	Window, Next, By, Updated string
	HasWindow                 bool
}

type durationOption struct {
	Minutes int
	Label   string
}

type ringsView struct {
	pageBase
	Rows      []ringRow
	Total     int
	Rings     []int
	Days      []struct{ Value, Label string }
	Durations []durationOption
	Timezone  string
}

func ringLabel(r int) string {
	switch r {
	case store.PilotRing:
		return strconv.Itoa(r) + " (piloto)"
	case store.DefaultRing:
		return strconv.Itoa(r) + " (padrão)"
	}
	return strconv.Itoa(r)
}

func durationLabel(m int) string {
	if m < 60 {
		return fmt.Sprintf("%d min", m)
	}
	if m%60 == 0 {
		return fmt.Sprintf("%d h", m/60)
	}
	return fmt.Sprintf("%d h %d min", m/60, m%60)
}

func allRings() []int {
	out := make([]int, store.MaxRing+1)
	for i := range out {
		out[i] = i
	}
	return out
}

// ringsPage lista os anéis em uso (máquinas e janela) e, para o admin, o formulário da janela.
func (a *api) ringsPage(w http.ResponseWriter, r *http.Request) {
	v := ringsView{pageBase: a.base(r, "Anéis e janelas", "aneis"), Rings: allRings(), Days: dayValues, Timezone: a.timezone}
	for _, m := range windowDurations {
		v.Durations = append(v.Durations, durationOption{Minutes: m, Label: durationLabel(m)})
	}
	ringsFlash(r.URL.Query(), &v.pageBase)
	machines, err := a.store.Machines(r.Context())
	if err != nil {
		a.internalError(w, r, "listar máquinas", err)
		return
	}
	windows, err := a.store.Windows(r.Context())
	if err != nil {
		a.internalError(w, r, "listar janelas", err)
		return
	}
	count := map[int]int{}
	for _, m := range machines {
		count[m.Ring]++
	}
	v.Total = len(machines)
	now := a.now()
	for ring := 0; ring <= store.MaxRing; ring++ {
		rw, has := store.WindowFor(windows, ring)
		if count[ring] == 0 && !has {
			continue
		}
		row := ringRow{Ring: ring, Label: ringLabel(ring), Machines: count[ring], Window: "sem janela", Next: "—", By: "—", Updated: "—", HasWindow: has}
		if has {
			row.Window, row.By, row.Updated = rw.Window.String(), rw.UpdatedBy, ago(now.Sub(rw.UpdatedAt))
			s, e := rw.Window.Next(now, window.MinDuration)
			loc := rw.Window.Location
			if s.After(now) {
				row.Next = s.In(loc).Format("02/01 15:04")
			} else {
				row.Next = "aberta até " + e.In(loc).Format("15:04")
			}
		}
		v.Rows = append(v.Rows, row)
	}
	a.render(w, r, http.StatusOK, "aneis", v)
}

// ringsFlash: as mensagens das ações de anel, de uma lista fixa (o número vem da URL e é
// convertido, nunca repetido como texto).
func ringsFlash(q url.Values, b *pageBase) {
	n, _ := strconv.Atoi(q.Get("anel"))
	switch {
	case q.Has("janela"):
		b.Flash = fmt.Sprintf("Janela do anel %d gravada. Vale para os jobs criados daqui em diante; os já criados mantêm a janela deles.", n)
	case q.Has("removida"):
		b.Flash = fmt.Sprintf("Janela do anel %d removida. Jobs \"na janela\" não podem mais ser pedidos para este anel; os já criados não mudam.", n)
	case q.Get("erro") == "sem-janela":
		b.Error = fmt.Sprintf("O anel %d não tinha janela; nada mudou.", n)
	case q.Get("erro") == "dias":
		b.Error = "Marque ao menos um dia da semana."
	case q.Get("erro") == "inicio":
		b.Error = "Hora de início inválida (use HH:MM, de 00:00 a 23:59)."
	case q.Get("erro") == "duracao":
		b.Error = "Escolha uma duração da lista."
	case q.Get("erro") == "fuso":
		b.Error = "Fuso horário desconhecido (use um nome IANA, como America/Porto_Velho)."
	case q.Get("erro") == "anel":
		b.Error = fmt.Sprintf("Escolha um anel de 0 a %d.", store.MaxRing)
	}
}

// formRing lê o anel do formulário (0 a MaxRing).
func formRing(r *http.Request, field string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue(field)))
	if err != nil || n < 0 || n > store.MaxRing {
		return 0, false
	}
	return n, true
}

// setWindowAction grava a janela do anel a partir do formulário.
func (a *api) setWindowAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	_ = r.ParseForm()
	back := func(q string) { http.Redirect(w, r, "/painel/aneis?"+q, http.StatusSeeOther) }
	ring, ok := formRing(r, "anel")
	if !ok {
		back("erro=anel")
		return
	}
	days := r.PostForm["dia"]
	if len(days) == 0 || len(days) > 7 {
		back("erro=dias")
		return
	}
	for _, d := range days {
		if !validDay(d) {
			back("erro=dias")
			return
		}
	}
	d, err := window.ParseDays(strings.Join(days, ","))
	if err != nil {
		back("erro=dias")
		return
	}
	start, err := window.ParseStart(r.PostFormValue("inicio"))
	if err != nil {
		back("erro=inicio")
		return
	}
	mins, err := strconv.Atoi(r.PostFormValue("duracao"))
	if err != nil || !validDuration(mins) {
		back("erro=duracao")
		return
	}
	tz := strings.TrimSpace(r.PostFormValue("fuso"))
	loc, err := loadZone(tz)
	if err != nil {
		back("erro=fuso")
		return
	}
	win := window.Window{Days: d, Start: start, Duration: time.Duration(mins) * time.Minute, Location: loc}
	if err := a.store.SetWindow(r.Context(), ring, win, s.Username); err != nil {
		a.internalError(w, r, "gravar janela", err)
		return
	}
	a.logger.Info("janela do anel definida pelo painel", "ring", ring, "janela", win.String(),
		"username", s.Username, "request_id", RequestID(r.Context()))
	back(fmt.Sprintf("janela=1&anel=%d", ring))
}

func validDay(d string) bool {
	for _, x := range dayValues {
		if x.Value == d {
			return true
		}
	}
	return false
}

func validDuration(m int) bool {
	for _, x := range windowDurations {
		if x == m {
			return true
		}
	}
	return false
}

// loadZone aceita só nomes IANA com "/" (America/Porto_Velho) ou UTC: "Local" dependeria
// do relógio do container, e o nome vai para a janela gravada.
func loadZone(tz string) (*time.Location, error) {
	if tz != "UTC" && (!strings.Contains(tz, "/") || len(tz) > 64 || strings.ContainsAny(tz, " .\\")) {
		return nil, errors.New("fuso inválido")
	}
	return time.LoadLocation(tz)
}

// deleteWindowAction remove a janela do anel.
func (a *api) deleteWindowAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	ring, err := strconv.Atoi(r.PathValue("ring"))
	if err != nil || ring < 0 || ring > store.MaxRing {
		http.Redirect(w, r, "/painel/aneis?erro=anel", http.StatusSeeOther)
		return
	}
	ok, err := a.store.DeleteWindow(r.Context(), ring)
	if err != nil {
		a.internalError(w, r, "remover janela", err)
		return
	}
	if !ok {
		http.Redirect(w, r, fmt.Sprintf("/painel/aneis?erro=sem-janela&anel=%d", ring), http.StatusSeeOther)
		return
	}
	a.logger.Info("janela do anel removida pelo painel", "ring", ring, "username", s.Username, "request_id", RequestID(r.Context()))
	http.Redirect(w, r, fmt.Sprintf("/painel/aneis?removida=1&anel=%d", ring), http.StatusSeeOther)
}

// machineRingAction põe uma máquina num anel, pelo detalhe.
func (a *api) machineRingAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	id := r.PathValue("id")
	if !machineIDPattern.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	ring, ok := formRing(r, "anel")
	if !ok {
		http.Redirect(w, r, machinePath(id)+"?erro=anel", http.StatusSeeOther)
		return
	}
	changed, err := a.store.SetMachineRing(r.Context(), id, ring)
	if err != nil {
		a.internalError(w, r, "mudar anel", err)
		return
	}
	if !changed {
		http.NotFound(w, r) // não existe ou está aposentada
		return
	}
	a.logger.Info("anel da máquina alterado pelo painel", "machine_id", id, "ring", ring,
		"username", s.Username, "request_id", RequestID(r.Context()))
	http.Redirect(w, r, fmt.Sprintf("%s?anel-alterado=1&anel=%d", machinePath(id), ring), http.StatusSeeOther)
}

// fleetRingAction põe várias máquinas num anel, a partir da Frota.
func (a *api) fleetRingAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	_ = r.ParseForm()
	// Volta para a Frota com o mesmo filtro; o filtro só passa se for um estado conhecido.
	back := "/painel/"
	if f := compliance.State(r.PostFormValue("estado")); f != "" && f.Rank() < len(compliance.States) {
		back += "?estado=" + url.QueryEscape(string(f)) + "&"
	} else {
		back += "?"
	}
	ring, ok := formRing(r, "anel")
	if !ok {
		http.Redirect(w, r, back+"erro=anel", http.StatusSeeOther)
		return
	}
	ids := r.PostForm["maquina"]
	if len(ids) == 0 || len(ids) > maxBatch {
		http.Redirect(w, r, back+"erro=nenhuma", http.StatusSeeOther)
		return
	}
	moved, skipped := 0, 0
	for _, id := range ids {
		if !machineIDPattern.MatchString(id) {
			skipped++
			continue
		}
		changed, err := a.store.SetMachineRing(r.Context(), id, ring)
		if err != nil {
			a.internalError(w, r, "mudar anel", err)
			return
		}
		if changed {
			moved++
		} else {
			skipped++
		}
	}
	a.logger.Info("anel alterado pelo painel em lote", "ring", ring, "maquinas", moved, "ignoradas", skipped,
		"username", s.Username, "request_id", RequestID(r.Context()))
	http.Redirect(w, r, fmt.Sprintf("%smovidas=%d&ignoradas=%d&anel=%d", back, moved, skipped, ring), http.StatusSeeOther)
}

// fleetRingFlash: as mensagens do lote na Frota.
func fleetRingFlash(q url.Values, b *pageBase) {
	ring, _ := strconv.Atoi(q.Get("anel"))
	switch {
	case q.Has("movidas"):
		moved, _ := strconv.Atoi(q.Get("movidas"))
		skipped, _ := strconv.Atoi(q.Get("ignoradas"))
		b.Flash = fmt.Sprintf("%d máquina(s) no anel %d.", moved, ring)
		if skipped > 0 {
			b.Flash += fmt.Sprintf(" %d ignorada(s): aposentadas ou inexistentes.", skipped)
		}
	case q.Get("erro") == "nenhuma":
		b.Error = fmt.Sprintf("Marque de 1 a %d máquinas.", maxBatch)
	case q.Get("erro") == "anel":
		b.Error = fmt.Sprintf("Escolha um anel de 0 a %d.", store.MaxRing)
	}
}
