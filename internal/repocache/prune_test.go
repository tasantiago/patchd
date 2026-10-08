package repocache

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func cacheVazio(t *testing.T, agora time.Time) *Cache {
	t.Helper()
	c, err := New(t.TempDir(), DefaultHosts, "teste", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	c.now = func() time.Time { return agora }
	return c
}

// arquivo cria um arquivo do cache com o tamanho e o último uso dados.
func arquivo(t *testing.T, c *Cache, rel string, tamanho int, uso time.Time) string {
	t.Helper()
	p := filepath.Join(c.Dir, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o750)
	if err := os.WriteFile(p, []byte(strings.Repeat("x", tamanho)), 0o640); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(p, uso, uso)
	return p
}

func existe(p string) bool { _, err := os.Stat(p); return err == nil }

func TestPrunePorIdadeETamanho(t *testing.T) {
	agora := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	c := cacheVazio(t, agora)
	dia := 24 * time.Hour
	velho := arquivo(t, c, "sha256/aa/aaaa", 100, agora.Add(-40*dia))
	velhoPool := arquivo(t, c, "pool/archive.ubuntu.com/ubuntu/pool/main/s/sudo/sudo_1_amd64.deb", 300, agora.Add(-31*dia))
	medio := arquivo(t, c, "sha256/bb/bbbb", 400, agora.Add(-20*dia))
	novo := arquivo(t, c, "pool/fedora/updates/44/x86_64/Packages/o/o-1.rpm", 500, agora.Add(-1*dia))
	indice := arquivo(t, c, "index/archive.ubuntu.com/ubuntu/dists/resolute/InRelease", 50, agora.Add(-90*dia))
	sobra := arquivo(t, c, "tmp/obj-123", 10, agora.Add(-2*dia))
	emAndamento := arquivo(t, c, "tmp/obj-456", 10, agora.Add(-time.Minute))

	// Simulação: conta, não apaga.
	res, err := c.Prune(PruneOptions{MaxAge: 30 * dia, DryRun: true})
	if err != nil || res.ByAge != 2 || !existe(velho) {
		t.Fatalf("dry-run: %+v %v", res, err)
	}
	if a := res.Before["sha256"]; a.Files != 2 || a.Bytes != 500 || !a.Oldest.Equal(agora.Add(-40*dia)) {
		t.Errorf("retrato sha256: %+v", a)
	}

	// Idade: saem os dois sem uso há mais de 30 dias; o índice fica (mesmo velho).
	res, err = c.Prune(PruneOptions{MaxAge: 30 * dia, MaxSize: 1000})
	if err != nil || res.ByAge != 2 || res.BySize != 0 || res.RemovedBytes != 400 || res.Tmp != 1 {
		t.Errorf("idade: %+v %v", res, err)
	}
	for p, quer := range map[string]bool{velho: false, velhoPool: false, medio: true, novo: true, indice: true, sobra: false, emAndamento: true} {
		if existe(p) != quer {
			t.Errorf("%s: existe=%v, esperado %v", p, existe(p), quer)
		}
	}
	// A pasta do pacote apagado some; as raízes ficam.
	if existe(filepath.Join(c.Dir, "pool", "archive.ubuntu.com")) || !existe(filepath.Join(c.Dir, "pool")) {
		t.Error("pastas vazias: as do pacote deviam sumir, a raiz pool/ não")
	}

	// Tamanho: 900 bytes guardados, limite 600: sai o menos usado (medio).
	res, _ = c.Prune(PruneOptions{MaxSize: 600})
	if res.BySize != 1 || existe(medio) || !existe(novo) {
		t.Errorf("tamanho: %+v, medio=%v novo=%v", res, existe(medio), existe(novo))
	}
}

func TestHITRenovaOUltimoUsoUmaVezPorDia(t *testing.T) {
	agora := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	c := cacheVazio(t, agora)
	antigo := arquivo(t, c, "sha256/cc/cccc", 10, agora.Add(-10*24*time.Hour))
	recente := arquivo(t, c, "sha256/dd/dddd", 10, agora.Add(-time.Hour))
	for _, p := range []string{antigo, recente} {
		fi, _ := os.Stat(p)
		c.touch(p, fi.ModTime())
	}
	if fi, _ := os.Stat(antigo); !fi.ModTime().Equal(agora) {
		t.Errorf("antigo: %v (devia virar agora)", fi.ModTime())
	}
	if fi, _ := os.Stat(recente); !fi.ModTime().Equal(agora.Add(-time.Hour)) {
		t.Errorf("recente: %v (usado há 1 h, não se escreve de novo)", fi.ModTime())
	}
}

func TestParseSize(t *testing.T) {
	for v, quer := range map[string]int64{"0": 0, "1024": 1024, "500M": 500 << 20, "50G": 50 << 30, "50GiB": 50 << 30, "2t": 2 << 40, " 10k ": 10 << 10} {
		if got, err := ParseSize(v); err != nil || got != quer {
			t.Errorf("%q: %d %v", v, got, err)
		}
	}
	for _, v := range []string{"", "x", "-1G", "1.5G", "G"} {
		if _, err := ParseSize(v); err == nil {
			t.Errorf("%q aceito", v)
		}
	}
}

// Um cache da parte 4c (datas da origem, de anos atrás) não é apagado na primeira limpeza:
// na abertura, as datas viram "agora", uma vez só.
func TestCacheAntigoGanhaOPrazoInteiro(t *testing.T) {
	dir := t.TempDir()
	velho := filepath.Join(dir, "sha256", "ee", "eeee")
	os.MkdirAll(filepath.Dir(velho), 0o750)
	os.WriteFile(velho, []byte("x"), 0o640)
	origem := time.Date(2024, 4, 23, 18, 34, 0, 0, time.UTC)
	os.Chtimes(velho, origem, origem)

	c, err := New(dir, DefaultHosts, "teste", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if res, _ := c.Prune(PruneOptions{MaxAge: 30 * 24 * time.Hour}); res.Removed != 0 || !existe(velho) {
		t.Errorf("primeira limpeza apagou o cache antigo: %+v", res)
	}
	// Na segunda abertura, a marca existe: a data não é renovada de novo.
	os.Chtimes(velho, origem, origem)
	if _, err := New(dir, DefaultHosts, "teste", slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(velho); !fi.ModTime().Equal(origem) {
		t.Errorf("a data foi renovada de novo: %v", fi.ModTime())
	}
}
