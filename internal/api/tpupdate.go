package api

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/pkgcache"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/tpinstall"
	"github.com/tasantiago/patchd/internal/version"
)

// Atualização de programas de terceiros pela página da máquina (Aula 14.6): o cartão
// "Atualização pelo patchd", na aba Pendências, mostra o plano da máquina (o mesmo do
// package plan) e, para o admin, um botão por programa que cria o job thirdparty (o mesmo
// do package install). O botão só aparece quando o servidor criaria o job: decisão
// ATUALIZAR, versão aprovada, instalador no cache e nenhuma instalação em aberto.

// panelThirdPartyTTL: o prazo do job pedido pelo painel, o mesmo padrão da linha de comando.
const panelThirdPartyTTL = 24 * time.Hour

type tpUpdateRow struct {
	App, Program, Href   string
	Installed            string // "8.9.7 (exe)"; duplicatas: as instalações
	Label, Class, Reason string
	Target               string // a versão do instalador (ATUALIZAR)
	Action, ActionHref   string // sem botão: o que falta
	CanInstall, Approved bool
}

type tpUpdateCard struct {
	Rows    []tpUpdateRow
	Updates int   // decisões ATUALIZAR
	Open    int64 // job thirdparty em aberto (só calculado para o admin com a chave)
	// Mac: o plano é o do Installomator (MacVersion: a versão do script).
	Mac        bool
	MacVersion string
}

// tpUpdateClass: a cor do estado da decisão.
func tpUpdateClass(state string) string {
	switch state {
	case tpinstall.StateUpdate:
		return "reboot_pendente"
	case tpinstall.StateCurrent:
		return "em_dia"
	}
	return "desconhecido"
}

// tpPlan: o plano da máquina pelo último inventário. ok é falso quando não se aplica:
// servidor sem as regras, máquina sem inventário do Windows ou de arquitetura desconhecida.
func (a *api) tpPlan(ctx context.Context, id string) (ds []tpinstall.Decision, approved map[string]bool, ok bool, err error) {
	if a.tpManifest == nil {
		return nil, nil, false, nil
	}
	inv, found, err := a.store.LatestInventory(ctx, id)
	if err != nil || !found || inv.OS.ID != "windows" && inv.OS.ID != "macos" {
		return nil, nil, false, err
	}
	if inv.OS.ID == "macos" {
		return a.tpPlanMac(ctx, inv)
	}
	arch, known := tpinstall.MachineArch(inv.OS.Arch)
	if !known {
		return nil, nil, false, nil
	}
	pkgs, err := a.store.ThirdPartyPackages(ctx)
	if err != nil {
		return nil, nil, false, err
	}
	approvals, err := a.store.ThirdPartyApprovals(ctx)
	if err != nil {
		return nil, nil, false, err
	}
	approved = map[string]bool{}
	for _, ap := range approvals {
		approved[store.TPApprovalKey(ap.AppID, ap.Version)] = true
	}
	return tpinstall.Plan(inv.Software, a.tpManifest.Get(), pkgs, arch), approved, true, nil
}

// tpPlanMac: o plano do Mac (Installomator), com a versão aprovada mais nova de cada programa
// como alvo.
func (a *api) tpPlanMac(ctx context.Context, inv protocol.InventoryReport) ([]tpinstall.Decision, map[string]bool, bool, error) {
	approvals, err := a.store.ThirdPartyApprovals(ctx)
	if err != nil {
		return nil, nil, false, err
	}
	approved, newest := map[string]bool{}, map[string]string{}
	for _, ap := range approvals {
		approved[store.TPApprovalKey(ap.AppID, ap.Version)] = true
		cur, ok := newest[ap.AppID]
		if n, err := version.CompareDotted(ap.Version, cur); !ok || err == nil && n > 0 {
			newest[ap.AppID] = ap.Version
		}
	}
	return tpinstall.PlanMac(inv.Software, a.tpManifest.Get(), func(app string) string { return newest[app] }), approved, true, nil
}

// macScript: o Installomator no cache, conferido; why diz o que falta.
func (a *api) macScript() (*tpinstall.MacScript, string) {
	return tpinstall.Campaigner{Dir: a.packagesDir(), Installomator: a.installomator}.MacScript()
}

// macReady: o que falta para o job do Installomator nesta máquina (vazio: nada).
func (a *api) macReady(ctx context.Context, id string) (string, error) {
	caps, err := a.store.MachineCapabilities(ctx, id)
	if err != nil {
		return "", err
	}
	if !slices.Contains(caps, protocol.CapabilityInstallomator) {
		return "o agente deste Mac é anterior ao Installomator: atualize o agente", nil
	}
	if _, why := a.macScript(); why != "" {
		return why, nil
	}
	return "", nil
}

// packagesDir: onde o servidor guarda os instaladores (PATCHD_CONTENT_DIR/pacotes).
func (a *api) packagesDir() string {
	if a.contentDir == "" {
		return ""
	}
	return filepath.Join(a.contentDir, "pacotes")
}

// installedLabel: a versão instalada, com o tipo de instalação.
func installedLabel(d tpinstall.Decision) string {
	var parts []string
	for _, e := range d.Entries {
		parts = append(parts, fmt.Sprintf("%s (%s)", cmp.Or(e.Software.Version, "?"), e.Kind))
	}
	if len(parts) == 0 && len(d.User) > 0 {
		return fmt.Sprintf("%d por usuário", len(d.User))
	}
	return strings.Join(parts, ", ")
}

// tpUpdateView monta o cartão. v.Admin e v.JobsOn já estão preenchidos.
func (a *api) tpUpdateView(r *http.Request, id string, v *machineView) error {
	ds, approved, ok, err := a.tpPlan(r.Context(), id)
	if err != nil || !ok || len(ds) == 0 {
		return err
	}
	card := &tpUpdateCard{}
	if len(ds) > 0 && ds[0].Package == nil && ds[0].App.OS == "macos" {
		card.Mac = true
		if a.installomator != nil {
			card.MacVersion = a.installomator.Version
		}
	}
	if v.Admin && v.JobsOn {
		if card.Open, err = a.openJob(r.Context(), id, jobs.TypeThirdParty); err != nil {
			return err
		}
	}
	dir := a.packagesDir()
	for _, d := range ds {
		row := tpUpdateRow{App: d.App.ID, Program: d.App.Name, Href: tpProgramPath(d.App.ID), Installed: installedLabel(d),
			Label: strings.ToLower(tpinstall.Labels[d.State]), Class: tpUpdateClass(d.State), Reason: d.Reason}
		if d.State == tpinstall.StateUpdate {
			card.Updates++
			row.Target = d.TargetVersion()
			row.Approved = approved[store.TPApprovalKey(d.App.ID, row.Target)]
			if d.Package == nil { // Mac: o Installomator
				if row.Action, err = a.macUpdateAction(r.Context(), id, v, card.Open); err != nil {
					return err
				}
			} else {
				row.Action, row.ActionHref = a.tpUpdateAction(d, row.Approved, dir, v, card.Open)
			}
			row.CanInstall = row.Action == ""
		}
		card.Rows = append(card.Rows, row)
	}
	v.TPUpdate = card
	return nil
}

// macUpdateAction: o que falta para o botão do Installomator; vazio quando ele aparece.
func (a *api) macUpdateAction(ctx context.Context, id string, v *machineView, open int64) (string, error) {
	switch {
	case !v.Admin:
		return "aprovado; pedir exige o perfil admin", nil
	case !v.JobsOn:
		return "pedir exige a chave de jobs no servidor", nil
	}
	why, err := a.macReady(ctx, id)
	if err != nil || why != "" {
		return why, err
	}
	if open > 0 {
		return fmt.Sprintf("job %d em aberto: uma instalação por vez", open), nil
	}
	return "", nil
}

// tpUpdateAction: o que falta para o botão, na ordem em que o admin resolve; vazio quando
// o botão aparece. O arquivo é conferido pelo tamanho; o pedido confere o SHA-256.
func (a *api) tpUpdateAction(d tpinstall.Decision, approved bool, dir string, v *machineView, open int64) (string, string) {
	switch {
	case !approved:
		return "aguardando aprovação de " + d.Package.Version, tpProgramPath(d.App.ID)
	case !v.Admin:
		return "aprovado; pedir exige o perfil admin", ""
	case !v.JobsOn:
		return "pedir exige a chave de jobs no servidor", ""
	case dir == "":
		return "o servidor não guarda instaladores (PATCHD_CONTENT_DIR)", ""
	}
	if err := pkgcache.Present(dir, d.Package.SHA256, d.Package.MSI(), d.Package.Size); err != nil {
		return "falta o instalador no cache: package fetch -app " + d.App.ID, ""
	}
	if _, err := tpinstall.JobParams(d); err != nil {
		return "o instalador guardado não forma um job (veja package plan)", ""
	}
	if open > 0 {
		return fmt.Sprintf("job %d em aberto: uma instalação por vez", open), ""
	}
	return "", ""
}

// thirdPartyUpdateAction cria o job thirdparty de um programa para a máquina. Refaz o
// plano no servidor: o formulário só diz o programa; versão, arquivo e argumentos vêm do
// plano, e cada condição do botão é conferida de novo (o SHA-256 do arquivo inclusive).
func (a *api) thirdPartyUpdateAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	id := r.PathValue("id")
	if !machineIDPattern.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxLoginBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulário inválido", http.StatusBadRequest)
		return
	}
	app := r.PostFormValue("app")
	back := machinePath(id)
	refuse := func(code, why string, extra ...any) {
		a.logger.Warn("instalação de terceiros pelo painel recusada: "+why,
			append([]any{"machine_id", id, "app", app, "username", s.Username, "request_id", RequestID(r.Context())}, extra...)...)
		http.Redirect(w, r, back+"?erro="+code, http.StatusSeeOther)
	}
	if a.jobKey == nil {
		http.Redirect(w, r, back+"?erro=sem-chave", http.StatusSeeOther)
		return
	}
	ds, approved, ok, err := a.tpPlan(r.Context(), id)
	if err != nil {
		a.internalError(w, r, "montar o plano de terceiros", err)
		return
	}
	var d *tpinstall.Decision
	for i := range ds {
		if ds[i].App.ID == app {
			d = &ds[i]
		}
	}
	switch {
	case !ok || d == nil:
		refuse("terceiros-nada", "programa fora do plano da máquina")
		return
	case d.State != tpinstall.StateUpdate:
		refuse("terceiros-nada", "a decisão não é ATUALIZAR", "state", d.State)
		return
	case !approved[store.TPApprovalKey(d.App.ID, d.TargetVersion())]:
		refuse("terceiros-nao-aprovado", "versão sem aprovação", "version", d.TargetVersion())
		return
	}
	var p jobs.ThirdPartyParams
	if d.Package == nil { // Mac: o Installomator, com o agente e o script conferidos de novo
		why, err := a.macReady(r.Context(), id)
		if err != nil {
			a.internalError(w, r, "conferir o Installomator", err)
			return
		}
		script, _ := a.macScript()
		if why != "" || script == nil {
			refuse("terceiros-mac", "Installomator indisponível", "motivo", why)
			return
		}
		if p, err = tpinstall.MacJobParams(*d, *script); err != nil {
			refuse("terceiros-job", "parâmetros inválidos", "version", d.TargetVersion(), "error", err)
			return
		}
	} else {
		dir := a.packagesDir()
		if dir == "" {
			refuse("terceiros-sem-arquivo", "o servidor não guarda instaladores")
			return
		}
		if _, err := pkgcache.Check(dir, d.Package.SHA256, d.Package.MSI()); err != nil {
			refuse("terceiros-sem-arquivo", "instalador ausente ou diferente no cache", "version", d.Package.Version, "error", err)
			return
		}
		if p, err = tpinstall.JobParams(*d); err != nil {
			refuse("terceiros-job", "parâmetros inválidos", "version", d.Package.Version, "error", err)
			return
		}
	}
	open, err := a.openJob(r.Context(), id, jobs.TypeThirdParty)
	if err != nil {
		a.internalError(w, r, "procurar job em aberto", err)
		return
	}
	if open > 0 {
		http.Redirect(w, r, fmt.Sprintf("%s?erro=terceiros-aberto&job=%d", back, open), http.StatusSeeOther)
		return
	}
	raw, err := json.Marshal(p)
	if err != nil {
		a.internalError(w, r, "montar o job", err)
		return
	}
	rec, err := jobs.Issue(r.Context(), a.store, a.jobKey,
		jobs.Request{MachineID: id, Type: jobs.TypeThirdParty, TTL: panelThirdPartyTTL, By: s.Username, Params: raw}, a.now())
	if errors.Is(err, jobs.ErrMachineUnavailable) {
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	if err != nil {
		a.internalError(w, r, "criar job", err)
		return
	}
	a.logger.Info("job criado pelo painel", "job_id", rec.ID, "machine_id", id, "type", rec.Type, "app", p.App, "version", p.Version,
		"username", s.Username, "request_id", RequestID(r.Context()))
	a.audit(r, s, "job.criar", id, fmt.Sprintf("job %d · %s · %s %s", rec.ID, typeLabel(rec.Type), p.App, p.Version))
	http.Redirect(w, r, fmt.Sprintf("%s?criado=1&job=%d", back, rec.ID), http.StatusSeeOther)
}

// Os avisos do pedido, na página da máquina (jobFlash).
func tpUpdateFlash(code string, job int64) string {
	switch code {
	case "terceiros-nada":
		return "O plano desta máquina não pede atualização desse programa (um inventário novo pode ter mudado o plano); nada foi pedido."
	case "terceiros-nao-aprovado":
		return "A versão do instalador não está aprovada; nada foi pedido. Aprove na página do programa, em Patch."
	case "terceiros-sem-arquivo":
		return "O instalador não está no cache do servidor ou não confere com o SHA-256 guardado; nada foi pedido. Baixe de novo com package fetch."
	case "terceiros-mac":
		return "O Installomator não está pronto para este Mac (agente anterior a ele, ou o script fora do cache do servidor); nada foi pedido. Veja o motivo no cartão."
	case "terceiros-job":
		return "Os argumentos do instalador guardado não formam um job válido (veja o log do servidor); nada foi pedido."
	case "terceiros-aberto":
		if job > 0 {
			return fmt.Sprintf("Já existe uma instalação de programa de terceiros em aberto para esta máquina (job %d). O agente faz uma por vez: espere o resultado ou cancele o job.", job)
		}
	}
	return ""
}
