package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/store"
)

type mergeMachineRow struct {
	ID, Name, Serial, Agent, LastSeen string
	BIOS                              int
}
type mergeView struct {
	pageBase
	Target     mergeMachineRow
	Source     *mergeMachineRow
	Candidates []mergeMachineRow
}

func (a *api) mergeRow(ctx context.Context, id string) (mergeMachineRow, error) {
	row := mergeMachineRow{ID: id, Name: id}
	inv, ok, err := a.store.LatestInventory(ctx, id)
	if err != nil {
		return row, err
	}
	if ok {
		row.Name = inv.OS.Hostname
		if inv.Hardware != nil {
			row.Serial = inv.Hardware.SerialNumber
		}
		row.Agent = inv.AgentVersion
	}
	list, err := a.store.BIOSPasswords(ctx, id)
	row.BIOS = len(list)
	return row, err
}

func (a *api) mergePage(w http.ResponseWriter, r *http.Request, s panel.Session) {
	id := r.PathValue("id")
	members, err := a.store.MachineMergeMembers(r.Context(), id)
	if err != nil {
		a.internalError(w, r, "ler identidades da máquina", err)
		return
	}
	if !machineIDPattern.MatchString(id) || len(members) == 0 {
		http.NotFound(w, r)
		return
	}
	for _, m := range members {
		if m.SourceID == id && m.TargetID != id {
			http.Redirect(w, r, machinePath(m.TargetID)+"/mesclar", http.StatusSeeOther)
			return
		}
	}
	v := mergeView{pageBase: a.baseFor(r.Context(), s, "Mesclar registros de equipamento", "inventario")}
	v.Target, err = a.mergeRow(r.Context(), id)
	if err != nil {
		a.internalError(w, r, "ler equipamento", err)
		return
	}
	active, err := a.store.Machines(r.Context())
	if err != nil {
		a.internalError(w, r, "ler equipamentos", err)
		return
	}
	retired, err := a.store.RetiredMachines(r.Context())
	if err != nil {
		a.internalError(w, r, "ler registros arquivados", err)
		return
	}
	associations, err := a.store.MachineMergeMembers(r.Context(), "")
	if err != nil {
		a.internalError(w, r, "ler registros incorporados", err)
		return
	}
	incorporated := map[string]bool{}
	for _, link := range associations {
		incorporated[link.SourceID] = true
	}
	for _, m := range retired {
		active = append(active, m.MachineSummary)
	}
	for _, m := range active {
		if m.ID == id {
			v.Target.LastSeen = a.stamp(m.LastSeenAt)
			v.Target.Agent = m.AgentVersion
			continue
		}
		if incorporated[m.ID] {
			continue
		}
		row := mergeMachineRow{ID: m.ID, Name: m.Hostname, Agent: m.AgentVersion, LastSeen: a.stamp(m.LastSeenAt)}
		v.Candidates = append(v.Candidates, row)
		if m.ID == r.URL.Query().Get("origem") {
			src, e := a.mergeRow(r.Context(), m.ID)
			if e != nil {
				a.internalError(w, r, "ler origem", e)
				return
			}
			src.LastSeen = row.LastSeen
			src.Agent = row.Agent
			v.Source = &src
		}
	}
	if r.URL.Query().Has("erro") {
		v.Error = "A mesclagem não foi aplicada. Confira os registros e informe o motivo e a confirmação."
	}
	if r.URL.Query().Get("erro") == "ocupada" {
		v.Error = store.ErrMergeBusy.Error()
	}
	w.Header().Set("Cache-Control", "no-store")
	a.render(w, r, http.StatusOK, "mesclar-maquinas", v)
}

func (a *api) mergeAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	target := r.PathValue("id")
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulário inválido", http.StatusBadRequest)
		return
	}
	source := r.PostFormValue("origem")
	reason := strings.Join(strings.Fields(r.PostFormValue("motivo")), " ")
	back := machinePath(target) + "/mesclar?origem=" + url.QueryEscape(source)
	if !machineIDPattern.MatchString(source) || !machineIDPattern.MatchString(target) || source == target || len(reason) < 10 || len(reason) > 500 || r.PostFormValue("confirmo") != "sim" {
		http.Redirect(w, r, back+"&erro=validacao", http.StatusSeeOther)
		return
	}
	event := store.AuditEvent{At: a.now(), Username: s.Username, Source: s.Source, Remote: remoteIP(r), RequestID: RequestID(r.Context())}
	err := a.store.MergeMachines(r.Context(), source, target, reason, event)
	if errors.Is(err, store.ErrMergeBusy) {
		http.Redirect(w, r, back+"&erro=ocupada", http.StatusSeeOther)
		return
	}
	if errors.Is(err, store.ErrMergeInvalid) {
		http.Redirect(w, r, back+"&erro=validacao", http.StatusSeeOther)
		return
	}
	if err != nil {
		a.internalError(w, r, "mesclar registros", err)
		return
	}
	http.Redirect(w, r, machinePath(target)+"/historico?mesclada=1", http.StatusSeeOther)
}

type historyMember struct {
	ID, Name, When, By, Reason string
	Active                     bool
}
type historyReport struct {
	ID       int64
	At, Href string
}
type historyBIOS struct {
	ID                  int64
	Origin, State, When string
	CanReveal           bool
}
type machineHistoryView struct {
	pageBase
	ID, Selected       string
	Members            []historyMember
	Inventories, Scans []historyReport
	Jobs               []store.JobRow
	BIOS               []historyBIOS
	NextInv, NextScan  string
}

func (a *api) mergedHistoryPage(w http.ResponseWriter, r *http.Request, s panel.Session) {
	id := r.PathValue("id")
	group, err := a.store.MachineMergeMembers(r.Context(), id)
	if err != nil {
		a.internalError(w, r, "ler histórico", err)
		return
	}
	if len(group) == 0 {
		http.NotFound(w, r)
		return
	}
	root := ""
	for _, g := range group {
		if g.SourceID == g.TargetID {
			root = g.TargetID
		}
	}
	if root == "" {
		http.NotFound(w, r)
		return
	}
	if root != id {
		http.Redirect(w, r, machinePath(root)+"/historico", http.StatusSeeOther)
		return
	}
	v := machineHistoryView{pageBase: a.baseFor(r.Context(), s, "Histórico do equipamento", "inventario"), ID: id, Selected: r.URL.Query().Get("registro")}
	if v.Selected == "" {
		v.Selected = id
	}
	allowed := false
	for _, g := range group {
		inv, ok, err := a.store.LatestInventory(r.Context(), g.SourceID)
		if err != nil {
			a.internalError(w, r, "ler inventário histórico", err)
			return
		}
		name := g.SourceID
		if ok {
			name = inv.OS.Hostname
		}
		row := historyMember{ID: g.SourceID, Name: name, Active: g.SourceID == id, By: g.By, Reason: g.Reason}
		if !g.At.IsZero() {
			row.When = a.stamp(g.At)
		}
		v.Members = append(v.Members, row)
		if g.SourceID == v.Selected {
			allowed = true
		}
		guards, err := a.store.BIOSPasswords(r.Context(), g.SourceID)
		if err != nil {
			a.internalError(w, r, "ler guarda histórica", err)
			return
		}
		for _, b := range guards {
			v.BIOS = append(v.BIOS, historyBIOS{ID: b.ID, Origin: b.MachineID, State: biosStateLabel[b.State].label, When: a.stamp(b.CreatedAt),
				CanReveal: a.escrow != nil && b.EscrowKeyID == a.escrow.id && b.State != store.BIOSFailed})
		}
	}
	if !allowed {
		http.NotFound(w, r)
		return
	}
	for _, kind := range []string{"inventario", "buscas"} {
		before, _ := strconv.ParseInt(r.URL.Query().Get(kind+"_antes"), 10, 64)
		list, err := a.store.MachineReportHistory(r.Context(), v.Selected, kind, before)
		if err != nil {
			a.internalError(w, r, "ler relatórios históricos", err)
			return
		}
		if len(list) > 50 {
			next := machinePath(id) + "/historico?registro=" + url.QueryEscape(v.Selected) + "&" + kind + "_antes=" + strconv.FormatInt(list[49].ID, 10)
			if kind == "inventario" {
				v.NextInv = next
			} else {
				v.NextScan = next
			}
			list = list[:50]
		}
		for _, report := range list {
			row := historyReport{ID: report.ID, At: a.stamp(report.At), Href: fmt.Sprintf("%s/historico/%s/%d?registro=%s", machinePath(id), kind, report.ID, url.QueryEscape(v.Selected))}
			if kind == "inventario" {
				v.Inventories = append(v.Inventories, row)
			} else {
				v.Scans = append(v.Scans, row)
			}
		}
	}
	v.Jobs, err = a.store.Jobs(r.Context(), v.Selected, 100)
	if err != nil {
		a.internalError(w, r, "ler jobs históricos", err)
		return
	}
	if r.URL.Query().Has("mesclada") {
		v.Flash = "Registros mesclados. O equipamento escolhido continua ativo; históricos e senhas dos registros incorporados foram preservados."
	}
	w.Header().Set("Cache-Control", "no-store")
	a.render(w, r, http.StatusOK, "historico-maquina", v)
}

func (a *api) mergedHistoryReport(w http.ResponseWriter, r *http.Request, s panel.Session) {
	group, err := a.store.MachineMergeMembers(r.Context(), r.PathValue("id"))
	if err != nil {
		a.internalError(w, r, "ler histórico", err)
		return
	}
	origin := r.URL.Query().Get("registro")
	allowed := false
	for _, g := range group {
		if g.SourceID == origin {
			allowed = true
		}
	}
	n, parseErr := strconv.ParseInt(r.PathValue("report"), 10, 64)
	kind := r.PathValue("kind")
	if !allowed || parseErr != nil || n < 1 || (kind != "inventario" && kind != "buscas") {
		http.NotFound(w, r)
		return
	}
	data, err := a.store.MachineReportByID(r.Context(), origin, kind, n)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}
