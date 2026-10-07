package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/catalog/apple"
	"github.com/tasantiago/patchd/internal/catalog/kev"
	"github.com/tasantiago/patchd/internal/catalog/msrc"
	"github.com/tasantiago/patchd/internal/catalog/osv"
	"github.com/tasantiago/patchd/internal/catalog/retry"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
)

// bancoCatalogoFalso junta os bancos falsos das fontes usadas nos testes (KEV e terceiros);
// as interfaces embutidas e vazias (MSRC, Ubuntu, Fedora, Apple) não são chamadas.
type bancoCatalogoFalso struct {
	msrcStore
	ubuntuStore
	fedoraStore
	appleStore
	*bancoKEVFalso
	*bancoTerceirosFalso

	mu        sync.Mutex
	execucoes []string // "fonte gatilho ok resumo"
	ocupado   bool
	travado   bool
	limpezas  int
}

// FeedHash existe no banco da Apple e no do KEV: aqui, vale o do KEV.
func (b *bancoCatalogoFalso) FeedHash(ctx context.Context, source string) (string, error) {
	return b.bancoKEVFalso.FeedHash(ctx, source)
}

func (b *bancoCatalogoFalso) RecordCatalogRun(_ context.Context, source, trigger string, started, finished time.Time, ok bool, detail string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if finished.Before(started) {
		return errors.New("fim antes do início")
	}
	estado := "ok"
	if !ok {
		estado = "FALHA"
	}
	b.execucoes = append(b.execucoes, strings.Join([]string{source, trigger, estado, detail}, " "))
	return nil
}

func (b *bancoCatalogoFalso) TryCatalogLock(context.Context) (func(), bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ocupado || b.travado {
		return nil, false, nil
	}
	b.travado = true
	return func() { b.mu.Lock(); b.travado = false; b.mu.Unlock() }, true, nil
}

func (b *bancoCatalogoFalso) PruneCatalogRuns(context.Context, time.Time) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.limpezas++
	return 0, nil
}

func (b *bancoCatalogoFalso) registros() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.execucoes...)
}

func fontesDeTeste(t *testing.T) catalogSources {
	t.Helper()
	m, err := thirdparty.Default()
	if err != nil {
		t.Fatal(err)
	}
	return catalogSources{
		AppleErr: errors.New("raiz da Apple ilegível"),
		KEV:      &kevFalso{cat: kev.Catalog{Version: "2026.10.07", Released: "2026-10-07T10:00:00Z", Vulns: []kev.Vuln{{CVE: "CVE-2026-1"}}}},
		Third:    &fontesTerceirosFalsas{pedidos: map[string]int{}, versoes: map[string]string{"chrome:win64": "155.0.8059.40"}},
		Manifest: m,
	}
}

func novoBanco() *bancoCatalogoFalso {
	return &bancoCatalogoFalso{bancoKEVFalso: &bancoKEVFalso{}, bancoTerceirosFalso: &bancoTerceirosFalso{gravadas: map[string]string{}}}
}

func TestSyncCatalog(t *testing.T) {
	banco := novoBanco()
	opts := syncOptions{Sources: map[string]bool{"apple": true, "kev": true, "terceiros": true}, Trigger: "manual"}
	var out bytes.Buffer
	res := syncCatalog(context.Background(), fontesDeTeste(t), banco, opts, &out)
	if len(res) != 3 || res[0].Source != "apple" || res[0].OK || !res[1].OK || res[1].Detail != "atualizado" || res[2].OK {
		t.Fatalf("resultados: %+v", res)
	}
	reg := banco.registros()
	if len(reg) != 3 || reg[0] != "apple manual FALHA raiz da Apple ilegível" || reg[1] != "kev manual ok atualizado" ||
		!strings.HasPrefix(reg[2], "terceiros manual FALHA 1 versão(ões) gravada(s), ") {
		t.Errorf("registros: %q", reg)
	}
	if !strings.Contains(out.String(), "apple FALHOU: raiz da Apple ilegível") {
		t.Errorf("saída:\n%s", out.String())
	}

	// Encerramento antes de começar: nada roda, nada é registrado como falha.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if res := syncCatalog(ctx, fontesDeTeste(t), novoBanco(), opts, &out); len(res) != 0 {
		t.Errorf("com o contexto cancelado: %+v", res)
	}
}

func TestRunScheduledSync(t *testing.T) {
	var log bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&log, nil))
	opts := syncOptions{Sources: map[string]bool{"kev": true}, Trigger: "agendada"}

	// Outro processo com a trava: esta execução pula a vez, sem registrar nada.
	banco := novoBanco()
	banco.ocupado = true
	runScheduledSync(context.Background(), logger, banco, fontesDeTeste(t), opts)
	if len(banco.registros()) != 0 || !strings.Contains(log.String(), "sincronização agendada pulada") {
		t.Errorf("com a trava ocupada: %q\n%s", banco.registros(), log.String())
	}

	log.Reset()
	banco = novoBanco()
	runScheduledSync(context.Background(), logger, banco, fontesDeTeste(t), opts)
	if reg := banco.registros(); len(reg) != 1 || reg[0] != "kev agendada ok atualizado" || banco.travado || banco.limpezas != 1 {
		t.Errorf("execução: %q travado=%t limpezas=%d", reg, banco.travado, banco.limpezas)
	}
	for _, trecho := range []string{"catálogo sincronizado", "fonte=kev", "sincronização agendada concluída", "com_falha=0"} {
		if !strings.Contains(log.String(), trecho) {
			t.Errorf("faltou %q no log:\n%s", trecho, log.String())
		}
	}
}

func TestCatalogLoop(t *testing.T) {
	banco := novoBanco()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	feito := make(chan struct{})
	go func() {
		catalogLoopWith(ctx, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), banco, fontesDeTeste(t),
			syncOptions{Sources: map[string]bool{"kev": true}, Trigger: "agendada"}, 20*time.Millisecond, time.Millisecond)
		close(feito)
	}()
	limite := time.After(5 * time.Second)
	for len(banco.registros()) < 3 {
		select {
		case <-limite:
			t.Fatalf("o agendamento rodou %d vez(es) em 5 s", len(banco.registros()))
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-feito:
	case <-time.After(5 * time.Second):
		t.Fatal("o agendamento não parou com o contexto cancelado")
	}
}

func TestParseCatalogInterval(t *testing.T) {
	for in, want := range map[string]time.Duration{"0": 0, "6h": 6 * time.Hour, "15m": 15 * time.Minute, "168h": 168 * time.Hour} {
		if got, err := parseCatalogInterval(in); err != nil || got != want {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
	for _, in := range []string{"", "6", "10m", "169h", "-1h", "seis horas"} {
		if _, err := parseCatalogInterval(in); err == nil {
			t.Errorf("%q deveria ser recusado", in)
		}
	}
	if d := firstCatalogRun(); d < time.Minute || d >= 5*time.Minute {
		t.Errorf("primeira execução em %v", d)
	}
}

func TestPrintCatalogStatus(t *testing.T) {
	agora := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	ss := []store.CatalogSourceStatus{
		{Source: "msrc", LastOK: agora.Add(-75 * time.Hour), LastFail: agora.Add(-time.Hour), Trigger: "agendada", Detail: "HTTP 503", Runs: 12},
		{Source: "ubuntu", LastOK: agora.Add(-5 * time.Hour), LastFail: agora.Add(-time.Hour), Trigger: "agendada", Detail: "1 com falha", Runs: 4},
		{Source: "kev", LastOK: agora.Add(-time.Hour), Latest: true, Trigger: "manual", Detail: "em dia", Runs: 2},
		{Source: "terceiros"},
	}
	var out bytes.Buffer
	printCatalogStatus(ss, agora, &out)
	for _, trecho := range []string{
		"msrc       ATRASADA (há 3 dias)",
		"ubuntu     em dia, mas a última falhou",
		"kev        em dia",
		"manual: em dia",
		"terceiros  NUNCA SINCRONIZADA",
	} {
		if !strings.Contains(out.String(), trecho) {
			t.Errorf("faltou %q em:\n%s", trecho, out.String())
		}
	}
}

func TestSyncCatalogContaRepeticoes(t *testing.T) {
	banco := novoBanco()
	srcs := fontesDeTeste(t)
	srcs.Retries = new(atomic.Int64)
	// A fonte falsa do KEV "repete" dois pedidos, como faria o transporte com repetição.
	srcs.KEV = kevQueRepete{kevFalso: srcs.KEV.(*kevFalso), contador: srcs.Retries}
	var vistos []string
	opts := syncOptions{Sources: map[string]bool{"kev": true}, Trigger: "manual", Done: func(r sourceResult) { vistos = append(vistos, r.Source) }}
	res := syncCatalog(context.Background(), srcs, banco, opts, &bytes.Buffer{})
	if len(res) != 1 || res[0].Detail != "atualizado; 2 pedido(s) repetido(s)" || len(vistos) != 1 {
		t.Errorf("repetições no resumo: %+v %v", res, vistos)
	}
}

type kevQueRepete struct {
	*kevFalso
	contador *atomic.Int64
}

func (k kevQueRepete) Fetch(ctx context.Context) (kev.Catalog, error) {
	k.contador.Add(2)
	return k.kevFalso.Fetch(ctx)
}

func TestNewCatalogSourcesComRepeticao(t *testing.T) {
	srcs := newCatalogSources(func(string) (string, bool) { return "", false })
	for nome, hc := range map[string]*http.Client{
		"msrc": srcs.MSRC.(*msrc.Client).HTTP, "osv": srcs.Ubuntu.(*osv.Client).HTTP, "kev": srcs.KEV.(*kev.Client).HTTP,
		"gdmf": srcs.Apple.(*apple.Client).GDMF, "sofa": srcs.Apple.(*apple.Client).SOFA, "terceiros": srcs.Third.(*thirdparty.Client).HTTP,
	} {
		if _, ok := hc.Transport.(*retry.Transport); !ok {
			t.Errorf("%s sem repetição: %T", nome, hc.Transport)
		}
	}
	// O gdmf continua com o transporte que só aceita a raiz da Apple, por baixo da repetição.
	if base := srcs.Apple.(*apple.Client).GDMF.Transport.(*retry.Transport).Base; base == nil {
		t.Error("gdmf perdeu o transporte com a raiz da Apple")
	}
	if srcs.ManifestErr != nil || srcs.Retries == nil {
		t.Errorf("manifesto: %v, contador: %v", srcs.ManifestErr, srcs.Retries)
	}
}
