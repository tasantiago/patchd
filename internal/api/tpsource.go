package api

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
	"github.com/tasantiago/patchd/internal/tpfetch"
)

// A fonte do instalador de cada programa (store.InstallerSource): o winget, o padrão, ou o
// site do fabricante (o link com {versao}, que o Baixar e o download automático usam com a
// versão de referência), e o "baixar sozinho cada versão nova".

// tpSourceView: o cartão da página do programa.
type tpSourceView struct {
	Vendor                    bool // o site do fabricante
	Choco                     bool // o pacote do Chocolatey
	Evergreen                 bool // o link que o Evergreen informa
	EgApp, EgFilter           string
	Package, URLVar           string
	ChecksumVar               string
	URL, Type, Args, Checksum string
	Auto                      bool
	Next                      string // o link da versão de referência (o próximo Baixar)
	Types                     []string
	UpdatedBy, UpdatedAt      string
	Winget                    string
	// ChocoRule: o programa foi incluído do Chocolatey (sem winget): o padrão é o pacote da
	// regra, e o winget não é opção.
	ChocoRule bool
	// VendorRule: o programa foi incluído do site do fabricante: o link é a única fonte.
	// Manual: sem fonte automática da versão (a do instalador mais novo baixado).
	VendorRule, Manual bool
	CanEdit            bool
}

// vendorTypes: os tipos que a fonte do fabricante aceita.
var vendorTypes = []string{"msi", "nullsoft", "inno", "wix", "burn", "exe"}

// installerSource: a fonte gravada do programa (zero: o winget, sem automático).
func (a *api) installerSource(r *http.Request, app string) (store.InstallerSource, bool, error) {
	list, err := a.store.InstallerSources(r.Context())
	if err != nil {
		return store.InstallerSource{}, false, err
	}
	i := slices.IndexFunc(list, func(s store.InstallerSource) bool { return s.AppID == app })
	if i < 0 {
		return store.InstallerSource{AppID: app, Kind: store.SourceWinget}, false, nil
	}
	return list[i], true, nil
}

// fillSource monta o cartão da fonte (programas do winget e do Chocolatey; o instalador
// próprio não tem).
func (a *api) fillSource(r *http.Request, v *tpProgramView, rule thirdparty.App) error {
	src, saved, err := a.installerSource(r, v.P.AppID)
	if err != nil {
		return err
	}
	src = tpfetch.RuleSource(rule, src)
	winget := rule.Install.Winget
	sv := &tpSourceView{Vendor: src.Kind == store.SourceURL, Choco: src.Kind == store.SourceChocolatey, URL: src.URLTemplate, Type: src.Type,
		Args: src.Arguments, Checksum: src.ChecksumURL, Package: src.PackageID, URLVar: src.URLVar, ChecksumVar: src.ChecksumVar,
		Auto: src.AutoFetch, Types: vendorTypes, Winget: winget, ChocoRule: rule.FromChocolatey(), CanEdit: v.Admin,
		VendorRule: rule.FromVendor(), Manual: rule.ManualVersion()}
	if saved {
		sv.UpdatedBy, sv.UpdatedAt = src.UpdatedBy, a.stamp(src.UpdatedAt)
	}
	// O Evergreen: o gravado; sem ele, o da regra (a fonte da versão ou a conferência), para
	// a opção já vir preenchida.
	sv.Evergreen = src.Kind == store.SourceEvergreen
	switch {
	case sv.Evergreen:
		sv.EgApp, sv.EgFilter = src.PackageID, src.Filter
	case rule.Source != nil && rule.Source.Type == thirdparty.SourceEvergreen:
		sv.EgApp, sv.EgFilter = rule.Source.ID, thirdparty.FormatEvergreenFilter(rule.Source.Filter)
	case rule.Check != nil && rule.Check.Type == thirdparty.SourceEvergreen:
		sv.EgApp, sv.EgFilter = rule.Check.ID, thirdparty.FormatEvergreenFilter(rule.Check.Filter)
	}
	if sv.Vendor && v.P.Reference != "" {
		sv.Next = tpfetch.ExpandVendor(src.URLTemplate, v.P.Reference)
	}
	if sv.Vendor || sv.Choco || sv.Evergreen {
		v.Kinds = nil // o tipo é o da fonte
	}
	v.Source = sv
	return nil
}

// sourceAction: POST /painel/patch/terceiros/fonte (app, fonte, link, tipo, argumentos,
// somas, auto).
func (a *api) sourceAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	app := strings.TrimSpace(r.PostFormValue("app"))
	rule := a.installRule(app)
	if rule == nil || rule.Uploaded() {
		http.Redirect(w, r, "/painel/patch?aba=terceiros", http.StatusSeeOther)
		return
	}
	back := tpProgramPath(app)
	src := store.InstallerSource{AppID: app, Kind: store.SourceWinget, AutoFetch: r.PostFormValue("auto") == "1" && !rule.ManualVersion(), UpdatedBy: s.Username}
	if f := r.PostFormValue("fonte"); rule.FromVendor() && f != store.SourceURL && f != store.SourceEvergreen {
		http.Redirect(w, r, back+"&erro=fonte&motivo="+url.QueryEscape("este programa foi incluído do site do fabricante: a fonte é o link do fabricante ou o do Evergreen")+"#fonte", http.StatusSeeOther)
		return
	}
	switch r.PostFormValue("fonte") {
	case store.SourceWinget:
		if rule.FromChocolatey() {
			http.Redirect(w, r, back+"&erro=fonte&motivo="+url.QueryEscape("este programa foi incluído do Chocolatey e não tem pacote do winget: escolha o Chocolatey ou o site do fabricante")+"#fonte", http.StatusSeeOther)
			return
		}
	case store.SourceChocolatey:
		src.Kind = store.SourceChocolatey
		src.PackageID = strings.TrimSpace(r.PostFormValue("pacote"))
		src.URLVar = strings.TrimSpace(r.PostFormValue("var_link"))
		src.ChecksumVar = strings.TrimSpace(r.PostFormValue("var_sha"))
		src.Type = r.PostFormValue("fonte_tipo")
		src.Arguments = strings.TrimSpace(r.PostFormValue("argumentos"))
		if !slices.Contains(vendorTypes, src.Type) {
			src.Type = ""
		}
		if err := tpfetch.CheckChoco(src, *rule); err != nil {
			http.Redirect(w, r, back+"&erro=fonte&motivo="+url.QueryEscape(err.Error())+"#fonte", http.StatusSeeOther)
			return
		}
	case store.SourceEvergreen:
		src.Kind = store.SourceEvergreen
		src.PackageID = strings.TrimSpace(r.PostFormValue("evergreen"))
		src.Type = r.PostFormValue("fonte_tipo")
		src.Arguments = strings.TrimSpace(r.PostFormValue("argumentos"))
		if !slices.Contains(vendorTypes, src.Type) {
			src.Type = ""
		}
		if rule.FromVendor() || rule.FromChocolatey() { // o tipo e os argumentos são os da regra
			src.Type, src.Arguments = rule.Install.Type, rule.Install.Arguments
		}
		filter, err := thirdparty.ParseEvergreenFilter(r.PostFormValue("evergreen_filtro"))
		if err == nil {
			src.Filter = thirdparty.FormatEvergreenFilter(filter)
			err = tpfetch.CheckEvergreen(src, *rule)
		}
		if err != nil {
			http.Redirect(w, r, back+"&erro=fonte&motivo="+url.QueryEscape(err.Error())+"#fonte", http.StatusSeeOther)
			return
		}
	case store.SourceURL:
		src.Kind = store.SourceURL
		src.URLTemplate = strings.TrimSpace(r.PostFormValue("link"))
		src.Type = r.PostFormValue("fonte_tipo")
		src.Arguments = strings.TrimSpace(r.PostFormValue("argumentos"))
		src.ChecksumURL = strings.TrimSpace(r.PostFormValue("somas"))
		if !slices.Contains(vendorTypes, src.Type) {
			src.Type = ""
		}
		if rule.FromVendor() { // o tipo e os argumentos são os da regra (em Editar)
			src.Type, src.Arguments = rule.Install.Type, rule.Install.Arguments
		}
		if err := tpfetch.CheckVendor(src, *rule); err != nil {
			http.Redirect(w, r, back+"&erro=fonte&motivo="+url.QueryEscape(err.Error())+"#fonte", http.StatusSeeOther)
			return
		}
	}
	var err error
	detail := "winget"
	if rule.FromChocolatey() && src.Kind == store.SourceChocolatey && src.PackageID == rule.Install.Chocolatey && !src.AutoFetch &&
		src.URLVar == "" && src.ChecksumVar == "" && src.Type == rule.Install.Type && src.Arguments == rule.Install.Arguments {
		_, err = a.store.DeleteInstallerSource(r.Context(), app) // o da regra: sem linha
	} else if src.Kind == store.SourceWinget && !src.AutoFetch {
		_, err = a.store.DeleteInstallerSource(r.Context(), app) // o padrão: sem linha
	} else {
		err = a.store.SaveInstallerSource(r.Context(), src)
		switch src.Kind {
		case store.SourceURL:
			detail = fmt.Sprintf("site do fabricante %s (%s)", src.URLTemplate, src.Type)
			if src.ChecksumURL != "" {
				detail += ", SHA-256 de " + src.ChecksumURL
			}
		case store.SourceEvergreen:
			detail = fmt.Sprintf("Evergreen %s (%s)", src.PackageID, src.Type)
			if src.Filter != "" {
				detail += ", filtro " + src.Filter
			}
		case store.SourceChocolatey:
			detail = fmt.Sprintf("Chocolatey %s (%s)", src.PackageID, src.Type)
			if src.URLVar != "" || src.ChecksumVar != "" {
				detail += fmt.Sprintf(", variáveis %s/%s", src.URLVar, src.ChecksumVar)
			}
		}
	}
	if err != nil {
		a.internalError(w, r, "guardar a fonte do instalador", err)
		return
	}
	if src.AutoFetch {
		detail += "; baixa sozinho a versão nova"
	}
	a.audit(r, s, "terceiros.fonte", app, detail)
	http.Redirect(w, r, back+"&fonte=1#fonte", http.StatusSeeOther)
}

// tryOpenAction: POST /painel/patch/terceiros/tentar-aberto (app, ligado). Liga ou desliga o
// "tentar com o programa aberto" do programa e troca o manifesto vivo: os próximos jobs já vão
// com a escolha.
func (a *api) tryOpenAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	app := strings.TrimSpace(r.PostFormValue("app"))
	rule := a.installRule(app)
	if rule == nil || a.tpManifest == nil {
		http.Redirect(w, r, "/painel/patch?aba=terceiros", http.StatusSeeOther)
		return
	}
	back := tpProgramPath(app)
	on := r.PostFormValue("ligado") == "1"
	if on && len(rule.Install.Processes) == 0 {
		http.Redirect(w, r, back+"&erro=tentar-aberto#instaladores", http.StatusSeeOther)
		return
	}
	if err := a.store.SaveTryOpen(r.Context(), store.TryOpenSetting{AppID: app, Enabled: on, UpdatedBy: s.Username}); err != nil {
		a.internalError(w, r, "guardar o tentar com o programa aberto", err)
		return
	}
	set, err := a.store.TryOpenSettings(r.Context())
	if err != nil {
		a.internalError(w, r, "ler o tentar com o programa aberto", err)
		return
	}
	a.tpManifest.Set(thirdparty.WithTryOpen(a.tpManifest.Get(), set))
	detail := "desligado: com o programa aberto, adia"
	if on {
		detail = "ligado: com o programa aberto, tenta instalar e só adia se não der certo"
	}
	a.audit(r, s, "terceiros.tentar_aberto", app, detail)
	http.Redirect(w, r, back+"&tentar-aberto="+map[bool]string{true: "1", false: "0"}[on]+"#instaladores", http.StatusSeeOther)
}
