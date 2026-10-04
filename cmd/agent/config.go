package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"path/filepath"
	"time"

	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/logging"
	"github.com/tasantiago/patchd/internal/platform"
)

// agentConfig é a configuração efetiva do agente, depois de aplicar padrão, ambiente e flags.
type agentConfig struct {
	ServerURL       string
	DataDir         string
	CheckinInterval time.Duration
	LogLevel        string
	LogFormat       string
	OfflineCatalog  string // caminho do wsusscn2.cab (Windows); vazio = sem busca offline
	EnrollTokenFile string // arquivo com o token de enrollment (enroll e install)
	ShowVersion     bool
	Inventory       bool // coleta o inventário, imprime em JSON e sai
	Scan            bool // busca atualizações e histórico, imprime em JSON e sai
}

// loadAgentConfig monta a configuração com a precedência flag > variável de ambiente > padrão
// e valida o resultado. A ajuda (-h) e os erros de flag são escritos em usage.
func loadAgentConfig(args []string, look config.Lookup, usage io.Writer) (agentConfig, error) {
	var cfg agentConfig

	// Duração vinda do ambiente: o erro só vale se a flag não for informada.
	envInterval, errInterval := config.Duration(look, "PATCHD_CHECKIN_INTERVAL", time.Hour)

	fs := flag.NewFlagSet("patchd-agent", flag.ContinueOnError)
	fs.SetOutput(usage)
	config.SetUsage(fs)
	fs.StringVar(&cfg.ServerURL, "server", config.String(look, "PATCHD_SERVER_URL", ""),
		"URL do patchd-server, ex.: https://patchd.exemplo:8443 (env PATCHD_SERVER_URL)")
	fs.StringVar(&cfg.DataDir, "data-dir", config.String(look, "PATCHD_DATA_DIR", platform.DataDir()),
		"pasta de dados do agente (env PATCHD_DATA_DIR)")
	fs.DurationVar(&cfg.CheckinInterval, "checkin-interval", envInterval,
		"intervalo entre check-ins, ex.: 30m ou 1h (env PATCHD_CHECKIN_INTERVAL)")
	fs.StringVar(&cfg.LogLevel, "log-level", config.String(look, "PATCHD_LOG_LEVEL", "info"),
		"nível de log: debug, info, warn ou error (env PATCHD_LOG_LEVEL)")
	fs.StringVar(&cfg.LogFormat, "log-format", config.String(look, "PATCHD_LOG_FORMAT", "json"),
		"formato de log: json ou text (env PATCHD_LOG_FORMAT)")
	fs.StringVar(&cfg.OfflineCatalog, "offline-cab", config.String(look, "PATCHD_OFFLINE_CAB", ""),
		"caminho do wsusscn2.cab para a busca offline no Windows (env PATCHD_OFFLINE_CAB)")
	fs.StringVar(&cfg.EnrollTokenFile, "enroll-token-file", config.String(look, "PATCHD_ENROLL_TOKEN_FILE", ""),
		"arquivo com o token de enrollment, usado por enroll e install (env PATCHD_ENROLL_TOKEN_FILE)")
	fs.BoolVar(&cfg.ShowVersion, "version", false, "mostra a versão e sai")
	fs.BoolVar(&cfg.Inventory, "inventory", false, "coleta o inventário, imprime em JSON na saída padrão e sai")
	fs.BoolVar(&cfg.Scan, "scan", false, "busca atualizações faltantes e o histórico, imprime em JSON na saída padrão e sai")

	if err := fs.Parse(args); err != nil {
		// O pacote flag já escreveu a mensagem e a ajuda em usage.
		return cfg, &config.UsageError{Err: err}
	}
	if cfg.ShowVersion {
		return cfg, nil
	}

	// Quais flags foram informadas explicitamente na linha de comando.
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })

	// Junta todos os problemas, para o operador corrigir tudo de uma vez.
	var problems []error
	// O pacote flag para de ler flags no primeiro argumento que não é flag;
	// argumentos sobrando indicam flags que foram ignoradas.
	if fs.NArg() > 0 {
		problems = append(problems, fmt.Errorf("argumentos não reconhecidos: %q (flags depois deles são ignoradas)", fs.Args()))
	}
	if cfg.Inventory && cfg.Scan {
		problems = append(problems, errors.New("use -inventory ou -scan, não os dois"))
	}
	if cfg.OfflineCatalog != "" && !filepath.IsAbs(cfg.OfflineCatalog) {
		problems = append(problems, fmt.Errorf("-offline-cab/PATCHD_OFFLINE_CAB: o caminho precisa ser absoluto (recebido %q)", cfg.OfflineCatalog))
	}
	if errInterval != nil && !given["checkin-interval"] {
		problems = append(problems, errInterval)
	}
	if cfg.CheckinInterval < time.Minute {
		problems = append(problems, fmt.Errorf("-checkin-interval/PATCHD_CHECKIN_INTERVAL: mínimo de 1m (recebido %s)", cfg.CheckinInterval))
	}
	if cfg.ServerURL != "" {
		if err := validateServerURL(cfg.ServerURL); err != nil {
			problems = append(problems, err)
		}
	}
	if !filepath.IsAbs(cfg.DataDir) {
		problems = append(problems, fmt.Errorf("-data-dir/PATCHD_DATA_DIR: o caminho precisa ser absoluto (recebido %q)", cfg.DataDir))
	}
	if err := logging.Check(cfg.LogLevel, cfg.LogFormat); err != nil {
		problems = append(problems, err)
	}
	return cfg, errors.Join(problems...)
}

// validateServerURL exige https (RNF-05). http só é aceito para o próprio host, em desenvolvimento.
// A URL recusada nunca é repetida na mensagem quando contém credenciais.
func validateServerURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return errors.New("-server/PATCHD_SERVER_URL: URL inválida")
	}
	if u.User != nil {
		return errors.New("-server/PATCHD_SERVER_URL: não inclua usuário ou senha na URL")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if host == "localhost" {
			return nil
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return nil
		}
		return fmt.Errorf("-server/PATCHD_SERVER_URL: http só é permitido para localhost; use https (recebido %q)", raw)
	default:
		return fmt.Errorf("-server/PATCHD_SERVER_URL: esquema %q não suportado; use https", u.Scheme)
	}
}
