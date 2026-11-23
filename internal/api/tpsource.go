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
	CanEdit   bool
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
		Auto: src.AutoFetch, Types: vendorTypes, Winget: winget, ChocoRule: rule.FromChocolatey(), CanEdit: v.Admin}
	if saved {
		sv.UpdatedBy, sv.UpdatedAt = src.UpdatedBy, a.stamp(src.UpdatedAt)
	}
	if sv.Vendor && v.P.Reference != "" {
		sv.Next = tpfetch.ExpandVendor(src.URLTemplate, v.P.Reference)
	}
	if sv.Vendor || sv.Choco {
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
	src := store.InstallerSource{AppID: app, Kind: store.SourceWinget, AutoFetch: r.PostFormValue("auto") == "1", UpdatedBy: s.Username}
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
	case store.SourceURL:
		src.Kind = store.SourceURL
		src.URLTemplate = strings.TrimSpace(r.PostFormValue("link"))
		src.Type = r.PostFormValue("fonte_tipo")
		src.Arguments = strings.TrimSpace(r.PostFormValue("argumentos"))
		src.ChecksumURL = strings.TrimSpace(r.PostFormValue("somas"))
		if !slices.Contains(vendorTypes, src.Type) {
			src.Type = ""
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
