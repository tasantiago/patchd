package main

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tasantiago/patchd/internal/catalog/kev"
	"github.com/tasantiago/patchd/internal/compliance"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
)

// Avaliar o parque repetia, máquina a máquina, as mesmas perguntas ao catálogo: os produtos
// do Windows, as CVEs do produto, as versões de referência dos terceiros, a situação das
// fontes. Com 4.000 máquinas eram 4.000 cópias da mesma lista de CVEs, na consulta e na
// memória. O evalMemo guarda cada resposta uma vez por passada, e a avaliação do Windows uma
// vez por (produto, build): o parque tem dezenas de builds, não milhares.
//
// Tudo o que sai daqui é compartilhado entre as máquinas e entre as goroutines da avaliação:
// só leitura. Quem precisa ordenar ou mudar copia antes.

// memoTTL: quanto um memo vale sem o catálogo ter mudado. O memo novo nasce quando a chave do
// catálogo muda (uma sincronização); o prazo cobre o que não entra na chave (a aprovação de
// uma versão de terceiros), no mesmo espírito do evalTTL das avaliações guardadas.
const memoTTL = 5 * time.Minute

type memoEntry struct {
	done chan struct{}
	v    any
	err  error
}

type evalMemo struct {
	key string // a chave do catálogo (complianceService.catalogKey)
	at  time.Time

	mu      sync.Mutex
	entries map[string]*memoEntry
}

func newEvalMemo(key string, at time.Time) *evalMemo {
	return &evalMemo{key: key, at: at, entries: map[string]*memoEntry{}}
}

// do devolve o valor guardado em key, ou calcula uma vez só: quem chega durante o cálculo
// espera o mesmo resultado. Um erro não fica guardado (a próxima máquina tenta de novo).
func (m *evalMemo) do(ctx context.Context, key string, fn func() (any, error)) (any, error) {
	m.mu.Lock()
	if e, ok := m.entries[key]; ok {
		m.mu.Unlock()
		select {
		case <-e.done:
			return e.v, e.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	e := &memoEntry{done: make(chan struct{})}
	m.entries[key] = e
	m.mu.Unlock()

	e.v, e.err = fn()
	if e.err != nil {
		m.mu.Lock()
		delete(m.entries, key)
		m.mu.Unlock()
	}
	close(e.done)
	return e.v, e.err
}

func memoized[T any](ctx context.Context, m *evalMemo, key string, fn func() (T, error)) (T, error) {
	v, err := m.do(ctx, key, func() (any, error) { return fn() })
	if err != nil {
		var zero T
		return zero, err
	}
	return v.(T), nil
}

// memoStore é o banco da avaliação com as respostas do catálogo guardadas no memo. O que é
// da máquina (o inventário, os avisos do Linux para os pacotes dela) segue direto ao banco.
type memoStore struct {
	complianceStore
	m *evalMemo
}

func (s memoStore) CatalogStatus(ctx context.Context, sources []string) ([]store.CatalogSourceStatus, error) {
	return memoized(ctx, s.m, "status:"+strings.Join(sources, ","), func() ([]store.CatalogSourceStatus, error) {
		return s.complianceStore.CatalogStatus(ctx, sources)
	})
}

func (s memoStore) WindowsProducts(ctx context.Context) ([]compliance.MSRCProduct, error) {
	return memoized(ctx, s.m, "win-products", func() ([]compliance.MSRCProduct, error) { return s.complianceStore.WindowsProducts(ctx) })
}

func (s memoStore) WindowsFixes(ctx context.Context, productIDs []string) ([]compliance.WindowsFix, error) {
	return memoized(ctx, s.m, "win-fixes:"+strings.Join(productIDs, ","), func() ([]compliance.WindowsFix, error) {
		return s.complianceStore.WindowsFixes(ctx, productIDs)
	})
}

func (s memoStore) ThirdPartyVersions(ctx context.Context) ([]thirdparty.Ref, error) {
	return memoized(ctx, s.m, "third-versions", func() ([]thirdparty.Ref, error) { return s.complianceStore.ThirdPartyVersions(ctx) })
}

func (s memoStore) UbuntuEcosystemsKnown(ctx context.Context) ([]string, error) {
	return memoized(ctx, s.m, "ubuntu-ecos", func() ([]string, error) { return s.complianceStore.UbuntuEcosystemsKnown(ctx) })
}

func (s memoStore) FedoraReleasesKnown(ctx context.Context) ([]string, error) {
	return memoized(ctx, s.m, "fedora-releases", func() ([]string, error) { return s.complianceStore.FedoraReleasesKnown(ctx) })
}

func (s memoStore) KEVEntries(ctx context.Context) ([]kev.Vuln, error) {
	return memoized(ctx, s.m, "kev", func() ([]kev.Vuln, error) { return s.complianceStore.KEVEntries(ctx) })
}

func (s memoStore) MacOSMajors(ctx context.Context) ([]compliance.MacOSMajor, error) {
	return memoized(ctx, s.m, "mac-majors", func() ([]compliance.MacOSMajor, error) { return s.complianceStore.MacOSMajors(ctx) })
}

func (s memoStore) MacOSFixes(ctx context.Context, major int) ([]compliance.MacOSFix, error) {
	return memoized(ctx, s.m, "mac-fixes:"+strconv.Itoa(major), func() ([]compliance.MacOSFix, error) {
		return s.complianceStore.MacOSFixes(ctx, major)
	})
}

// windowsResult: a avaliação de um build contra as CVEs de um produto, feita uma vez. O
// resultado é o mesmo para toda máquina com esse produto e esse build (EvaluateWindows só
// olha os dois), então todas apontam para a mesma lista de CVEs pendentes. nil: o catálogo
// não tem CVEs para o produto.
func (s memoStore) windowsResult(ctx context.Context, productIDs []string, build string,
	compute func() (*compliance.WindowsResult, error)) (*compliance.WindowsResult, error) {
	return memoized(ctx, s.m, "win-result:"+strings.Join(productIDs, ",")+"|"+build, compute)
}

// windowsMemo é o que o evaluateWindows procura no banco para reaproveitar a avaliação.
type windowsMemo interface {
	windowsResult(ctx context.Context, productIDs []string, build string,
		compute func() (*compliance.WindowsResult, error)) (*compliance.WindowsResult, error)
}

// memoFor devolve o memo desta passada: o mesmo enquanto o catálogo não muda e o prazo não
// vence; senão, um novo (o anterior fica para quem ainda o usa e some com o coletor).
func (s *complianceService) memoFor(catalog string, now time.Time) *evalMemo {
	s.memoMu.Lock()
	defer s.memoMu.Unlock()
	if s.memo == nil || s.memo.key != catalog || now.Sub(s.memo.at) > memoTTL || now.Before(s.memo.at) {
		s.memo = newEvalMemo(catalog, now)
	}
	return s.memo
}
