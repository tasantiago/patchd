package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/buildinfo"
	"github.com/tasantiago/patchd/internal/metrics"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

func TestConfiguracaoDasMetricas(t *testing.T) {
	dir := t.TempDir()
	bom := filepath.Join(dir, "metrics_token")
	_ = os.WriteFile(bom, []byte(strings.Repeat("a1", 16)+"\n"), 0o600)
	curto := filepath.Join(dir, "curto")
	_ = os.WriteFile(curto, []byte("abc\n"), 0o600)
	base := func(kv ...string) map[string]string {
		m := map[string]string{"PATCHD_CATALOG_INTERVAL": "0"}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	for nome, c := range map[string]struct {
		env map[string]string
		msg string
	}{
		"porta sem token":     {base("PATCHD_METRICS_LISTEN_ADDR", ":9090"), "exige PATCHD_METRICS_TOKEN_FILE"},
		"token curto":         {base("PATCHD_METRICS_LISTEN_ADDR", ":9090", "PATCHD_METRICS_TOKEN_FILE", curto), "pelo menos 24"},
		"mesma porta da API":  {base("PATCHD_METRICS_LISTEN_ADDR", ":8080", "PATCHD_METRICS_TOKEN_FILE", bom), "só para as métricas"},
		"porta inválida":      {base("PATCHD_METRICS_LISTEN_ADDR", "9090", "PATCHD_METRICS_TOKEN_FILE", bom), "PATCHD_METRICS_LISTEN_ADDR"},
		"token e arquivo":     {base("PATCHD_METRICS_LISTEN_ADDR", ":9090", "PATCHD_METRICS_TOKEN_FILE", bom, "PATCHD_METRICS_TOKEN", "x"), "apenas um"},
		"arquivo inexistente": {base("PATCHD_METRICS_LISTEN_ADDR", ":9090", "PATCHD_METRICS_TOKEN_FILE", filepath.Join(dir, "nao")), "ler o arquivo"},
	} {
		_, err := loadServerConfig(nil, lookMap(c.env), io.Discard)
		if err == nil || !strings.Contains(err.Error(), c.msg) {
			t.Errorf("%s: %v", nome, err)
		}
		if err != nil && strings.Contains(err.Error(), "a1a1") {
			t.Errorf("%s: o token apareceu no erro", nome)
		}
	}
	cfg, err := loadServerConfig(nil, lookMap(base("PATCHD_METRICS_LISTEN_ADDR", "127.0.0.1:9090", "PATCHD_METRICS_TOKEN_FILE", bom)), io.Discard)
	if err != nil || cfg.MetricsListenAddr != "127.0.0.1:9090" || cfg.MetricsToken != strings.Repeat("a1", 16) {
		t.Errorf("válida: %q %v", cfg.MetricsListenAddr, err)
	}
	// Desligadas: sem porta, o token (se houver) é ignorado.
	cfg, err = loadServerConfig(nil, lookMap(base("PATCHD_METRICS_TOKEN_FILE", bom)), io.Discard)
	if err != nil || cfg.MetricsListenAddr != "" || cfg.MetricsToken != "" {
		t.Errorf("desligadas: %+v %v", cfg.MetricsListenAddr, err)
	}
}

type bancoDasMetricas struct {
	leituras int
	falha    error
	semCopia bool
}

func (b *bancoDasMetricas) Machines(ctx context.Context) ([]protocol.MachineSummary, error) {
	b.leituras++
	if b.falha != nil {
		return nil, b.falha
	}
	sim, nao := true, false
	return []protocol.MachineSummary{{ID: "maq-1", CheckinTLS: &sim}, {ID: "maq-2", CheckinTLS: &sim},
		{ID: "maq-3", CheckinTLS: &nao}, {ID: "maq-4"}}, nil
}

func (b *bancoDasMetricas) JobStateCounts(ctx context.Context) (map[string]int, error) {
	return map[string]int{"concluido": 5, "pendente": 2}, nil
}

func (b *bancoDasMetricas) CatalogStatus(ctx context.Context, sources []string) ([]store.CatalogSourceStatus, error) {
	ok := time.Unix(1_790_000_000, 0)
	return []store.CatalogSourceStatus{
		{Source: "msrc", LastOK: ok, Latest: true, Runs: 3},
		{Source: "ubuntu", LastOK: ok, Latest: false, Runs: 4},
		{Source: "apple"}, // nunca rodou: fora das duas séries
	}, nil
}

func (b *bancoDasMetricas) LastBackup(ctx context.Context) (store.BackupRun, bool, error) {
	if b.semCopia {
		return store.BackupRun{}, false, nil
	}
	return store.BackupRun{FinishedAt: time.Unix(1_790_090_000, 0), File: "patchd-20260912T023000Z.dump.age", Bytes: 4_500_000, Encrypted: true}, true, nil
}

func (b *bancoDasMetricas) TableSizes(ctx context.Context) (map[string]int64, int64, error) {
	return map[string]int64{"inventory_reports": 3 << 20, "jobs": 64 << 10}, 9 << 20, nil
}

type frotaDasMetricas struct{}

func (frotaDasMetricas) Fleet(ctx context.Context) (protocol.ComplianceFleet, error) {
	return protocol.ComplianceFleet{Counts: map[string]int{"em_dia": 1, "faltando": 2, "reboot_pendente": 0, "sem_dado_recente": 0, "desconhecido": 1}}, nil
}

type validade time.Time

func (v validade) NotAfter() time.Time { return time.Time(v) }

func TestMedidores(t *testing.T) {
	banco := &bancoDasMetricas{}
	log := &bytes.Buffer{}
	snap := newSnapshotter(banco, frotaDasMetricas{}, slog.New(slog.NewTextHandler(log, nil)))
	agora := time.Unix(1_790_100_000, 0)
	snap.now = func() time.Time { return agora }
	reg := metrics.NewRegistry()
	registerGauges(reg, snap, validade(time.Unix(1_800_000_000, 0)), buildinfo.Info{Version: "v0.10.2", Commit: "abc123"}, time.Unix(1_790_000_500, 0))
	coleta := func() string {
		var b strings.Builder
		if err := reg.WriteText(&b); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	texto := coleta()
	for _, linha := range []string{
		`patchd_build_info{version="v0.10.2",commit="abc123"} 1`,
		`patchd_start_time_seconds 1790000500`,
		`patchd_tls_cert_not_after_timestamp_seconds 1800000000`,
		`patchd_coleta_banco_ok 1`,
		`patchd_machines{canal="http"} 1`, `patchd_machines{canal="https"} 2`, `patchd_machines{canal="nenhum"} 1`,
		`patchd_compliance_machines{estado="faltando"} 2`, `patchd_compliance_machines{estado="reboot_pendente"} 0`,
		`patchd_jobs{estado="concluido"} 5`, `patchd_jobs{estado="falhou"} 0`, `patchd_jobs{estado="expirado"} 0`,
		`patchd_catalog_last_success_timestamp_seconds{fonte="msrc"} 1790000000`,
		`patchd_catalog_last_run_ok{fonte="msrc"} 1`, `patchd_catalog_last_run_ok{fonte="ubuntu"} 0`,
		"# TYPE patchd_machines gauge",
		`patchd_db_bytes 9437184`, `patchd_db_table_bytes{tabela="inventory_reports"} 3145728`, `patchd_db_table_bytes{tabela="jobs"} 65536`,
		`patchd_backup_last_success_timestamp_seconds 1790090000`, `patchd_backup_last_size_bytes 4500000`, `patchd_backup_last_encrypted 1`,
	} {
		if !strings.Contains(texto, linha+"\n") {
			t.Errorf("falta %q", linha)
		}
	}
	if strings.Contains(texto, `fonte="apple"`) || strings.Contains(texto, "maq-") {
		t.Error("fonte que nunca rodou, ou ID de máquina, na saída")
	}
	// Uma leitura do banco por coleta (todos os medidores usam a mesma), e nenhuma nova
	// antes de 1 minuto.
	if banco.leituras != 1 {
		t.Errorf("leituras na primeira coleta: %d", banco.leituras)
	}
	agora = agora.Add(30 * time.Second)
	coleta()
	if banco.leituras != 1 {
		t.Errorf("antes de 1 minuto: %d leituras", banco.leituras)
	}
	// Banco fora: coleta_banco_ok 0, sem números velhos, e o motivo no log.
	banco.falha = errors.New("conexão recusada")
	agora = agora.Add(time.Minute)
	texto = coleta()
	if banco.leituras != 2 || !strings.Contains(texto, "patchd_coleta_banco_ok 0\n") || strings.Contains(texto, "patchd_machines{") ||
		strings.Contains(texto, "patchd_jobs{") || !strings.Contains(texto, "patchd_build_info{") {
		t.Errorf("banco fora (%d leituras):\n%s", banco.leituras, texto)
	}
	if !strings.Contains(log.String(), "conexão recusada") {
		t.Errorf("log: %s", log.String())
	}
	// Nenhuma cópia registrada: as séries das cópias não existem (o alerta usa absent()).
	banco.falha, banco.semCopia = nil, true
	agora = agora.Add(time.Minute)
	texto = coleta()
	if !strings.Contains(texto, "patchd_coleta_banco_ok 1\n") || strings.Contains(texto, "\npatchd_backup_last_success_timestamp_seconds ") ||
		!strings.Contains(texto, "# TYPE patchd_backup_last_success_timestamp_seconds gauge") {
		t.Errorf("sem cópia:\n%s", texto)
	}
}

// TestPortaDasMetricas: a porta própria, com TLS quando o servidor tem TLS, só responde com
// o token, e o NotAfter do certificado vem do certReloader.
func TestPortaDasMetricas(t *testing.T) {
	dir := t.TempDir()
	cert := certDeTeste(t, dir, 7)
	c, err := newCertReloader(filepath.Join(dir, "srv.crt"), filepath.Join(dir, "srv.key"), silencio())
	if err != nil {
		t.Fatal(err)
	}
	if !c.NotAfter().Equal(cert.NotAfter) {
		t.Errorf("NotAfter: %v (quer %v)", c.NotAfter(), cert.NotAfter)
	}
	reg := metrics.NewRegistry()
	registerGauges(reg, nil, c, buildinfo.Info{Version: "v0.10.2", Commit: "abc"}, time.Now())
	const token = "um-token-de-teste-com-32-caracteres!"
	addr := portaLivre(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, silencio(), []listener{{name: "Métricas", addr: addr, handler: reg.Handler(token), tls: tlsConfig(c)}})
	}()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("encerramento: %v", err)
		}
	}()
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	cl := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	pede := func(auth string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, "https://"+addr+"/metrics", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		var resp *http.Response
		for i := 0; i < 50; i++ {
			if resp, err = cl.Do(req); err == nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, _ := pede(""); code != http.StatusUnauthorized {
		t.Errorf("sem token: %d", code)
	}
	if code, _ := pede("Bearer outro-token"); code != http.StatusUnauthorized {
		t.Errorf("token errado: %d", code)
	}
	code, body := pede("Bearer " + token)
	if code != 200 || !strings.Contains(body, "patchd_tls_cert_not_after_timestamp_seconds ") || strings.Contains(body, "patchd_machines") {
		t.Errorf("com token: %d\n%s", code, body)
	}
	// Em HTTP puro, a porta TLS não responde as métricas.
	if resp, err := (&http.Client{Timeout: 2 * time.Second}).Get("http://" + addr + "/metrics"); err == nil {
		resp.Body.Close()
		if resp.StatusCode == 200 {
			t.Error("métricas em HTTP numa porta TLS")
		}
	}
}
