package api

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/tpfetch"
)

// A fonte do instalador de cada programa (store.InstallerSource): o winget, o padrão, ou o
// site do fabricante (o link com {versao}, que o Baixar e o download automático usam com a
// versão de referência), e o "baixar sozinho cada versão nova".

// tpSourceView: o cartão da página do programa.
type tpSourceView struct {
	Vendor                    bool // o site do fabricante
	URL, Type, Args, Checksum string
	Auto                      bool
	Next                      string // o link da versão de referência (o próximo Baixar)
	Types                     []string
	UpdatedBy, UpdatedAt      string
	Winget                    string
	CanEdit                   bool
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

// fillSource monta o cartão da fonte (programas do winget; o instalador próprio não tem).
func (a *api) fillSource(r *http.Request, v *tpProgramView, winget string) error {
	src, saved, err := a.installerSource(r, v.P.AppID)
	if err != nil {
		return err
	}
	sv := &tpSourceView{Vendor: src.Kind == store.SourceURL, URL: src.URLTemplate, Type: src.Type, Args: src.Arguments,
		Checksum: src.ChecksumURL, Auto: src.AutoFetch, Types: vendorTypes, Winget: winget, CanEdit: v.Admin}
	if saved {
		sv.UpdatedBy, sv.UpdatedAt = src.UpdatedBy, a.stamp(src.UpdatedAt)
	}
	if sv.Vendor && v.P.Reference != "" {
		sv.Next = tpfetch.ExpandVendor(src.URLTemplate, v.P.Reference)
	}
	if sv.Vendor {
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
	if r.PostFormValue("fonte") == store.SourceURL {
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
	if src.Kind == store.SourceWinget && !src.AutoFetch {
		_, err = a.store.DeleteInstallerSource(r.Context(), app) // o padrão: sem linha
	} else {
		err = a.store.SaveInstallerSource(r.Context(), src)
		if src.Kind == store.SourceURL {
			detail = fmt.Sprintf("site do fabricante %s (%s)", src.URLTemplate, src.Type)
			if src.ChecksumURL != "" {
				detail += ", SHA-256 de " + src.ChecksumURL
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
