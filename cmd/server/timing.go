package main

import (
	"context"
	"fmt"
	"io"
	"sort"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/tasantiago/patchd/internal/catalog/kev"
	"github.com/tasantiago/patchd/internal/compliance"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
)

// Medir antes de otimizar (Aula 7.7): timedStore envolve o banco da avaliação e anota
// quanto tempo cada consulta levou. O resto do tempo da avaliação é Go (comparar versões).

type stepTimes struct {
	mu    sync.Mutex
	total map[string]time.Duration
	calls map[string]int
}

func newStepTimes() *stepTimes {
	return &stepTimes{total: map[string]time.Duration{}, calls: map[string]int{}}
}

// add anota uma chamada que começou em start. Uso: defer t.add("Consulta", time.Now()).
func (t *stepTimes) add(name string, start time.Time) {
	d := time.Since(start)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.total[name] += d
	t.calls[name]++
}

func (t *stepTimes) sum() time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	var s time.Duration
	for _, d := range t.total {
		s += d
	}
	return s
}

// print escreve as consultas da mais lenta para a mais rápida e o tempo fora do banco.
func (t *stepTimes) print(out io.Writer, elapsed time.Duration) {
	t.mu.Lock()
	names := make([]string, 0, len(t.total))
	for n := range t.total {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool { return t.total[names[i]] > t.total[names[j]] })
	t.mu.Unlock()

	fmt.Fprintln(out, "\nTempos (consultas ao banco, da mais lenta para a mais rápida):")
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "CONSULTA\tCHAMADAS\tTOTAL")
	for _, n := range names {
		fmt.Fprintf(tw, "%s\t%d\t%s\n", n, t.calls[n], ms(t.total[n]))
	}
	_ = tw.Flush()
	db := t.sum()
	fmt.Fprintf(out, "banco: %s · fora do banco (Go): %s · total: %s\n", ms(db), ms(elapsed-db), ms(elapsed))
}

func ms(d time.Duration) string {
	return fmt.Sprintf("%.1f ms", float64(d.Microseconds())/1000)
}

// timedStore cronometra cada método do banco da avaliação.
type timedStore struct {
	st fleetStore
	t  *stepTimes
}

func (x timedStore) Machines(ctx context.Context) ([]protocol.MachineSummary, error) {
	defer x.t.add("Machines", time.Now())
	return x.st.Machines(ctx)
}

func (x timedStore) ResolveMachine(ctx context.Context, prefix string) (string, bool, error) {
	defer x.t.add("ResolveMachine", time.Now())
	return x.st.ResolveMachine(ctx, prefix)
}

func (x timedStore) LatestInventory(ctx context.Context, id string) (protocol.InventoryReport, bool, error) {
	defer x.t.add("LatestInventory", time.Now())
	return x.st.LatestInventory(ctx, id)
}

func (x timedStore) UbuntuEcosystemsKnown(ctx context.Context) ([]string, error) {
	defer x.t.add("UbuntuEcosystemsKnown", time.Now())
	return x.st.UbuntuEcosystemsKnown(ctx)
}

func (x timedStore) UbuntuAdvisories(ctx context.Context, ecosystem string, sources []string) ([]compliance.Advisory, error) {
	defer x.t.add("UbuntuAdvisories", time.Now())
	return x.st.UbuntuAdvisories(ctx, ecosystem, sources)
}

func (x timedStore) FedoraReleasesKnown(ctx context.Context) ([]string, error) {
	defer x.t.add("FedoraReleasesKnown", time.Now())
	return x.st.FedoraReleasesKnown(ctx)
}

func (x timedStore) FedoraAdvisories(ctx context.Context, release string, sources []string) ([]compliance.Advisory, error) {
	defer x.t.add("FedoraAdvisories", time.Now())
	return x.st.FedoraAdvisories(ctx, release, sources)
}

func (x timedStore) KEVEntries(ctx context.Context) ([]kev.Vuln, error) {
	defer x.t.add("KEVEntries", time.Now())
	return x.st.KEVEntries(ctx)
}

func (x timedStore) WindowsProducts(ctx context.Context) ([]compliance.MSRCProduct, error) {
	defer x.t.add("WindowsProducts", time.Now())
	return x.st.WindowsProducts(ctx)
}

func (x timedStore) WindowsFixes(ctx context.Context, productIDs []string) ([]compliance.WindowsFix, error) {
	defer x.t.add("WindowsFixes", time.Now())
	return x.st.WindowsFixes(ctx, productIDs)
}

func (x timedStore) MacOSMajors(ctx context.Context) ([]compliance.MacOSMajor, error) {
	defer x.t.add("MacOSMajors", time.Now())
	return x.st.MacOSMajors(ctx)
}

func (x timedStore) MacOSFixes(ctx context.Context, major int) ([]compliance.MacOSFix, error) {
	defer x.t.add("MacOSFixes", time.Now())
	return x.st.MacOSFixes(ctx, major)
}

func (x timedStore) MacOSUnfixed(ctx context.Context, major int, since time.Time) ([]compliance.MacOSCVE, error) {
	defer x.t.add("MacOSUnfixed", time.Now())
	return x.st.MacOSUnfixed(ctx, major, since)
}

func (x timedStore) AppleModel(ctx context.Context, model string) (compliance.MacModel, bool, error) {
	defer x.t.add("AppleModel", time.Now())
	return x.st.AppleModel(ctx, model)
}

func (x timedStore) AppleModelsCount(ctx context.Context) (int, error) {
	defer x.t.add("AppleModelsCount", time.Now())
	return x.st.AppleModelsCount(ctx)
}

func (x timedStore) AppleExtraBuild(ctx context.Context, build string) (compliance.MacExtra, bool, error) {
	defer x.t.add("AppleExtraBuild", time.Now())
	return x.st.AppleExtraBuild(ctx, build)
}

func (x timedStore) ThirdPartyVersions(ctx context.Context) ([]thirdparty.Ref, error) {
	defer x.t.add("ThirdPartyVersions", time.Now())
	return x.st.ThirdPartyVersions(ctx)
}

func (x timedStore) CatalogStatus(ctx context.Context, sources []string) ([]store.CatalogSourceStatus, error) {
	defer x.t.add("CatalogStatus", time.Now())
	return x.st.CatalogStatus(ctx, sources)
}

// Conferido na compilação: o cronômetro serve onde o banco da frota serve.
var _ fleetStore = timedStore{}
