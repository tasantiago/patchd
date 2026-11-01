package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
"github.com/tasantiago/patchd/internal/bundle"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/standardization"
	"github.com/tasantiago/patchd/internal/store"
)

func (a *api) standardizationService(r *http.Request) *standardization.Service {
	return &standardization.Service{Store: a.store, Manifest: a.tpManifest, Key: a.jobKey, PackagesDir: a.packagesDir(), BundlesDir:a.bundlesDir(), Now: a.now,
		OnCreated: func(rec jobs.Record, app, version string) {
			a.logger.Info("padronização: instalação agendada", "machine_id", rec.MachineID, "job_id", rec.ID, "app", app, "version", version, "request_id", RequestID(r.Context()))
			a.auditAs(r, store.StandardizationAuthor, "agente", "padronizacao.instalacao", rec.MachineID, fmt.Sprintf("job %d · %s %s", rec.ID, app, version))
		}}
}

func (a *api) standardizationUser(w http.ResponseWriter, r *http.Request, id string) {
	var user protocol.SelfServiceUser
	if !decodeJSON(w, r, &user) {
		return
	}
	if err := user.Check(); err != nil {
		writeError(w, 400, "invalid_user", err.Error())
		return
	}
	plan, err := a.standardizationService(r).Reconcile(r.Context(), id, user)
	if a.canceled(r, err) {
		return
	}
	if err != nil {
		a.internalError(w, r, "avaliar padronização", err)
		return
	}
	writeJSON(w, 200, plan)
}

func (a *api) standardizationReport(w http.ResponseWriter, r *http.Request, id string) {
	var report protocol.StandardizationReport
	if !decodeJSON(w, r, &report) {
		return
	}
	if err := report.SelfServiceUser.Check(); err != nil {
		writeError(w, 400, "invalid_user", err.Error())
		return
	}
	if err := report.StandardizationResult.Check(); err != nil {
		writeError(w, 400, "invalid_report", err.Error())
		return
	}
	if err := a.store.ReportStandardization(r.Context(), id, report.Account, report.StandardizationResult, a.now()); err != nil {
 if !errors.Is(err,store.ErrStandardizationStale){a.internalError(w,r,"registrar relato de atalhos",err);return}
		writeError(w, 409, "stale_standardization", "Não foi possível registrar esta revisão; consulte novamente o plano.")
		a.logger.Warn("padronização: relato não registrado", "error", err, "machine_id", id, "request_id", RequestID(r.Context()))
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *api) standardizeCheckin(r *http.Request, id string) {
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	if _, err := a.standardizationService(r).Reconcile(ctx, id, protocol.SelfServiceUser{}); err != nil && !a.canceled(r, err) {
		a.logger.Error("padronização: avaliação no check-in falhou", "machine_id", id, "error", err, "request_id", RequestID(r.Context()))
	}
}

type standardAppOption struct {
	ID, Name        string
	Selected, Ready bool
}
type standardMachineOption struct {
	ID, Name, OS string
	Selected     bool
}
type standardShortcutRow struct {
	protocol.StandardShortcut
	Index int
}
type standardStateView struct {
	MachineID, Name, Account, Policies, When, Shortcuts, Reported string
	Apps                                                          []protocol.StandardAppState
	Warnings                                                      []string
}
type standardInstallerRow struct { store.StandardInstaller; Bundles []store.ScriptBundle; Index int }
type standardizationView struct {
 InstallerRows []standardInstallerRow
	pageBase
	Editor       store.StandardizationPolicy
	Policies     []store.StandardizationPolicy
	Apps         []standardAppOption
	Machines     []standardMachineOption
	ShortcutRows []standardShortcutRow
	States       []standardStateView
	ADText       string
	CatalogOff   bool
}

func (a *api) standardizationPage(w http.ResponseWriter, r *http.Request, s panel.Session) {
	v := standardizationView{pageBase: a.baseFor(r.Context(), s, "Padronização", "padronizacao")}
	policies, err := a.store.StandardizationPolicies(r.Context())
	if err != nil {
		a.internalError(w, r, "listar grupos de padronização", err)
		return
	}
	if id, err := strconv.ParseInt(r.URL.Query().Get("grupo"), 10, 64); err == nil && id > 0 {
		found := false
		for _, p := range policies {
			if p.ID == id {
				v.Editor = p
				found = true
				break
			}
		}
		if !found {
			http.NotFound(w, r)
			return
		}
	} else if r.URL.Query().Get("basico") == "1" {
		v.Editor.Name = "Padrão básico"
		v.Editor.AllMachines = true
	}
	if r.URL.Query().Get("salvo") == "1" {
		v.Flash = "Grupo salvo. Padrões ativos serão avaliados pelos agentes."
	}
	if r.URL.Query().Get("excluido") == "1" {
		v.Flash = "Grupo excluído. Aplicativos instalados permanecem no computador; atalhos gerenciados serão revistos na próxima sincronização."
	}
	a.renderStandardization(w, r, 200, &v)
}

func (a *api) renderStandardization(w http.ResponseWriter, r *http.Request, status int, v *standardizationView) {
	policies, err := a.store.StandardizationPolicies(r.Context())
	if err != nil {
		a.internalError(w, r, "listar grupos de padronização", err)
		return
	}
	v.Policies = policies
	machines, err := a.store.Machines(r.Context())
	if err != nil {
		a.internalError(w, r, "listar computadores", err)
		return
	}
	names := map[string]string{}
	for _, m := range machines {
		names[m.ID] = dash(m.Hostname)
		v.Machines = append(v.Machines, standardMachineOption{ID: m.ID, Name: dash(m.Hostname), OS: strings.TrimSpace(m.OSName + " " + m.OSVersion), Selected: slices.Contains(v.Editor.Machines, m.ID)})
	}
	pkgs, err := a.store.ThirdPartyPackages(r.Context())
	if err != nil {
		a.internalError(w, r, "listar instaladores", err)
		return
	}
	approvals, err := a.store.ThirdPartyApprovals(r.Context())
	if err != nil {
		a.internalError(w, r, "listar aprovações", err)
		return
	}
	ready := map[string]bool{}
	for _, p := range pkgs {
		for _, ap := range approvals {
			if ap.AppID == p.AppID && ap.Version == p.Version {
				ready[p.AppID] = true
				break
			}
		}
	}
	v.CatalogOff = a.tpManifest == nil
	if a.tpManifest != nil {
		for _, app := range a.tpManifest.For("windows") {
			if app.Install != nil {
				v.Apps = append(v.Apps, standardAppOption{ID: app.ID, Name: app.Name, Selected: slices.Contains(v.Editor.Apps, app.ID), Ready: ready[app.ID]})
			}
		}
	}
	bundles,err:=a.store.ScriptBundles(r.Context());if err!=nil{a.internalError(w,r,"listar instaladores por script",err);return}
 for _,i:=range v.Editor.Installers {v.InstallerRows=append(v.InstallerRows,standardInstallerRow{StandardInstaller:i,Bundles:bundles,Index:len(v.InstallerRows)+1})}
 for n:=min(len(v.InstallerRows)+3,protocol.MaxStandardizationApps);len(v.InstallerRows)<n;{v.InstallerRows=append(v.InstallerRows,standardInstallerRow{Bundles:bundles,Index:len(v.InstallerRows)+1})}
 v.ADText = strings.Join(v.Editor.ADGroups, "\n")
	for _, s := range v.Editor.Shortcuts {
		v.ShortcutRows = append(v.ShortcutRows, standardShortcutRow{StandardShortcut: s, Index: len(v.ShortcutRows) + 1})
	}
	n := min(len(v.ShortcutRows)+3, protocol.MaxStandardizationShortcuts)
	for len(v.ShortcutRows) < n {
		v.ShortcutRows = append(v.ShortcutRows, standardShortcutRow{StandardShortcut: protocol.StandardShortcut{Kind: "site"}, Index: len(v.ShortcutRows) + 1})
	}
	states, err := a.store.StandardizationStates(r.Context())
	if err != nil {
		a.internalError(w, r, "listar avaliações de padronização", err)
		return
	}
	for _, s := range states {
		row := standardStateView{MachineID: s.MachineID, Name: names[s.MachineID], Account: s.Account, Policies: strings.Join(s.Plan.Policies, ", "), When: a.stamp(s.EvaluatedAt), Apps: s.Plan.Apps, Warnings: s.Plan.Warnings, Shortcuts: "aguardando relato"}
		if row.Name == "" {
			row.Name = short(s.MachineID)
		}
		if row.Account == "" {
			row.Account = "computador"
			row.Shortcuts = "avaliados na sessão do usuário"
		} else if len(s.Plan.Shortcuts) == 0 {
			row.Shortcuts = "nenhum atalho desejado"
		}
		if s.ShortcutResult != nil && s.ShortcutsAt != nil {
			applied, conflicts, removed := 0, 0, 0
			for _, it := range s.ShortcutResult.Items {
				switch it.State {
				case "aplicado", "em_dia":
					applied++
				case "conflito":
					conflicts++
				case "removido":
					removed++
				}
			}
			row.Shortcuts = fmt.Sprintf("%d aplicados/em dia; %d conflitos; %d removidos", applied, conflicts, removed)
			row.Reported = a.stamp(*s.ShortcutsAt)
		}
		v.States = append(v.States, row)
	}
	a.render(w, r, status, "padronizacao", v)
}

func standardValues(values []string) []string {
	out := []string{}
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v != "" {
			out = append(out, v)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func (a *api) saveStandardization(w http.ResponseWriter, r *http.Request, s panel.Session) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulário inválido", 400)
		return
	}
	p := store.StandardizationPolicy{Name: strings.TrimSpace(r.PostFormValue("nome")), Description: strings.TrimSpace(r.PostFormValue("descricao")), Enabled: r.PostFormValue("ativo") == "1", AllMachines: r.PostFormValue("todos") == "1", Machines: standardValues(r.PostForm["maquina"]), Apps: standardValues(r.PostForm["aplicativo"]), UpdatedBy: s.Username}
	for key,dest:=range map[string]*int64{"id":&p.ID,"revisao":&p.Revision}{v:=r.PostFormValue(key);if v==""{v="0"};n,err:=strconv.ParseInt(v,10,64);if err!=nil||n<0{http.Error(w,"identificador ou revisão inválida",400);return};*dest=n}
	p.ADGroups = standardValues(strings.FieldsFunc(r.PostFormValue("grupos_ad"), func(c rune) bool { return c == ',' || c == '\n' || c == '\r' }))
	if p.AllMachines {
		p.Machines, p.ADGroups = nil, nil
	}
	cols := []string{"atalho_nome", "atalho_tipo", "atalho_destino", "atalho_icone", "atalho_os"}
	count := len(r.PostForm[cols[0]])
	for _, col := range cols {
		if len(r.PostForm[col]) != count || count > protocol.MaxStandardizationShortcuts {
			http.Error(w, "atalhos fora dos limites", 400)
			return
		}
	}
	for i := 0; i < count; i++ {
		name, target := strings.TrimSpace(r.PostForm[cols[0]][i]), strings.TrimSpace(r.PostForm[cols[2]][i])
		if name == "" && target == "" {
			continue
		}
		p.Shortcuts = append(p.Shortcuts, protocol.StandardShortcut{OS:r.PostForm[cols[4]][i],Name: name, Kind: r.PostForm[cols[1]][i], Target: target, Icon: r.PostForm[cols[3]][i]})
	}
	icols:=[]string{"instalador_pacote","instalador_os","instalador_software"};n:=len(r.PostForm[icols[0]])
 for _,col:=range icols {if len(r.PostForm[col])!=n||n>protocol.MaxStandardizationApps{http.Error(w,"instaladores fora dos limites",400);return}}
 for i:=0;i<n;i++ {
   raw,software:=r.PostForm[icols[0]][i],strings.TrimSpace(r.PostForm[icols[2]][i]);if raw==""&&software==""{continue}
   id,err:=strconv.ParseInt(raw,10,64);if err!=nil||id<=0{http.Error(w,"selecione um pacote de instalação",400);return}
   b,ok,err:=a.store.ScriptBundle(r.Context(),id);if err!=nil{a.internalError(w,r,"ler instalador por script",err);return};if !ok{http.Error(w,"pacote de instalação removido; selecione outro",400);return}
   params:=jobs.ScriptParams{Bundle:b.Name,SHA256:b.SHA256,Size:b.Size,Entry:b.Entry,Runner:bundle.RunnerFor(b.Entry),Args:b.Args,Timeout:b.ExecutionTimeout()}
   p.Installers=append(p.Installers,store.StandardInstaller{BundleID:id,OS:r.PostForm[icols[1]][i],Software:software,Params:params})
 }
 v := standardizationView{pageBase: a.baseFor(r.Context(), s, "Padronização", "padronizacao"), Editor: p}
	bad := func(err error) { v.Error = err.Error(); a.renderStandardization(w, r, 400, &v) }
	if err := p.Validate(); err != nil {
		bad(err)
		return
	}
	if len(p.Apps) > 0 {
		if a.tpManifest == nil {
			bad(errors.New("o catálogo de aplicativos ainda não está configurado"))
			return
		}
		for _, id := range p.Apps {
			found := false
			for _, rule := range a.tpManifest.For("windows") {
				if rule.ID == id && rule.Install != nil {
					found = true
					break
				}
			}
			if !found {
				bad(errors.New("um aplicativo selecionado não pertence ao catálogo de instalação"))
				return
			}
		}
	}
	ms, err := a.store.Machines(r.Context())
	if err != nil {
		a.internalError(w, r, "validar computadores do grupo", err)
		return
	}
	for _, id := range p.Machines {
		if !slices.ContainsFunc(ms, func(m protocol.MachineSummary) bool { return m.ID == id }) {
			bad(errors.New("um computador selecionado não está mais ativo na frota"))
			return
		}
	}
	id, err := a.store.SaveStandardizationPolicy(r.Context(), p)
	if errors.Is(err, store.ErrStandardizationConflict) {
		v.Error = err.Error()
		a.renderStandardization(w, r, 409, &v)
		return
	}
	if err != nil {
		a.internalError(w, r, "salvar grupo de padronização", err)
		return
	}
	a.audit(r, s, "padronizacao.salvar", strconv.FormatInt(id, 10), fmt.Sprintf("%s · ativo=%t · %d aplicativos · %d atalhos", p.Name, p.Enabled, len(p.Apps)+len(p.Installers), len(p.Shortcuts)))
	http.Redirect(w, r, fmt.Sprintf("/painel/padronizacao?grupo=%d&salvo=1", id), http.StatusSeeOther)
}

func (a *api) deleteStandardization(w http.ResponseWriter, r *http.Request, s panel.Session) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxLoginBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulário inválido", 400)
		return
	}
	rev, _ := strconv.ParseInt(r.PostFormValue("revisao"), 10, 64)
	err = a.store.DeleteStandardizationPolicy(r.Context(), id, rev)
	if errors.Is(err, store.ErrStandardizationConflict) {
		http.Error(w, "O grupo mudou; recarregue antes de excluir.", 409)
		return
	}
	if err != nil {
		a.internalError(w, r, "excluir grupo de padronização", err)
		return
	}
	a.audit(r, s, "padronizacao.excluir", strconv.FormatInt(id, 10), "grupo excluído")
	http.Redirect(w, r, "/painel/padronizacao?excluido=1", http.StatusSeeOther)
}
