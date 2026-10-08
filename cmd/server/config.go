package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"time"

	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/logging"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/repocache"
)

// serverConfig é a configuração efetiva do servidor, depois de aplicar padrão, ambiente e flags.
type serverConfig struct {
	ListenAddr  string
	DatabaseURL string // segredo: só por PATCHD_DATABASE_URL ou PATCHD_DATABASE_URL_FILE
	LogLevel    string
	LogFormat   string
	ReleasesDir string // pasta das versões do agente (Aula 5.4); vazia: sem atualização automática
	// CatalogInterval: de quanto em quanto tempo o servidor sincroniza o catálogo (Aula 6.6);
	// zero desliga (o catalog sync manual continua funcionando).
	CatalogInterval time.Duration
	// RepoCacheDir liga o cache dos repositórios Ubuntu (Aula 6.6, parte 4b); vazio desliga.
	// RepoCacheHosts são as origens que o cache aceita.
	RepoCacheDir   string
	RepoCacheHosts string
	// RepoCacheURL: como as máquinas alcançam o cache; com ele, o check-in anuncia o cache
	// e os agentes Linux se configuram (Aula 6.6). Vazio: o cache funciona, sem anúncio.
	RepoCacheURL string
	ShowVersion  bool
	HealthCheck  bool // modo usado pelo HEALTHCHECK do Docker
}

// loadServerConfig monta a configuração com a precedência flag > variável de ambiente > padrão
// e valida o resultado. A ajuda (-h) e os erros de flag são escritos em usage.
func loadServerConfig(args []string, look config.Lookup, usage io.Writer) (serverConfig, error) {
	var cfg serverConfig

	fs := flag.NewFlagSet("patchd-server", flag.ContinueOnError)
	fs.SetOutput(usage)
	config.SetUsage(fs)
	fs.StringVar(&cfg.ListenAddr, "listen", config.String(look, "PATCHD_LISTEN_ADDR", ":8080"),
		"endereço de escuta da API, ex.: :8080 (env PATCHD_LISTEN_ADDR)")
	fs.StringVar(&cfg.LogLevel, "log-level", config.String(look, "PATCHD_LOG_LEVEL", "info"),
		"nível de log: debug, info, warn ou error (env PATCHD_LOG_LEVEL)")
	fs.StringVar(&cfg.LogFormat, "log-format", config.String(look, "PATCHD_LOG_FORMAT", "json"),
		"formato de log: json ou text (env PATCHD_LOG_FORMAT)")
	fs.StringVar(&cfg.ReleasesDir, "releases-dir", config.String(look, "PATCHD_RELEASES_DIR", ""),
		"pasta das versões assinadas do agente; vazia desliga a atualização automática (env PATCHD_RELEASES_DIR)")
	catalogInterval := fs.String("catalog-interval", config.String(look, "PATCHD_CATALOG_INTERVAL", "6h"),
		"intervalo da sincronização do catálogo, ex.: 6h; 0 desliga (env PATCHD_CATALOG_INTERVAL)")
	fs.StringVar(&cfg.RepoCacheDir, "repo-cache-dir", config.String(look, "PATCHD_REPO_CACHE_DIR", ""),
		"pasta do cache dos repositórios Ubuntu; vazia desliga o cache (env PATCHD_REPO_CACHE_DIR)")
	fs.StringVar(&cfg.RepoCacheHosts, "repo-cache-hosts", config.String(look, "PATCHD_REPO_CACHE_HOSTS", repocache.DefaultHosts),
		"origens aceitas pelo cache, separadas por vírgula (env PATCHD_REPO_CACHE_HOSTS)")
	fs.StringVar(&cfg.RepoCacheURL, "repo-cache-url", config.String(look, "PATCHD_REPO_CACHE_URL", ""),
		"URL pela qual as máquinas alcançam o cache, anunciada no check-in, ex.: http://patchd.exemplo:8080 (env PATCHD_REPO_CACHE_URL)")
	fs.BoolVar(&cfg.ShowVersion, "version", false, "mostra a versão e sai")
	fs.BoolVar(&cfg.HealthCheck, "healthcheck", false,
		"consulta o /healthz do servidor local e sai com 0 (saudável) ou 1 (uso do HEALTHCHECK do Docker)")

	if err := fs.Parse(args); err != nil {
		// O pacote flag já escreveu a mensagem e a ajuda em usage.
		return cfg, &config.UsageError{Err: err}
	}
	if cfg.ShowVersion {
		return cfg, nil
	}

	var problems []error
	if fs.NArg() > 0 {
		problems = append(problems, fmt.Errorf("argumentos não reconhecidos: %q (flags depois deles são ignoradas)", fs.Args()))
	}
	if err := validateListenAddr(cfg.ListenAddr); err != nil {
		problems = append(problems, err)
	}
	if d, err := parseCatalogInterval(*catalogInterval); err != nil {
		problems = append(problems, err)
	} else {
		cfg.CatalogInterval = d
	}
	if cfg.ReleasesDir != "" && !filepath.IsAbs(cfg.ReleasesDir) {
		problems = append(problems, fmt.Errorf("-releases-dir/PATCHD_RELEASES_DIR: o caminho precisa ser absoluto (recebido %q)", cfg.ReleasesDir))
	}

	if cfg.RepoCacheDir != "" {
		if !filepath.IsAbs(cfg.RepoCacheDir) {
			problems = append(problems, fmt.Errorf("-repo-cache-dir/PATCHD_REPO_CACHE_DIR: o caminho precisa ser absoluto (recebido %q)", cfg.RepoCacheDir))
		}
		if _, err := repocache.ParseHosts(cfg.RepoCacheHosts); err != nil {
			problems = append(problems, err)
		}
	}
	if cfg.RepoCacheURL != "" {
		if cfg.RepoCacheDir == "" {
			problems = append(problems, errors.New("-repo-cache-url/PATCHD_REPO_CACHE_URL: anunciar o cache exige PATCHD_REPO_CACHE_DIR"))
		} else if err := cfg.RepoCacheAnnouncement().Validate(); err != nil {
			problems = append(problems, fmt.Errorf("-repo-cache-url/PATCHD_REPO_CACHE_URL: %w", err))
		}
	}

	// Segredo: nunca por flag (a linha de comando é visível para outros usuários).
	dbURL, err := config.Secret(look, "PATCHD_DATABASE_URL")
	if err != nil {
		problems = append(problems, err)
	}
	cfg.DatabaseURL = dbURL
	if cfg.DatabaseURL != "" {
		if err := validateDatabaseURL(cfg.DatabaseURL); err != nil {
			problems = append(problems, err)
		}
	}

	if err := logging.Check(cfg.LogLevel, cfg.LogFormat); err != nil {
		problems = append(problems, err)
	}
	return cfg, errors.Join(problems...)
}

// parseCatalogInterval aceita "0" (desligado) ou uma duração entre 15 minutos e 7 dias: menos
// que isso só sobrecarrega as fontes (e a API do GitHub limita os pedidos); mais, e o
// catálogo atrasa sem ninguém notar.
func parseCatalogInterval(v string) (time.Duration, error) {
	if v == "0" {
		return 0, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 15*time.Minute || d > 7*24*time.Hour {
		return 0, fmt.Errorf("-catalog-interval/PATCHD_CATALOG_INTERVAL: use 0 (desligado) ou uma duração entre 15m e 168h, ex.: 6h (recebido %q)", v)
	}
	return d, nil
}

// validateListenAddr aceita "host:porta" ou ":porta", com porta entre 1 e 65535.
func validateListenAddr(addr string) error {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("-listen/PATCHD_LISTEN_ADDR: endereço inválido %q (use host:porta ou :porta)", addr)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("-listen/PATCHD_LISTEN_ADDR: porta inválida %q (use de 1 a 65535)", port)
	}
	return nil
}

// validateDatabaseURL confere o formato da URL do PostgreSQL.
// As mensagens nunca incluem a URL: ela contém a senha.
func validateDatabaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		// O erro do url.Parse repete a URL inteira; por isso não é repassado.
		return errors.New("PATCHD_DATABASE_URL: URL mal formada")
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return fmt.Errorf("PATCHD_DATABASE_URL: esquema %q não suportado; use postgres://", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("PATCHD_DATABASE_URL: falta o host do banco")
	}
	return nil
}

// redactDatabaseURL devolve a URL do banco com a senha mascarada, para uso em logs.
// Mascara tanto usuário:senha@ quanto o parâmetro ?password=.
func redactDatabaseURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "(URL inválida)"
	}
	q := u.Query()
	if q.Has("password") {
		q.Set("password", "xxxxx")
		u.RawQuery = q.Encode()
	}
	return u.Redacted()
}

// RepoCacheAnnouncement é o anúncio do check-in montado da configuração.
func (c serverConfig) RepoCacheAnnouncement() protocol.RepoCache {
	hs, _ := repocache.ParseHosts(c.RepoCacheHosts)
	list := make([]string, 0, len(hs))
	for h := range hs {
		list = append(list, h)
	}
	return protocol.RepoCache{URL: c.RepoCacheURL, AptHosts: list}
}
