package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/tasantiago/patchd/internal/api"
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
	c := winget.NewClient("patchd-server/" + version)
	c.HTTP = retry.Wrap(c.HTTP, nil)
	return c.Describe
}

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
