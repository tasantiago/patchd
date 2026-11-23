package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/tasantiago/patchd/internal/catalog/retry"
	"github.com/tasantiago/patchd/internal/choco"
	"github.com/tasantiago/patchd/internal/pkgcache"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
	"github.com/tasantiago/patchd/internal/tpfetch"
	"github.com/tasantiago/patchd/internal/winget"
)

// A fonte do instalador de cada programa (store.InstallerSource) no servidor: o Baixar do
// painel e o download automático da versão de referência, pelo winget ou pelo site do
// fabricante (docs/operacao.md, "Fonte do instalador").

// sourceStore: o que o download pela fonte lê e grava.
type sourceStore interface {
	tpfetch.Store
	InstallerSources(ctx context.Context) ([]store.InstallerSource, error)
	ThirdPartyVersions(ctx context.Context) ([]thirdparty.Ref, error)
	ThirdPartyPackages(ctx context.Context) ([]store.ThirdPartyPackage, error)
	RecordAudit(ctx context.Context, e store.AuditEvent) error
}

// sourceFetcher baixa o instalador da regra pela fonte gravada dela.
type sourceFetcher struct {
	st  sourceStore
	dir string
	wg  *winget.Client
	f   *pkgcache.Fetcher
	hc  *http.Client // o arquivo de SHA-256 do fabricante e o pacote do Chocolatey
	// choco: o repositório do Chocolatey (choco.BaseURL; outro nos testes).
	choco string
}

func newSourceFetcher(st sourceStore, dir, version string) *sourceFetcher {
	ua := "patchd-server/" + version
	c := winget.NewClient(ua)
	c.HTTP = retry.Wrap(c.HTTP, nil)
	return &sourceFetcher{st: st, dir: dir, wg: c, f: pkgcache.NewFetcher(ua),
		hc: &http.Client{Timeout: 2 * time.Minute, CheckRedirect: pkgcache.HTTPSOnly}, choco: choco.BaseURL}
}

func (s *sourceFetcher) source(ctx context.Context, app string) (store.InstallerSource, error) {
	list, err := s.st.InstallerSources(ctx)
	if err != nil {
		return store.InstallerSource{}, err
	}
	if i := slices.IndexFunc(list, func(x store.InstallerSource) bool { return x.AppID == app }); i >= 0 {
		return list[i], nil
	}
	return store.InstallerSource{AppID: app, Kind: store.SourceWinget}, nil
}

// reference: a versão de referência do programa no Windows ("" se ainda não buscada).
func (s *sourceFetcher) reference(ctx context.Context, app string) (string, error) {
	refs, err := s.st.ThirdPartyVersions(ctx)
	if err != nil {
		return "", err
	}
	for _, r := range refs {
		if r.App == app && r.OS == "windows" {
			return r.Version, nil
		}
	}
	return "", nil
}

// fetch: versão vazia, a mais nova (no winget, a mais nova dele; no fabricante, a de
// referência). types: os tipos do Baixar (só no winget; a fonte do fabricante tem o seu).
func (s *sourceFetcher) fetch(ctx context.Context, rule thirdparty.App, ver string, types []string, out io.Writer) bool {
	src, err := s.source(ctx, rule.ID)
	if err != nil {
		fmt.Fprintf(out, "%s (%s)\n  ERRO        ler a fonte do instalador: %v\n", rule.Name, rule.ID, err)
		return false
	}
	switch src.Kind {
	case store.SourceChocolatey:
		return tpfetch.Choco(ctx, s.hc, s.choco, s.f, s.st, rule, src, ver, s.dir, out)
	case store.SourceURL:
	default:
		return tpfetch.One(ctx, s.wg, s.f, s.st, rule, tpfetch.Options{Version: ver, Arch: "x64", Types: types}, s.dir, out)
	}
	if ver == "" {
		if ver, err = s.reference(ctx, rule.ID); err != nil {
			fmt.Fprintf(out, "%s (%s)\n  ERRO        ler a versão de referência: %v\n", rule.Name, rule.ID, err)
			return false
		}
	}
	return tpfetch.Vendor(ctx, s.hc, s.f, s.st, rule, src, ver, s.dir, out)
}

// autoFetch: a cada passada, os programas com "baixar sozinho" cuja versão de referência
// ainda não tem instalador guardado. Cada (programa, versão) é tentado no máximo a cada
// autoRetry: o winget pode demorar dias para ter a versão nova, e o fabricante, horas.
type autoFetch struct {
	s      *sourceFetcher
	rules  func() thirdparty.Manifest
	logger *slog.Logger
	mu     sync.Mutex
	tried  map[string]time.Time
	now    func() time.Time
}

const (
	autoEvery = time.Hour
	autoRetry = 6 * time.Hour
	autoFirst = 10 * time.Minute
)

func (a *autoFetch) run(ctx context.Context) {
	t := time.NewTimer(autoFirst)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		a.pass(ctx)
		t.Reset(autoEvery)
	}
}

// pass faz uma passada; devolve quantos downloads tentou.
func (a *autoFetch) pass(ctx context.Context) int {
	srcs, err := a.s.st.InstallerSources(ctx)
	if err != nil {
		a.logger.Warn("download automático: ler as fontes", "error", err)
		return 0
	}
	pkgs, err := a.s.st.ThirdPartyPackages(ctx)
	if err != nil {
		a.logger.Warn("download automático: ler os instaladores", "error", err)
		return 0
	}
	tried := 0
	for _, src := range srcs {
		if !src.AutoFetch {
			continue
		}
		var rule *thirdparty.App
		for _, r := range a.rules().For("windows") {
			if r.ID == src.AppID && r.Install != nil && !r.Uploaded() {
				rule = &r
				break
			}
		}
		if rule == nil {
			continue
		}
		ref, err := a.s.reference(ctx, rule.ID)
		if err != nil || ref == "" || hasVersion(pkgs, rule.ID, ref) {
			continue
		}
		key := rule.ID + "\x00" + ref
		a.mu.Lock()
		if a.tried == nil {
			a.tried = map[string]time.Time{}
		}
		last, seen := a.tried[key]
		if seen && a.now().Sub(last) < autoRetry {
			a.mu.Unlock()
			continue
		}
		a.tried[key] = a.now()
		a.mu.Unlock()
		tried++
		ver := ""
		if src.Kind == store.SourceURL {
			ver = ref
		}
		var out strings.Builder
		fctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
		ok := a.s.fetch(fctx, *rule, ver, nil, &out)
		cancel()
		via := map[string]string{store.SourceURL: "site do fabricante", store.SourceChocolatey: "Chocolatey"}[src.Kind]
		if via == "" {
			via = "winget"
		}
		detail := fmt.Sprintf("automático: versão de referência %s, pelo %s: %s", ref, via, map[bool]string{true: "baixado", false: "falhou"}[ok])
		a.logger.Info("download automático de instalador", "app", rule.ID, "version", ref, "fonte", via, "ok", ok, "andamento", lastLines(out.String(), 4))
		if err := a.s.st.RecordAudit(ctx, store.AuditEvent{At: a.now(), Username: "patchd", Source: "automático", Action: "terceiros.baixar",
			Target: rule.ID, Detail: detail}); err != nil && !errors.Is(err, context.Canceled) {
			a.logger.Warn("download automático: auditoria", "error", err)
		}
	}
	return tried
}

// hasVersion: o programa já tem instalador guardado da versão (comparada pela parte numérica).
func hasVersion(pkgs []store.ThirdPartyPackage, app, ver string) bool {
	want, ok := thirdparty.Normalize(ver)
	for _, p := range pkgs {
		if p.AppID != app {
			continue
		}
		if p.Version == ver {
			return true
		}
		if got, ok2 := thirdparty.Normalize(p.Version); ok && ok2 && got == want {
			return true
		}
	}
	return false
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}
