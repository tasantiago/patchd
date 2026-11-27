package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/authsig"
	"github.com/tasantiago/patchd/internal/catalog/retry"
	"github.com/tasantiago/patchd/internal/choco"
	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/pkgcache"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
	"github.com/tasantiago/patchd/internal/winget"
)

// customAppStore: onde ficam os programas incluídos pelo painel (migração 0074).
type customAppStore interface {
	CustomApps(ctx context.Context) ([]store.CustomApp, error)
	// TryOpenSettings: o "tentar com o programa aberto" escolhido no painel (migração 0081).
	TryOpenSettings(ctx context.Context) (map[string]bool, error)
}

// thirdPartyRules: o manifesto dos programas de terceiros como o servidor o usa. base é o
// embutido mais o local (PATCHD_THIRDPARTY_MANIFEST); all soma os programas incluídos pelo
// painel. Um programa do banco que não passa na conferência fica de fora, com aviso no log.
func thirdPartyRules(ctx context.Context, look config.Lookup, st customAppStore, logger *slog.Logger) (base, all thirdparty.Manifest, err error) {
	path, _ := look("PATCHD_THIRDPARTY_MANIFEST")
	if base, err = thirdparty.Load(path); err != nil {
		return base, base, err
	}
	if st == nil {
		return base, base, nil
	}
	list, err := st.CustomApps(ctx)
	if err != nil {
		return base, base, err
	}
	all, errs := withCustomApps(base, list)
	for _, e := range errs {
		if logger != nil {
			logger.Warn("programa de terceiros incluído pelo painel fora do manifesto", "error", e)
		}
	}
	tryOpen, err := st.TryOpenSettings(ctx)
	if err != nil {
		return base, base, err
	}
	return base, thirdparty.WithTryOpen(all, tryOpen), nil
}

// withCustomApps: base com os programas do banco.
func withCustomApps(base thirdparty.Manifest, list []store.CustomApp) (thirdparty.Manifest, []error) {
	rules := make([]json.RawMessage, 0, len(list))
	for _, a := range list {
		rules = append(rules, a.Rule)
	}
	return thirdparty.WithCustom(base, rules)
}

// wingetDescriber: a busca do formulário "Incluir programa", no winget-pkgs público (o mesmo
// cliente do package fetch).
func wingetDescriber(version string) api.WingetDescriber {
	ua := "patchd-server/" + version
	c := winget.NewClient(ua)
	c.HTTP = retry.Wrap(c.HTTP, nil)
	return func(ctx context.Context, id string) (winget.Description, error) {
		d, err := c.Describe(ctx, id)
		if err != nil || d.InstallerURL == "" {
			return d, err
		}
		d.SignerOrg, d.SignerSubject, d.SignerNote = signerOf(ctx, d.InstallerURL, ua)
		return d, nil
	}
}

// chocoDescriber: a busca "Do Chocolatey" do "Incluir programa": o pacote mais novo do
// repositório da comunidade, lido sem executar o script, e quem assina o instalador do link.
func chocoDescriber(version string) api.ChocoDescriber {
	ua := "patchd-server/" + version
	hc := retry.Wrap(&http.Client{Timeout: time.Minute, CheckRedirect: pkgcache.HTTPSOnly}, nil)
	return func(ctx context.Context, id string) (api.ChocoDescription, error) {
		p, err := choco.Fetch(ctx, hc, choco.BaseURL, id, "", ua)
		if err != nil {
			return api.ChocoDescription{}, err
		}
		d, err := choco.Describe(p)
		if err != nil {
			if len(p.Embedded) > 0 {
				return api.ChocoDescription{}, fmt.Errorf("o pacote %s %s traz o instalador dentro (%s): o patchd só usa o link do fabricante; inclua como instalador próprio",
					id, p.Version, strings.Join(p.Embedded, ", "))
			}
			return api.ChocoDescription{}, err
		}
		out := api.ChocoDescription{Description: d}
		out.SignerOrg, out.SignerSubject, out.SignerNote = signerOf(ctx, d.URL, ua)
		return out, nil
	}
}

// sigHTTP: a leitura da assinatura segue redirecionamentos http e https (o http do espelho não
// muda nada: é para preencher o formulário; quem confere é o agente).
var sigHTTP = &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
	if req.URL.Scheme != "https" && req.URL.Scheme != "http" {
		return fmt.Errorf("redirecionamento para %q", req.URL.Scheme)
	}
	if len(via) >= 10 {
		return errors.New("mais de 10 redirecionamentos")
	}
	return nil
}}

// signerOf: quem assinou o instalador do link, só pelos pedaços com a assinatura (Range), sem
// baixá-lo: a organização (O=) e o Subject, ou uma nota de por que não deu.
func signerOf(ctx context.Context, link, ua string) (org, subject, note string) {
	sctx, cancel := context.WithTimeout(ctx, signerTimeout)
	defer cancel()
	s, err := authsig.Remote(sctx, sigHTTP, link, ua)
	switch {
	case errors.Is(err, authsig.ErrUnsigned):
		return "", "", "o instalador não é assinado"
	case err != nil:
		return "", "", "não deu para ler a assinatura do instalador: " + err.Error()
	case s.Organization == "":
		return "", "", "o certificado que assinou não tem O= (" + s.Subject + ")"
	}
	return s.Organization, s.Subject, ""
}

// signerTimeout: a leitura da assinatura do instalador na busca do "Incluir programa".
const signerTimeout = 20 * time.Second

// customAppSync: a versão de referência do programa recém-incluído, sem esperar a próxima
// sincronização do catálogo (que pode estar a horas). Grava só a versão dele: o
// syncThirdParty com um manifesto de um programa só apagaria a de todos os outros.
func customAppSync(st thirdpartyStore, look config.Lookup, logger *slog.Logger) func(context.Context, thirdparty.App) {
	src := newCatalogSources(look).Third
	return func(ctx context.Context, app thirdparty.App) {
		if app.Source == nil || app.Source.Type == thirdparty.SourceUpload || app.Source.Type == thirdparty.SourceManual {
			return // o instalador próprio e o manual do fabricante: a referência vem do instalador guardado
		}
		ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		ver, err := src.Latest(ctx, *app.Source)
		if err == nil {
			err = st.SaveThirdPartyVersion(ctx, app.ID, app.OS, ver, app.Source.String())
		}
		if err != nil {
			logger.Warn("programa incluído: versão de referência não buscada (vem na próxima sincronização do catálogo)", "app", app.ID, "error", err)
			return
		}
		logger.Info("programa incluído: versão de referência buscada", "app", app.ID, "versao", ver)
	}
}
