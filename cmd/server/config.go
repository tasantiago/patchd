package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"

	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/logging"
)

// serverConfig é a configuração efetiva do servidor, depois de aplicar padrão, ambiente e flags.
type serverConfig struct {
	ListenAddr  string
	DatabaseURL string // segredo: só por PATCHD_DATABASE_URL ou PATCHD_DATABASE_URL_FILE
	LogLevel    string
	LogFormat   string
	ShowVersion bool
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
	fs.BoolVar(&cfg.ShowVersion, "version", false, "mostra a versão e sai")

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
