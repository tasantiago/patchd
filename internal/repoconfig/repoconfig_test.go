package repoconfig

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/protocol"
)

// raiz monta um sistema de arquivos falso com os executáveis de cada gerenciador.
func raiz(t *testing.T, gerenciadores ...string) *Config {
	t.Helper()
	r := t.TempDir()
	for _, g := range gerenciadores {
		arquivos := map[string][]string{"apt": {aptGet}, "dnf": {dnf5}}[g]
		for _, f := range arquivos {
			p := filepath.Join(r, f)
			os.MkdirAll(filepath.Dir(p), 0o755)
			os.WriteFile(p, nil, 0o755)
		}
		if g == "apt" {
			os.MkdirAll(filepath.Join(r, aptConfDir), 0o755)
		}
	}
	return &Config{Root: r}
}

func cache(t *testing.T) (*httptest.Server, protocol.RepoCache) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repo/stats" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"HIT":0}`))
	}))
	t.Cleanup(srv.Close)
	return srv, protocol.RepoCache{URL: srv.URL + "/", AptHosts: []string{"archive.ubuntu.com", "security.ubuntu.com"}}
}

func TestApplyGravaSoParaOsGerenciadoresDaMaquina(t *testing.T) {
	_, rc := cache(t)
	casos := []struct {
		gerenciadores []string
		arquivos      []string
	}{
		{[]string{"apt"}, []string{AptFile}},
		{[]string{"dnf"}, []string{DnfFile}},
		{[]string{"apt", "dnf"}, []string{AptFile, DnfFile}},
		{nil, nil},
	}
	for _, c := range casos {
		cfg := raiz(t, c.gerenciadores...)
		res, err := cfg.Apply(context.Background(), rc)
		if err != nil || !slices.Equal(res.Written, c.arquivos) {
			t.Errorf("%v: gravado %v, erro %v", c.gerenciadores, res.Written, err)
		}
		// De novo, igual: nada muda.
		if res, _ := cfg.Apply(context.Background(), rc); res.Changed() {
			t.Errorf("%v: reaplicar mudou %v", c.gerenciadores, res.Written)
		}
	}
}

func TestConteudo(t *testing.T) {
	rc := protocol.RepoCache{URL: "http://patchd.exemplo:8080/", AptHosts: []string{"archive.ubuntu.com", "security.ubuntu.com"}}
	apt := string(AptContent(rc))
	for _, l := range []string{
		`Acquire::http::Proxy::archive.ubuntu.com "http://patchd.exemplo:8080";`,
		`Acquire::http::Proxy::security.ubuntu.com "http://patchd.exemplo:8080";`,
	} {
		if !strings.Contains(apt, l+"\n") {
			t.Errorf("apt sem %q:\n%s", l, apt)
		}
	}
	if strings.Contains(apt, "Acquire::http::Proxy \"") {
		t.Error("o proxy do apt nunca é global, só por origem")
	}
	dnf := string(DnfContent(rc))
	for _, l := range []string{
		"[fedora]\nbaseurl=http://patchd.exemplo:8080/repo/fedora/fedora/$releasever/$basearch/\nmetalink=\nmirrorlist=\n",
		"[updates]\nbaseurl=http://patchd.exemplo:8080/repo/fedora/updates/$releasever/$basearch/\nmetalink=\nmirrorlist=\n",
	} {
		if !strings.Contains(dnf, l) {
			t.Errorf("dnf sem %q:\n%s", l, dnf)
		}
	}
}

func TestSyncRemoveQuandoOAnuncioSomeOuOCacheNaoResponde(t *testing.T) {
	srv, rc := cache(t)
	cfg := raiz(t, "apt", "dnf")
	ctx := context.Background()
	if _, err := cfg.Sync(ctx, &rc); err != nil || len(cfg.Installed()) != 2 {
		t.Fatalf("aplicar: %v, %v", err, cfg.Installed())
	}
	// Anúncio ausente: apaga.
	res, err := cfg.Sync(ctx, nil)
	if err != nil || len(res.Removed) != 2 || len(cfg.Installed()) != 0 {
		t.Errorf("sem anúncio: %+v %v", res, err)
	}
	// De novo sem anúncio: nada a fazer.
	if res, _ := cfg.Sync(ctx, nil); res.Changed() {
		t.Errorf("sem anúncio, 2ª vez: %+v", res)
	}
	// Cache fora: não grava, e apaga o que houver.
	cfg.Sync(ctx, &rc)
	srv.Close()
	res, err = cfg.Sync(ctx, &rc)
	if err == nil || !strings.Contains(err.Error(), "não respondeu") || len(cfg.Installed()) != 0 {
		t.Errorf("cache fora: %+v %v %v", res, err, cfg.Installed())
	}
}

func TestAnuncioInvalidoNaoTocaEmNada(t *testing.T) {
	cfg := raiz(t, "apt")
	ruim := protocol.RepoCache{URL: "http://x\";Acquire::http::Proxy \"http://mal", AptHosts: []string{"archive.ubuntu.com"}}
	if _, err := cfg.Apply(context.Background(), ruim); err == nil || len(cfg.Installed()) != 0 {
		t.Errorf("anúncio com injeção aplicado: %v %v", err, cfg.Installed())
	}
	// Um cache respondendo outra coisa em /repo/stats (o cache desligado): recusado.
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	rc := protocol.RepoCache{URL: srv.URL, AptHosts: []string{"archive.ubuntu.com"}}
	if _, err := cfg.Apply(context.Background(), rc); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("cache desligado: %v", err)
	}
}
