package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/authsig"
	"github.com/tasantiago/patchd/internal/catalog/retry"
	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
	"github.com/tasantiago/patchd/internal/winget"
)

// customAppStore: onde ficam os programas incluídos pelo painel (migração 0074).
type customAppStore interface {
	CustomApps(ctx context.Context) ([]store.CustomApp, error)
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
	return base, all, nil
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
	// Quem assinou: só os pedaços do instalador com a assinatura (Range), sem baixá-lo. É para
	// preencher o formulário; quem confere é o agente (o http do espelho não muda isso).
	sigHTTP := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" && req.URL.Scheme != "http" {
			return fmt.Errorf("redirecionamento para %q", req.URL.Scheme)
		}
		if len(via) >= 10 {
			return errors.New("mais de 10 redirecionamentos")
		}
		return nil
	}}
	return func(ctx context.Context, id string) (winget.Description, error) {
		d, err := c.Describe(ctx, id)
		if err != nil || d.InstallerURL == "" {
			return d, err
		}
		sctx, cancel := context.WithTimeout(ctx, signerTimeout)
		defer cancel()
		s, err := authsig.Remote(sctx, sigHTTP, d.InstallerURL, ua)
		switch {
		case errors.Is(err, authsig.ErrUnsigned):
			d.SignerNote = "o instalador não é assinado"
		case err != nil:
			d.SignerNote = "não deu para ler a assinatura do instalador: " + err.Error()
		case s.Organization == "":
			d.SignerNote = "o certificado que assinou não tem O= (" + s.Subject + ")"
		default:
			d.SignerOrg, d.SignerSubject = s.Organization, s.Subject
		}
		return d, nil
	}
}

// signerTimeout: a leitura da assinatura do instalador na busca do "Incluir programa".
const signerTimeout = 20 * time.Second

// customAppSync: a versão de referência do programa recém-incluído, sem esperar a próxima
// sincronização do catálogo (que pode estar a horas). Grava só a versão dele: o
// syncThirdParty com um manifesto de um programa só apagaria a de todos os outros.
func customAppSync(st thirdpartyStore, look config.Lookup, logger *slog.Logger) func(context.Context, thirdparty.App) {
	src := newCatalogSources(look).Third
	return func(ctx context.Context, app thirdparty.App) {
		if app.Source == nil || app.Source.Type == thirdparty.SourceUpload {
			return // o instalador próprio: a referência vem do envio
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
