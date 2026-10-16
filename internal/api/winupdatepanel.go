package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/wuerr"
)

// Atualizações do Windows pelo painel (Aula 16.3). Na aba Pendências de uma máquina com
// Windows, as correções agrupadas por KB: quantas CVEs de cada uma estão aprovadas em Patch,
// quantas aguardam, e se a busca da própria máquina mostra o KB pendente na fonte da
// política. O botão cria o job windows_update (16.1) com os KBs aprovados que a busca mostra.
// Como no update do Linux, o formulário só diz QUANDO; os KBs e os UpdateIDs vêm da
// avaliação e da busca que o servidor lê na hora.

var kbInFix = regexp.MustCompile(`KB(\d{4,8})`)

// Situação do KB na busca da máquina.
const (
	winPending     = "pendente"      // na fonte da política: instalável
	winNotInScan   = "fora-da-busca" // a busca não mostra (já instalado, aguardando reinício, outro número)
	winOfflineOnly = "so-offline"    // só o catálogo offline mostra: a fonte (WSUS) ainda não oferece
)

type winKBRow struct {
	KB, FixedIn                string
	Approved, Waiting, Exploit int
	State, Label, Class, Note  string
	WaitingHref                string
	CampaignHref               string // Aula 16.4: campanha por anel com este KB
}

type winUpdateCard struct {
	Rows        []winKBRow
	Problem     string // a fonte da política falhou na busca atual (Aula 16.2)
	ScanAt      string
	Installable []string // KBs que o botão instala
	Open        int64    // job windows_update em aberto
	CanInstall  bool
}

// winPlan agrupa as pendências por KB e confere cada uma na busca. Puro: o handler da
// página e o do botão chamam com o que leram do banco.
func winPlan(items []protocol.PendingItem, approvals []store.PatchApproval, rep protocol.PatchScanReport) winUpdateCard {
	var c winUpdateCard
	ok := map[string]bool{}
	for _, a := range approvals {
		ok[store.ApprovalKey(a.Item, a.FixedIn)] = true
	}
	byKB := map[string]*winKBRow{}
	var order []string
	for _, it := range items {
		for _, m := range kbInFix.FindAllStringSubmatch(it.FixedIn, -1) {
			kb := m[1]
			row := byKB[kb]
			if row == nil {
				row = &winKBRow{KB: kb, FixedIn: it.FixedIn, WaitingHref: (patchQuery{Status: "aguardando", Q: "KB" + kb}).href(),
					CampaignHref: "/painel/campanhas?" + url.Values{"tipo": {jobs.TypeWinUpdate}, "kb": {"KB" + kb}}.Encode() + "#nova"}
				byKB[kb] = row
				order = append(order, kb)
			}
			if ok[store.ApprovalKey(it.ID, it.FixedIn)] {
				row.Approved++
				if it.Exploited {
					row.Exploit++
				}
			} else {
				row.Waiting++
			}
		}
	}
	if len(order) == 0 {
		return c
	}
	c.Problem = jobs.PolicySourceProblem(rep)
	if c.Problem != "" {
		c.Problem = wuerr.Annotate(c.Problem)
	}
	for _, kb := range order {
		row := byKB[kb]
		_, notFound, offline := jobs.WinUpdatesForKBs(rep, []string{kb})
		switch {
		case c.Problem != "":
			row.State, row.Label, row.Class = winNotInScan, "sem busca válida", "desconhecido"
			row.Note = "a busca na fonte da política falhou; veja o aviso acima"
		case len(notFound) > 0:
			row.State, row.Label, row.Class = winNotInScan, "não aparece na busca", "em_dia"
			row.Note = "já instalada, aguardando reinício, ou a fonte oferece com outro número"
		case len(offline) > 0:
			row.State, row.Label, row.Class = winOfflineOnly, "só no catálogo offline", "reboot_pendente"
			row.Note = "a fonte da política (WSUS) ainda não oferece esta atualização: aprove-a no WSUS"
		default:
			row.State, row.Label, row.Class = winPending, "pendente na fonte", "faltando"
			if row.Approved == 0 {
				row.Note = "nenhuma CVE desta correção aprovada em Patch"
			} else {
				c.Installable = append(c.Installable, kb)
			}
		}
		c.Rows = append(c.Rows, *row)
	}
	return c
}

// winUpdateView monta o cartão da aba Pendências (nil: a máquina não tem busca do Windows).
func (a *api) winUpdateView(ctx context.Context, d protocol.ComplianceDetail, admin bool) (*winUpdateCard, error) {
	rep, found, err := a.store.LatestScan(ctx, d.MachineID)
	if err != nil || !found || !hasWUA(rep) {
		return nil, err
	}
	approvals, err := a.store.PatchApprovals(ctx)
	if err != nil {
		return nil, err
	}
	c := winPlan(d.Items, approvals, rep)
	c.ScanAt = a.stamp(rep.ScannedAt)
	if c.Open, err = a.openJob(ctx, d.MachineID, jobs.TypeWinUpdate); err != nil {
		return nil, err
	}
	c.CanInstall = admin && a.jobKey != nil && c.Open == 0 && c.Problem == "" && len(c.Installable) > 0
	return &c, nil
}

func hasWUA(rep protocol.PatchScanReport) bool {
	return slices.ContainsFunc(rep.Scans, func(s protocol.ScanResult) bool { return strings.HasPrefix(s.Source, "wua-") })
}

// winUpdateAction cria o job windows_update com os KBs aprovados que a busca mostra
// pendentes. Tudo é refeito aqui: a página pode ter ficado velha.
func (a *api) winUpdateAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	id := r.PathValue("id")
	if !machineIDPattern.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	back := machinePath(id)
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulário inválido", http.StatusBadRequest)
		return
	}
	when := r.PostFormValue("quando")
	switch {
	case a.jobKey == nil:
		http.Redirect(w, r, back+"?erro=sem-chave", http.StatusSeeOther)
		return
	case a.compliance == nil:
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	case when != "agora" && when != "janela":
		http.Redirect(w, r, back+"?erro=quando", http.StatusSeeOther)
		return
	}
	open, err := a.openJob(r.Context(), id, jobs.TypeWinUpdate)
	if err != nil {
		a.internalError(w, r, "procurar job em aberto", err)
		return
	}
	if open > 0 {
		http.Redirect(w, r, fmt.Sprintf("%s?erro=windows-aberto&job=%d", back, open), http.StatusSeeOther)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), complianceTimeout)
	defer cancel()
	d, found, err := a.compliance.Detail(ctx, id)
	if err != nil {
		a.internalError(w, r, "avaliar a máquina", err)
		return
	}
	if !found {
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	c, err := a.winUpdateView(ctx, d, true)
	if err != nil {
		a.internalError(w, r, "montar as atualizações do Windows", err)
		return
	}
	switch {
	case c == nil || len(c.Rows) == 0:
		http.Redirect(w, r, back+"?erro=windows-nada", http.StatusSeeOther)
		return
	case c.Problem != "":
		http.Redirect(w, r, back+"?erro=windows-fonte", http.StatusSeeOther)
		return
	case len(c.Installable) == 0:
		http.Redirect(w, r, back+"?erro=windows-nada", http.StatusSeeOther)
		return
	}
	rep, _, err := a.store.LatestScan(ctx, id)
	if err != nil {
		a.internalError(w, r, "ler a busca", err)
		return
	}
	params, _, _ := jobs.WinUpdatesForKBs(rep, c.Installable)
	if err := params.Validate(); err != nil {
		a.logger.Warn("windows_update pelo painel recusado", "machine_id", id, "error", err, "request_id", RequestID(r.Context()))
		http.Redirect(w, r, back+"?erro=windows-muitas", http.StatusSeeOther)
		return
	}
	raw, _ := json.Marshal(params)
	req := jobs.Request{MachineID: id, Type: jobs.TypeWinUpdate, TTL: panelUpdateTTL, By: s.Username, Params: raw}
	if when == "janela" {
		start, end, ok, err := a.nextWindow(r, id)
		if err != nil {
			a.internalError(w, r, "ler janela", err)
			return
		}
		if !ok {
			http.Redirect(w, r, back+"?erro=sem-janela", http.StatusSeeOther)
			return
		}
		req.NotBefore, req.TTL = start, end.Sub(later(a.now(), start))
	}
	rec, err := jobs.Issue(r.Context(), a.store, a.jobKey, req, a.now())
	if errors.Is(err, jobs.ErrMachineUnavailable) {
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	if err != nil {
		a.internalError(w, r, "criar job", err)
		return
	}
	kbs := strings.Join(params.Labels(), ", ")
	a.logger.Info("job criado pelo painel", "job_id", rec.ID, "machine_id", id, "type", rec.Type, "updates", kbs,
		"window", when == "janela", "username", s.Username, "request_id", RequestID(r.Context()))
	a.audit(r, s, "job.criar", id, fmt.Sprintf("job %d · %s · %s", rec.ID, typeLabel(rec.Type), kbs))
	http.Redirect(w, r, fmt.Sprintf("%s?criado=1&job=%d", back, rec.ID), http.StatusSeeOther)
}
