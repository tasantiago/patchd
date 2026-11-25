package api

import (
	"context"
	"slices"
	"strings"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/selfservice"
)

// Atualizações do Windows pela Central de software: o usuário instala, quando quiser, o que
// a TI já aprovou. É o mesmo plano do botão "Instalar" da aba Pendências da máquina
// (winPlan): só os KBs com CVE aprovada em Patch e pendentes na busca da própria máquina, na
// fonte da política (WSUS ou Windows Update), com o cache do servidor se o parâmetro incluir a
// máquina. O job nunca reinicia; o reinício continua sendo o da aba Reinício.

// SelfServiceWindowsKey liga e desliga a seção (Configuração › Parâmetros › Autoatendimento).
const SelfServiceWindowsKey = "PATCHD_SELFSERVICE_WINDOWS"

// selfServiceWindowsOn: o parâmetro (sem o serviço de parâmetros, ligado).
func (a *api) selfServiceWindowsOn() bool {
	if a.params == nil {
		return true
	}
	return a.params.Value(SelfServiceWindowsKey) != "desligado"
}

// selfServiceWindows monta a seção Windows da Central e o job (nil: desligado, sem Windows,
// servidor, ou sem busca do Windows Update).
func (a *api) selfServiceWindows(ctx context.Context, id string) (*selfservice.WindowsPlan, error) {
	if !a.selfServiceWindowsOn() || a.compliance == nil {
		return nil, nil
	}
	inv, found, err := a.store.LatestInventory(ctx, id)
	if err != nil || !found || inv.OS.ID != "windows" || strings.Contains(inv.OS.Name, "Server") || strings.HasPrefix(inv.OS.Edition, "Server") {
		return nil, err // em servidor, a atualização fica com a TI
	}
	rep, found, err := a.store.LatestScan(ctx, id)
	if err != nil || !found || !hasWUA(rep) {
		return nil, err
	}
	d, found, err := a.compliance.Detail(ctx, id)
	if err != nil || !found {
		return nil, err
	}
	approvals, err := a.store.PatchApprovals(ctx)
	if err != nil {
		return nil, err
	}
	c := winPlan(d.Items, approvals, rep)
	at := rep.ScannedAt
	plan := &selfservice.WindowsPlan{State: protocol.SelfServiceWindowsState{Updates: []protocol.SelfServiceWinUpdate{}, Problem: c.Problem, ScanAt: &at}}
	if c.Problem != "" {
		return plan, nil
	}
	exploited := map[string]bool{}
	for _, row := range c.Rows {
		exploited[row.KB] = row.Exploit > 0
		if row.State == winPending && row.Approved == 0 {
			plan.State.Waiting++
		}
	}
	kbs := c.Installable
	if len(kbs) > jobs.MaxWinUpdates {
		kbs = kbs[:jobs.MaxWinUpdates]
	}
	if len(kbs) == 0 {
		return plan, nil
	}
	plan.Params, _, _ = jobs.WinUpdatesForKBs(rep, kbs)
	if len(plan.Params.Updates) > jobs.MaxWinUpdates {
		plan.Params.Updates = plan.Params.Updates[:jobs.MaxWinUpdates]
	}
	if plan.Params.Cache, err = a.winCacheFor(ctx, id); err != nil {
		return nil, err
	}
	for _, kb := range kbs {
		u := protocol.SelfServiceWinUpdate{KB: kb, Exploited: exploited[kb]}
		for _, sc := range rep.Scans {
			if !strings.HasPrefix(sc.Source, "wua-") {
				continue
			}
			for _, m := range sc.Missing {
				if slices.Contains(m.KBs, kb) {
					if u.Title == "" {
						u.Title = m.Title
					}
					u.Restart = u.Restart || m.RestartRequired
				}
			}
		}
		plan.State.Updates = append(plan.State.Updates, u)
	}
	return plan, nil
}
