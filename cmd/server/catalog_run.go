package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"text/tabwriter"
	"time"

	"github.com/tasantiago/patchd/internal/buildinfo"
	"github.com/tasantiago/patchd/internal/catalog/apple"
	"github.com/tasantiago/patchd/internal/catalog/bodhi"
	"github.com/tasantiago/patchd/internal/catalog/kev"
	"github.com/tasantiago/patchd/internal/catalog/msrc"
	"github.com/tasantiago/patchd/internal/catalog/osv"
	"github.com/tasantiago/patchd/internal/catalog/retry"
	"github.com/tasantiago/patchd/internal/catalog/wsusscan"
	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
)

// catalogSourceNames são as fontes, na ordem em que rodam.
var catalogSourceNames = []string{"msrc", "ubuntu", "fedora", "apple", "kev", "terceiros", "wsusscn2"}

// catalogStale: uma fonte sem sincronização bem-sucedida há mais que isso está atrasada.
// Com o agendamento padrão de 6 h, são oito execuções perdidas seguidas.
const catalogStale = 48 * time.Hour

// syncOptions são as opções da sincronização. O agendamento usa os padrões do catalog sync.
type syncOptions struct {
	Sources map[string]bool
	Months  int    // janela do MSRC
	MaxUSN  int    // teto de USNs por execução (0: sem teto)
	Workers int    // downloads simultâneos do Ubuntu
	Full    bool   // MSRC: todos os documentos da janela; Fedora: todos os updates
	Trigger string // agendada ou manual
	// Done, quando definido, recebe o resultado de cada fonte assim que ela termina (o
	// agendamento escreve no log na hora, não só no fim da execução).
	Done func(sourceResult)
}

// O wsusscn2 só entra quando há onde guardar o arquivo (PATCHD_CONTENT_DIR).
func defaultSyncOptions(trigger string, withContent bool) syncOptions {
	all := map[string]bool{}
	for _, s := range catalogSourceNames {
		all[s] = s != "wsusscn2" || withContent
	}
	return syncOptions{Sources: all, Months: 12, Workers: 4, Trigger: trigger}
}

// wsusscanSource baixa o wsusscn2.cab para o diretório de conteúdo.
type wsusscanSource interface {
	Sync(ctx context.Context, dir string, prev *wsusscan.File, progress func(string)) (wsusscan.File, bool, error)
}

// contentStore guarda a descrição dos arquivos distribuídos.
type contentStore interface {
	ContentFile(ctx context.Context, name string) (wsusscan.File, bool, error)
	SaveContentFile(ctx context.Context, name string, f wsusscan.File) error
}

// catalogSources são as fontes da sincronização; os testes passam versões falsas.
type catalogSources struct {
	MSRC        msrcSource
	Ubuntu      ubuntuSource
	Fedora      fedoraSource
	Apple       appleSource
	AppleErr    error // o cliente da Apple não pôde ser montado (a raiz embutida)
	KEV         kevSource
	Third       thirdpartySource
	Manifest    thirdparty.Manifest
	ManifestErr error
	// Retries conta os pedidos repetidos pelos clientes (nil nos testes: nenhum).
	Retries *atomic.Int64
	// WSUS baixa o wsusscn2.cab para ContentDir (vazio: sem diretório de conteúdo).
	WSUS          wsusscanSource
	ContentDir    string
	ContentDirErr error
}

func (s catalogSources) retries() int64 {
	if s.Retries == nil {
		return 0
	}
	return s.Retries.Load()
}

// newCatalogSources monta os clientes reais, com os endereços trocados pelo ambiente.
func newCatalogSources(look config.Lookup) catalogSources {
	ua := "patchd-server/" + buildinfo.Get().Version
	env := func(name string) (string, bool) {
		v, ok := look(name)
		return v, ok && v != ""
	}
	var s catalogSources
	s.Retries = new(atomic.Int64)
	// Repetição de pedidos (erro de rede, 429, 5xx) em todas as fontes, menos o Bodhi, que
	// já tem a sua desde a Aula 6.2.
	count := func(*http.Request, int, string) { s.Retries.Add(1) }
	m := msrc.NewClient(ua)
	m.HTTP = retry.Wrap(m.HTTP, count)
	if u, ok := env("PATCHD_MSRC_URL"); ok {
		m.BaseURL = u
	}
	o := osv.NewClient("Ubuntu", ua)
	o.HTTP = retry.Wrap(o.HTTP, count)
	if u, ok := env("PATCHD_OSV_URL"); ok {
		o.BaseURL = u
	}
	b := bodhi.NewClient(ua)
	if u, ok := env("PATCHD_BODHI_URL"); ok {
		b.BaseURL = u
	}
	a, err := apple.NewClient(ua)
	if err == nil {
		a.GDMF = retry.Wrap(a.GDMF, count) // mantém o transporte com a raiz da Apple, por baixo
		a.SOFA = retry.Wrap(a.SOFA, count)
		if u, ok := env("PATCHD_GDMF_URL"); ok {
			a.GDMFURL = u
		}
		if u, ok := env("PATCHD_SOFA_URL"); ok {
			a.SOFAURL = u
		}
		s.Apple = a
	}
	s.AppleErr = err
	k := kev.NewClient(ua)
	k.HTTP = retry.Wrap(k.HTTP, count)
	if u, ok := env("PATCHD_KEV_URL"); ok {
		k.URL = u
	}
	path, _ := look("PATCHD_THIRDPARTY_MANIFEST")
	s.Manifest, s.ManifestErr = thirdparty.Load(path)
	t := thirdparty.NewClient(ua)
	t.HTTP = retry.Wrap(t.HTTP, count)
	s.MSRC, s.Ubuntu, s.Fedora, s.KEV, s.Third = m, o, b, k, t
	// O wsusscn2.cab tem a própria repetição, com retomada: centenas de MB não passam pela
	// leitura em memória do retry.
	w := wsusscan.NewClient(ua)
	if u, ok := env("PATCHD_WSUSSCN2_URL"); ok {
		w.URL = u
	}
	s.WSUS = w
	s.ContentDir, s.ContentDirErr = contentDir(look)
	return s
}

// contentDir lê PATCHD_CONTENT_DIR: a pasta onde o servidor guarda e de onde distribui o
// wsusscn2.cab. Vazio: sem distribuição.
func contentDir(look config.Lookup) (string, error) {
	dir, ok := look("PATCHD_CONTENT_DIR")
	if !ok || dir == "" {
		return "", nil
	}
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("PATCHD_CONTENT_DIR precisa ser um caminho absoluto (recebido %q)", dir)
	}
	return dir, nil
}

// catalogStore é o banco da sincronização inteira.
type catalogStore interface {
	msrcStore
	ubuntuStore
	fedoraStore
	appleStore
	kevStore
	thirdpartyStore
	contentStore
	RecordCatalogRun(ctx context.Context, source, trigger string, started, finished time.Time, ok bool, detail string) error
}

// sourceResult é o resultado de uma fonte numa execução.
type sourceResult struct {
	Source    string
	OK        bool
	Detail    string
	Duration  time.Duration
	RecordErr error // o registro em catalog_runs falhou: a fonte pode aparecer como atrasada sem estar
}

// syncCatalog roda as fontes pedidas, em sequência, e registra cada uma em catalog_runs.
// Uma fonte que falha não impede as outras. A saída detalhada de cada fonte vai para out.
func syncCatalog(ctx context.Context, srcs catalogSources, st catalogStore, opts syncOptions, out io.Writer) []sourceResult {
	var results []sourceResult
	for _, name := range catalogSourceNames {
		if !opts.Sources[name] {
			continue
		}
		if ctx.Err() != nil {
			break // encerramento: o que não rodou não é registrado como falha
		}
		started, before := time.Now(), srcs.retries()
		ok, detail := syncOneSource(ctx, name, srcs, st, opts, out)
		if n := srcs.retries() - before; n > 0 {
			detail += fmt.Sprintf("; %d pedido(s) repetido(s)", n)
		}
		r := sourceResult{Source: name, OK: ok, Detail: detail, Duration: time.Since(started).Round(time.Millisecond)}
		r.RecordErr = st.RecordCatalogRun(ctx, name, opts.Trigger, started.UTC(), time.Now().UTC(), ok, detail)
		if r.RecordErr != nil {
			fmt.Fprintf(out, "  (o registro da execução de %s falhou: %v)\n", name, r.RecordErr)
		}
		if opts.Done != nil {
			opts.Done(r)
		}
		results = append(results, r)
	}
	return results
}

// syncOneSource roda uma fonte e devolve se deu certo e o resumo.
func syncOneSource(ctx context.Context, name string, srcs catalogSources, st catalogStore, opts syncOptions, out io.Writer) (bool, string) {
	failure := func(err error) (bool, string) {
		fmt.Fprintf(out, "  %s FALHOU: %v\n", name, err)
		return false, err.Error()
	}
	switch name {
	case "msrc":
		fmt.Fprintln(out, "MSRC:")
		since := time.Now().UTC().AddDate(0, -opts.Months, 0)
		r, err := syncMSRCWith(ctx, srcs.MSRC, st, since, opts.Full, out)
		if err != nil {
			return failure(err)
		}
		summary := fmt.Sprintf("%d documento(s) gravado(s), %d com falha", r.Synced, r.Failed)
		if r.Missing > 0 {
			summary += fmt.Sprintf(", %d ausente(s) da lista (mantidos)", r.Missing)
		}
		fmt.Fprintln(out, summary)
		return r.Failed == 0, summary
	case "ubuntu":
		fmt.Fprintln(out, "Ubuntu (OSV):")
		res, err := syncUbuntu(ctx, srcs.Ubuntu, st, opts.MaxUSN, opts.Workers, out)
		if err != nil {
			return failure(err)
		}
		if remaining := res.Pending - res.Saved - res.Withdrawn - res.Failed; remaining > 0 {
			fmt.Fprintf(out, "  ubuntu: %d ficam para a próxima execução (-max)\n", remaining)
		}
		return res.Failed == 0, fmt.Sprintf("%d USN(s) gravada(s), %d retirada(s), %d com falha, %d ausente(s) da lista",
			res.Saved, res.Withdrawn, res.Failed, res.Missing)
	case "fedora":
		fmt.Fprintln(out, "Fedora (Bodhi):")
		n, nfail, err := syncFedora(ctx, srcs.Fedora, st, opts.Full, out)
		if err != nil {
			return failure(err)
		}
		fmt.Fprintf(out, "  fedora: %d update(s) gravado(s), %d versão(ões) com falha\n", n, nfail)
		return nfail == 0, fmt.Sprintf("%d update(s) gravado(s), %d versão(ões) com falha", n, nfail)
	case "apple":
		fmt.Fprintln(out, "Apple (gdmf e SOFA):")
		if srcs.AppleErr != nil {
			return failure(srcs.AppleErr)
		}
		changed, nfail, err := syncApple(ctx, srcs.Apple, st, out)
		if err != nil {
			return failure(err)
		}
		return nfail == 0, fmt.Sprintf("%d fonte(s) atualizada(s), %d com falha", changed, nfail)
	case "kev":
		fmt.Fprintln(out, "CISA KEV:")
		changed, kfail, err := syncKEV(ctx, srcs.KEV, st, out)
		switch {
		case err != nil:
			return failure(err)
		case kfail:
			return false, "o feed falhou; o catálogo atual fica"
		case changed:
			return true, "atualizado"
		}
		return true, "em dia"
	case "terceiros":
		fmt.Fprintln(out, "Programas de terceiros:")
		if srcs.ManifestErr != nil {
			return failure(srcs.ManifestErr)
		}
		saved, tfail, err := syncThirdParty(ctx, srcs.Manifest, srcs.Third, st, out)
		if err != nil {
			return failure(err)
		}
		return tfail == 0, fmt.Sprintf("%d versão(ões) gravada(s), %d com falha", saved, tfail)
	case "wsusscn2":
		fmt.Fprintln(out, "Windows Update offline (wsusscn2.cab):")
		switch {
		case srcs.ContentDirErr != nil:
			return failure(srcs.ContentDirErr)
		case srcs.ContentDir == "":
			return failure(errors.New("defina PATCHD_CONTENT_DIR: a pasta absoluta onde o servidor guarda o arquivo"))
		}
		var prev *wsusscan.File
		if f, ok, err := st.ContentFile(ctx, "wsusscn2"); err != nil {
			return failure(err)
		} else if ok {
			prev = &f
		}
		started := time.Now()
		f, changed, err := srcs.WSUS.Sync(ctx, srcs.ContentDir, prev, func(line string) { fmt.Fprintf(out, "  %s\n", line) })
		if err != nil {
			return failure(err)
		}
		state := "em dia"
		if changed {
			if err := st.SaveContentFile(ctx, "wsusscn2", f); err != nil {
				return failure(err)
			}
			state = fmt.Sprintf("baixado em %s", time.Since(started).Round(time.Second))
		}
		summary := fmt.Sprintf("%s: %s, %.1f MiB, publicado pela Microsoft em %s UTC, sha256 %s...",
			state, f.Name, float64(f.Size)/(1<<20), f.LastModified.Format("2006-01-02 15:04"), f.SHA256[:16])
		fmt.Fprintf(out, "  wsusscn2: %s\n", summary)
		return true, summary
	}
	return failure(fmt.Errorf("fonte desconhecida"))
}

// catalogLocker é a trava que impede duas sincronizações ao mesmo tempo no mesmo banco.
type catalogLocker interface {
	TryCatalogLock(ctx context.Context) (unlock func(), ok bool, err error)
}

// errCatalogBusy: outra sincronização (o agendamento do servidor, outro catalog sync ou
// outra instância) está em andamento.
var errCatalogBusy = errors.New("outra sincronização do catálogo está em andamento neste banco (o agendamento do servidor ou outro catalog sync); tente de novo depois")

// withCatalogLock roda fn com a trava do catálogo, ou devolve errCatalogBusy sem esperar.
func withCatalogLock(ctx context.Context, l catalogLocker, fn func()) error {
	unlock, ok, err := l.TryCatalogLock(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return errCatalogBusy
	}
	defer unlock()
	fn()
	return nil
}

// catalogScheduler é o que o agendamento usa do banco.
type catalogScheduler interface {
	catalogStore
	catalogLocker
	PruneCatalogRuns(ctx context.Context, before time.Time) (int64, error)
}

// catalogRunsRetention: por quanto tempo o registro das execuções é guardado.
const catalogRunsRetention = 90 * 24 * time.Hour

// catalogLoop sincroniza o catálogo dentro do servidor: a primeira vez depois de first, e
// depois a cada interval. Cada execução disputa a trava do banco; se outro processo estiver
// sincronizando, esta pula a vez. O resultado de cada fonte vai para o log.
func catalogLoop(ctx context.Context, logger *slog.Logger, st catalogScheduler, srcs catalogSources, interval, first time.Duration) {
	catalogLoopWith(ctx, logger, st, srcs, defaultSyncOptions("agendada", srcs.ContentDir != ""), interval, first)
}

// catalogLoopWith é o laço do agendamento, com as opções como parâmetro (os testes limitam
// as fontes).
func catalogLoopWith(ctx context.Context, logger *slog.Logger, st catalogScheduler, srcs catalogSources, opts syncOptions, interval, first time.Duration) {
	logger.Info("sincronização do catálogo agendada", "primeira_em", first.Round(time.Second).String(), "intervalo", interval.String())
	timer := time.NewTimer(first)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		runScheduledSync(ctx, logger, st, srcs, opts)
		timer.Reset(interval)
	}
}

// runScheduledSync faz uma execução agendada.
func runScheduledSync(ctx context.Context, logger *slog.Logger, st catalogScheduler, srcs catalogSources, opts syncOptions) {
	started := time.Now()
	var results []sourceResult
	opts.Done = func(r sourceResult) {
		if r.OK {
			logger.Info("catálogo sincronizado", "fonte", r.Source, "duracao", r.Duration.String(), "resumo", r.Detail)
		} else {
			logger.Warn("catálogo com falha", "fonte", r.Source, "duracao", r.Duration.String(), "resumo", r.Detail)
		}
		if r.RecordErr != nil {
			logger.Error("registro da sincronização falhou: a fonte pode aparecer como atrasada", "fonte", r.Source, "error", r.RecordErr)
		}
	}
	err := withCatalogLock(ctx, st, func() {
		logger.Info("sincronização agendada iniciada")
		// A saída detalhada (uma linha por documento, por USN com falha...) não vai para o
		// log: o resumo de cada fonte vai, e o detalhe fica no catalog sync manual.
		results = syncCatalog(ctx, srcs, st, opts, io.Discard)
	})
	switch {
	case errors.Is(err, errCatalogBusy):
		logger.Info("sincronização agendada pulada: outra está em andamento")
		return
	case err != nil:
		if ctx.Err() == nil {
			logger.Error("sincronização agendada falhou", "error", err)
		}
		return
	}
	failed := 0
	for _, r := range results {
		if !r.OK {
			failed++
		}
	}
	logger.Info("sincronização agendada concluída", "fontes", len(results), "com_falha", failed,
		"duracao", time.Since(started).Round(time.Second).String())
	if n, err := st.PruneCatalogRuns(ctx, time.Now().Add(-catalogRunsRetention)); err != nil && ctx.Err() == nil {
		logger.Error("limpeza do registro de sincronizações falhou", "error", err)
	} else if n > 0 {
		logger.Info("registro de sincronizações antigas apagado", "count", n)
	}
}

// firstCatalogRun espalha a primeira execução entre 1 e 5 minutos depois da partida: o
// servidor já está atendendo, e várias instâncias (ou reinícios seguidos) não batem nas
// fontes no mesmo instante.
func firstCatalogRun() time.Duration {
	return time.Minute + rand.N(4*time.Minute)
}

// printCatalogStatus escreve o "catalog status".
func printCatalogStatus(ss []store.CatalogSourceStatus, now time.Time, out io.Writer) {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "FONTE\tSITUAÇÃO\tÚLTIMO SUCESSO (UTC)\tÚLTIMA FALHA (UTC)\tEXECUÇÕES\tÚLTIMA EXECUÇÃO")
	day := func(t time.Time) string {
		if t.IsZero() {
			return "-"
		}
		return t.Format("2006-01-02 15:04")
	}
	for _, s := range ss {
		last := "-"
		if s.Runs > 0 {
			last = s.Trigger + ": " + s.Detail
			if r := []rune(last); len(r) > 90 {
				last = string(r[:87]) + "..."
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\n", s.Source, catalogState(s, now), day(s.LastOK), day(s.LastFail), s.Runs, last)
	}
	tw.Flush()
}

// catalogState resume a situação de uma fonte.
func catalogState(s store.CatalogSourceStatus, now time.Time) string {
	switch {
	case s.LastOK.IsZero():
		return "NUNCA SINCRONIZADA"
	case now.Sub(s.LastOK) > catalogStale:
		return fmt.Sprintf("ATRASADA (%s)", ago(now.Sub(s.LastOK)))
	case !s.Latest:
		return "em dia, mas a última falhou"
	}
	return "em dia"
}

// ago escreve uma duração como "3 dias" ou "5 h".
func ago(d time.Duration) string {
	if d >= 48*time.Hour {
		return fmt.Sprintf("há %d dias", int(d.Hours()/24))
	}
	return fmt.Sprintf("há %d h", int(d.Hours()))
}

// catalogWarnings devolve os avisos de catálogo atrasado para as fontes de uma avaliação.
func catalogWarnings(ss []store.CatalogSourceStatus, now time.Time) []string {
	var out []string
	for _, s := range ss {
		switch {
		case s.LastOK.IsZero():
			out = append(out, fmt.Sprintf("catálogo %s sem sincronização bem-sucedida registrada (o registro começou na Aula 6.6)", s.Source))
		case now.Sub(s.LastOK) > catalogStale:
			out = append(out, fmt.Sprintf("catálogo %s sincronizado com sucesso pela última vez %s; a avaliação pode estar perdendo correções novas",
				s.Source, ago(now.Sub(s.LastOK))))
		}
	}
	return out
}
