package api

import (
	"cmp"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/pkgcache"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
	"github.com/tasantiago/patchd/internal/version"
)

// Aprovação dos instaladores de terceiros no painel (Aula 14.5): na página do programa,
// o cartão "Instaladores guardados" mostra cada versão que o servidor baixou e conferiu,
// com a aprovação. Aprovar é por programa e versão, e só o admin aprova.

type tpInstallInfo struct{ Winget, Type, Signer, Processes string }

type tpPackageRow struct{ Type, Arch, Size, SHA, Fetched string }

type tpVersionRow struct {
	Version                      string
	Packages                     []tpPackageRow
	Approved                     bool
	ApprovedBy, ApprovedAt, Note string
	CanApprove, CanRevoke        bool
	CampaignHref                 string // aprovada, admin e chave de jobs: o formulário da campanha já preenchido (Aula 14.8)
}

// installRule: a regra do Windows com install, se houver.
func (a *api) installRule(app string) *thirdparty.App {
	if a.tpManifest == nil {
		return nil
	}
	for _, r := range a.tpManifest.For("windows") {
		if r.ID == app && r.Install != nil {
			return &r
		}
	}
	return nil
}

// fillInstallers monta o cartão de instaladores da página do programa.
func (a *api) fillInstallers(r *http.Request, v *tpProgramView) error {
	rule := a.installRule(v.P.AppID)
	if rule == nil {
		return nil
	}
	v.Install = &tpInstallInfo{Winget: rule.Install.Winget, Type: rule.Install.Type, Signer: rule.Install.Signer,
		Processes: strings.Join(rule.Install.Processes, ", ")}
	v.MaxNote = maxNoteLen
	pkgs, err := a.store.ThirdPartyPackages(r.Context())
	if err != nil {
		return err
	}
	approvals, err := a.store.ThirdPartyApprovals(r.Context())
	if err != nil {
		return err
	}
	approved := map[string]store.ThirdPartyApproval{}
	for _, ap := range approvals {
		approved[store.TPApprovalKey(ap.AppID, ap.Version)] = ap
	}
	byVersion := map[string]*tpVersionRow{}
	for _, p := range pkgs {
		if p.AppID != v.P.AppID {
			continue
		}
		row := byVersion[p.Version]
		if row == nil {
			row = &tpVersionRow{Version: p.Version}
			if ap, ok := approved[store.TPApprovalKey(p.AppID, p.Version)]; ok {
				row.Approved, row.ApprovedBy, row.ApprovedAt, row.Note = true, ap.ApprovedBy, a.stamp(ap.ApprovedAt), ap.Note
			}
			row.CanApprove, row.CanRevoke = v.Admin && !row.Approved, v.Admin && row.Approved
			if row.Approved && v.Admin && a.jobKey != nil {
				row.CampaignHref = "/painel/campanhas?" + url.Values{"tipo": {"thirdparty"}, "programa": {p.AppID + " " + p.Version}}.Encode() + "#nova"
			}
			byVersion[p.Version] = row
		}
		row.Packages = append(row.Packages, tpPackageRow{Type: p.Type, Arch: p.Architecture, Size: pkgcache.Bytes(p.Size),
			SHA: p.SHA256[:12], Fetched: a.stamp(p.FetchedAt)})
	}
	for _, row := range byVersion {
		v.Versions = append(v.Versions, *row)
	}
	// A versão mais nova primeiro.
	slices.SortFunc(v.Versions, func(x, y tpVersionRow) int {
		nx, _ := thirdparty.Normalize(x.Version)
		ny, _ := thirdparty.Normalize(y.Version)
		if c, err := version.CompareDotted(ny, nx); err == nil && c != 0 {
			return c
		}
		return cmp.Compare(y.Version, x.Version)
	})
	return nil
}

// approveThirdPartyAction aprova uma versão guardada de um programa com install.
func (a *api) approveThirdPartyAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	app, ver := strings.TrimSpace(r.PostFormValue("app")), strings.TrimSpace(r.PostFormValue("version"))
	back := tpProgramPath(app)
	if a.installRule(app) == nil || ver == "" || len(ver) > 64 {
		http.Redirect(w, r, "/painel/patch?aba=terceiros", http.StatusSeeOther)
		return
	}
	note := strings.TrimSpace(r.PostFormValue("nota"))
	if utf8.RuneCountInString(note) > maxNoteLen {
		http.Redirect(w, r, back+"&erro=nota", http.StatusSeeOther)
		return
	}
	pkgs, err := a.store.ThirdPartyPackages(r.Context())
	if err != nil {
		a.internalError(w, r, "listar instaladores", err)
		return
	}
	if !slices.ContainsFunc(pkgs, func(p store.ThirdPartyPackage) bool { return p.AppID == app && p.Version == ver }) {
		http.Redirect(w, r, back+"&erro=nao-guardado", http.StatusSeeOther)
		return
	}
	created, err := a.store.ApproveThirdParty(r.Context(), store.ThirdPartyApproval{AppID: app, Version: ver, ApprovedBy: s.Username, Note: note})
	if err != nil {
		a.internalError(w, r, "aprovar o instalador", err)
		return
	}
	if !created {
		http.Redirect(w, r, back+"&erro=ja-aprovado", http.StatusSeeOther)
		return
	}
	a.logger.Info("instalador de terceiros aprovado", "app", app, "version", ver, "username", s.Username, "request_id", RequestID(r.Context()))
	http.Redirect(w, r, back+"&aprovado=1", http.StatusSeeOther)
}

// revokeThirdPartyAction revoga a aprovação; jobs já criados seguem o prazo deles.
func (a *api) revokeThirdPartyAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	app, ver := strings.TrimSpace(r.PostFormValue("app")), strings.TrimSpace(r.PostFormValue("version"))
	if app == "" || ver == "" || len(app) > 64 || len(ver) > 64 {
		http.Redirect(w, r, "/painel/patch?aba=terceiros", http.StatusSeeOther)
		return
	}
	removed, err := a.store.RevokeThirdParty(r.Context(), app, ver)
	if err != nil {
		a.internalError(w, r, "revogar a aprovação", err)
		return
	}
	if removed {
		a.logger.Info("aprovação de instalador de terceiros revogada", "app", app, "version", ver, "username", s.Username,
			"request_id", RequestID(r.Context()))
	}
	http.Redirect(w, r, tpProgramPath(app)+"&revogado=1", http.StatusSeeOther)
}

// tpApprovalFlash: as mensagens da página do programa depois de aprovar ou revogar.
func tpApprovalFlash(q url.Values) (flash, errMsg string) {
	switch {
	case q.Has("aprovado"):
		return "Versão aprovada: o patchd pode instalá-la nas máquinas onde o plano diz atualizar.", ""
	case q.Has("revogado"):
		return "Aprovação revogada. Jobs já criados seguem o prazo deles (cancele os pendentes em Jobs).", ""
	case q.Get("erro") == "ja-aprovado":
		return "", "Esta versão já estava aprovada; a aprovação original foi mantida."
	case q.Get("erro") == "nao-guardado":
		return "", "Esta versão não tem instalador guardado no servidor; nada foi aprovado."
	case q.Get("erro") == "nota":
		return "", fmt.Sprintf("A observação tem no máximo %d caracteres.", maxNoteLen)
	}
	return "", ""
}
