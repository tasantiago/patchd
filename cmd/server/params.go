package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/mail"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/tasantiago/patchd/internal/alerts"
	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/notify"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/repocache"
	"github.com/tasantiago/patchd/internal/settings"
	"github.com/tasantiago/patchd/internal/store"
)

// Parâmetros pela tela (Aula 19.2). A lista do que a tela muda fica aqui, com a mesma
// conferência da partida. A tela vence; o .env é o valor inicial e o "restaurar o padrão".
// Fica sempre no ambiente: banco, portas, TLS, chaves e segredos, pastas, a URL pública e os
// endereços do AD e do RHBK.

// settingsRefresh: a cada quanto o servidor relê a tabela (outro servidor no mesmo banco).
const settingsRefresh = 30 * time.Second

// catalogChoices: as opções do intervalo do catálogo na tela.
var catalogChoices = []settings.Option{
	{Value: "0", Label: "desligada (só patchd-server catalog sync)"},
	{Value: "1h", Label: "de hora em hora"}, {Value: "2h", Label: "a cada 2 horas"}, {Value: "3h", Label: "a cada 3 horas"},
	{Value: "6h", Label: "a cada 6 horas"}, {Value: "12h", Label: "a cada 12 horas"}, {Value: "24h", Label: "uma vez por dia"},
}

func showDays(v string) string {
	if v == "0" {
		return "0 (guardar sempre)"
	}
	return v + " dias"
}

// settingDefs monta a lista. look é o ambiente (o diretório do cache decide se a URL dele
// pode ser mudada).
func settingDefs(look config.Lookup) []settings.Def {
	days := func(key string, min int, zeroOK bool) func(string) (string, error) {
		return func(v string) (string, error) {
			n, err := parseDays(key, v, min, zeroOK)
			if err != nil {
				return "", errors.New(strings.TrimPrefix(err.Error(), key+": "))
			}
			return strconv.Itoa(n), nil
		}
	}
	return []settings.Def{
		{Key: "PATCHD_PANEL_TITLE", Group: "Aparência", Label: "Nome do painel", Default: "patchd",
			Help: "No topo do painel, na tela de login e na Central de software. De 1 a 40 caracteres.", Effect: "na hora",
			Check: func(v string) (string, error) {
				t, ok := panelTitle(v)
				if !ok {
					return "", fmt.Errorf("use de 1 a 40 caracteres (recebido %d)", len([]rune(t)))
				}
				return t, nil
			}},
		{Key: "PATCHD_TIMEZONE", Group: "Aparência", Label: "Fuso horário",
			Help:   "Fuso IANA sugerido nas janelas de manutenção e usado nas datas da lista de Equipamentos, ex.: America/Porto_Velho. Vazio: UTC.",
			Effect: "na hora", Check: func(v string) (string, error) {
				v = strings.TrimSpace(v)
				if v == "" {
					return "", nil
				}
				if _, err := time.LoadLocation(v); err != nil || !strings.Contains(v, "/") && v != "UTC" {
					return "", fmt.Errorf("fuso desconhecido %q (use o nome IANA, ex.: America/Porto_Velho)", v)
				}
				return v, nil
			}},
		{Key: "PATCHD_RETENTION_HISTORY_DAYS", Group: "Retenção", Label: "Inventários e buscas", Kind: settings.Number, Unit: "dias",
			Default: strconv.Itoa(defaultHistoryDays), Help: "Os anteriores ao atual de cada máquina (o atual fica sempre). De 30 a 3650.",
			Effect: "na próxima passada da retenção (em até 1 hora)", Check: days("PATCHD_RETENTION_HISTORY_DAYS", 30, false), Show: showDays},
		{Key: "PATCHD_RETENTION_JOBS_DAYS", Group: "Retenção", Label: "Jobs e campanhas encerrados", Kind: settings.Number, Unit: "dias",
			Default: strconv.Itoa(defaultJobsDays), Help: "0 guarda sempre; senão, de 90 a 3650.",
			Effect: "na próxima passada da retenção (em até 1 hora)", Check: days("PATCHD_RETENTION_JOBS_DAYS", 90, true), Show: showDays},
		{Key: "PATCHD_RETENTION_AUDIT_DAYS", Group: "Retenção", Label: "Trilha de auditoria", Kind: settings.Number, Unit: "dias",
			Default: strconv.Itoa(defaultAuditDays), Help: "0 guarda sempre; senão, de 365 a 3650. Encurtar apaga eventos antigos de vez.",
			Effect: "na próxima passada da retenção (em até 1 hora)", Check: days("PATCHD_RETENTION_AUDIT_DAYS", 365, true), Show: showDays},
		{Key: "PATCHD_CATALOG_INTERVAL", Group: "Catálogo", Label: "Sincronização automática", Kind: settings.Select, Options: catalogChoices,
			Default: "6h", Help: "De quanto em quanto tempo o servidor busca as fontes do catálogo (Microsoft, Ubuntu, Fedora, Apple, KEV, terceiros).",
			Effect: "na hora: a próxima sincronização é recalculada", Check: func(v string) (string, error) {
				v = strings.TrimSpace(v)
				if _, err := parseCatalogInterval(v); err != nil {
					return "", errors.New("use 0 (desligada) ou uma duração entre 15m e 168h, ex.: 6h")
				}
				return v, nil
			},
			Show: func(v string) string {
				for _, o := range catalogChoices {
					if o.Value == v {
						return o.Label
					}
				}
				return v
			}},
		// Aula 19.3: as notificações por e-mail. A senha continua em arquivo; o conjunto é
		// conferido (notifyCheck) antes de gravar.
		{Key: "PATCHD_SMTP_ADDR", Group: "Notificações", Label: "Servidor SMTP",
			Help:   "host:porta do SMTP da instituição, ex.: smtp.exemplo:587. Vazio: notificações desligadas.",
			Effect: notifyEffect, Check: checkSMTPAddr, Show: func(v string) string {
				if v == "" {
					return "(vazio: desligadas)"
				}
				return v
			}},
		{Key: "PATCHD_SMTP_TLS", Group: "Notificações", Label: "Segurança da conexão", Kind: settings.Select, Default: notify.TLSStart,
			Options: smtpTLSChoices, Help: "STARTTLS (porta 587) é o padrão e é exigido; TLS direto para a porta 465; sem TLS só para relay interno, e sem usuário.",
			Effect: notifyEffect, Check: func(v string) (string, error) {
				v = strings.ToLower(strings.TrimSpace(v))
				if v != notify.TLSStart && v != notify.TLSWrap && v != notify.TLSNone {
					return "", errors.New("use starttls, tls ou none")
				}
				return v, nil
			}, Show: showChoice(smtpTLSChoices)},
		{Key: "PATCHD_SMTP_USER", Group: "Notificações", Label: "Usuário do SMTP",
			Help:   "Se o SMTP exigir login. A senha não vem para a tela: fica no arquivo PATCHD_SMTP_PASSWORD_FILE (o segredo smtp_password do compose).",
			Effect: notifyEffect, Check: func(v string) (string, error) {
				v = strings.TrimSpace(v)
				if len(v) > 200 || strings.ContainsFunc(v, func(r rune) bool { return r <= ' ' }) {
					return "", errors.New("usuário inválido (sem espaços, até 200 caracteres)")
				}
				return v, nil
			}},
		{Key: "PATCHD_SMTP_FROM", Group: "Notificações", Label: "Remetente",
			Help: "Quem aparece como remetente, ex.: patchd <patchd@exemplo>.", Effect: notifyEffect,
			Check: func(v string) (string, error) {
				v = strings.TrimSpace(v)
				if v == "" {
					return "", nil
				}
				if _, err := mail.ParseAddress(v); err != nil {
					return "", errors.New("endereço inválido; use nome <endereço> ou só o endereço")
				}
				return v, nil
			}},
		{Key: "PATCHD_NOTIFY_TO", Group: "Notificações", Label: "Destinatários",
			Help: "A lista da equipe, separada por vírgula (de preferência uma lista de distribuição).", Effect: notifyEffect,
			Check: func(v string) (string, error) {
				var out []string
				for _, t := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ';' }) {
					if t = strings.TrimSpace(t); t == "" {
						continue
					}
					if _, err := mail.ParseAddress(t); err != nil {
						return "", fmt.Errorf("%q não é um endereço válido", t)
					}
					out = append(out, t)
				}
				if len(out) > 50 {
					return "", errors.New("no máximo 50 destinatários: use uma lista de distribuição")
				}
				return strings.Join(out, ", "), nil
			}},
		{Key: "PATCHD_NOTIFY_EVENTS", Group: "Notificações", Label: "Avisos", Kind: settings.Multi, Options: notifyKindChoices,
			Default: strings.Join(alerts.Kinds, ","), Help: "O que gera e-mail automático (a cada 5 minutos, sem repetir o mesmo aviso).",
			Effect: notifyEffect, Check: func(v string) (string, error) {
				if strings.TrimSpace(v) == "" {
					return "", errors.New("marque ao menos um aviso (para desligar tudo, apague o servidor SMTP)")
				}
				k, err := alerts.ParseKinds(v)
				if err != nil {
					return "", errors.New(strings.TrimPrefix(err.Error(), "PATCHD_NOTIFY_EVENTS: "))
				}
				return strings.Join(k, ","), nil
			}, Show: func(v string) string {
				var l []string
				for _, k := range strings.Split(v, ",") {
					l = append(l, showChoice(notifyKindChoices)(k))
				}
				return strings.Join(l, ", ")
			}},
		{Key: "PATCHD_REPO_CACHE_URL", Group: "Cache de repositórios", Label: "URL anunciada aos agentes Linux",
			Help:   "Como as máquinas alcançam o cache, ex.: http://patchd.exemplo:8080. Vazio: o cache funciona, mas não é anunciado no check-in.",
			Effect: "no próximo check-in de cada máquina",
			Check: func(v string) (string, error) {
				v = strings.TrimSpace(v)
				if v == "" {
					return "", nil
				}
				ann := serverConfig{RepoCacheURL: v, RepoCacheHosts: config.String(look, "PATCHD_REPO_CACHE_HOSTS", repocache.DefaultHosts)}.RepoCacheAnnouncement()
				if err := ann.Validate(); err != nil {
					return "", err
				}
				return v, nil
			},
			Locked: func() string {
				if d, _ := look("PATCHD_REPO_CACHE_DIR"); strings.TrimSpace(d) == "" {
					return "o cache está desligado (sem PATCHD_REPO_CACHE_DIR no .env)"
				}
				return ""
			}},
	}
}

// notifyEffect: quando a mudança nas notificações vale.
const notifyEffect = "na hora: o próximo e-mail já sai com a configuração nova"

var smtpTLSChoices = []settings.Option{
	{Value: notify.TLSStart, Label: "STARTTLS (porta 587)"}, {Value: notify.TLSWrap, Label: "TLS direto (porta 465)"},
	{Value: notify.TLSNone, Label: "sem TLS (relay interno, sem usuário)"},
}

var notifyKindChoices = []settings.Option{
	{Value: alerts.KindRequests, Label: "pedidos esperando a TI"}, {Value: alerts.KindCampaigns, Label: "campanhas pausadas"},
	{Value: alerts.KindSources, Label: "fontes de busca falhando"},
}

func showChoice(opts []settings.Option) func(string) string {
	return func(v string) string {
		for _, o := range opts {
			if o.Value == v {
				return o.Label
			}
		}
		return v
	}
}

// checkSMTPAddr: host:porta (vazio desliga).
func checkSMTPAddr(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}
	host, port, err := net.SplitHostPort(v)
	n, perr := strconv.Atoi(port)
	if err != nil || host == "" || perr != nil || n < 1 || n > 65535 || strings.ContainsAny(host, " /") {
		return "", errors.New("use host:porta, ex.: smtp.exemplo:587")
	}
	return v, nil
}

// notifyCheck (Aula 19.3): com alguma das variáveis das notificações mudando, o conjunto tem de
// montar um Mailer (o mesmo que a partida monta). Assim não dá para gravar um SMTP com
// usuário e sem o arquivo da senha, ou TLS desligado com senha.
func notifyCheck(look settings.Lookup, changed []string) error {
	if !slices.ContainsFunc(changed, func(k string) bool {
		return strings.HasPrefix(k, "PATCHD_SMTP_") || strings.HasPrefix(k, "PATCHD_NOTIFY_")
	}) {
		return nil
	}
	if _, err := newMailer(look); err != nil {
		return fmt.Errorf("as notificações não fecham: %w", err)
	}
	_, err := notifyKinds(look)
	return err
}

// newSettings monta o serviço e lê a tabela.
func newSettings(ctx context.Context, look config.Lookup, st settings.Store, logger *slog.Logger) (*settings.Service, error) {
	s := settings.New(settingDefs(look), look, st, logger)
	s.CheckAll = notifyCheck
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return s, s.Refresh(ctx)
}

// liveRetention: a retenção que vale agora (a tela por cima do .env). Um valor que não passa
// fica de fora na releitura, então o erro aqui é só de defesa: vale a última boa.
func liveRetention(s *settings.Service, last store.Retention) func() store.Retention {
	return func() store.Retention {
		r, err := retentionConfig(s.Lookup)
		if err != nil {
			return last
		}
		last = r
		return r
	}
}

// liveCatalogInterval: o intervalo do catálogo que vale agora.
func liveCatalogInterval(s *settings.Service) func() time.Duration {
	return func() time.Duration {
		d, err := parseCatalogInterval(s.Value("PATCHD_CATALOG_INTERVAL"))
		if err != nil {
			return 0
		}
		return d
	}
}

// liveRepoCache: o anúncio do cache que vale agora (nil: sem anúncio).
func liveRepoCache(s *settings.Service, hosts string) func() *protocol.RepoCache {
	return func() *protocol.RepoCache {
		u := s.Value("PATCHD_REPO_CACHE_URL")
		if u == "" {
			return nil
		}
		ann := serverConfig{RepoCacheURL: u, RepoCacheHosts: hosts}.RepoCacheAnnouncement()
		return &ann
	}
}

// countScreen: quantos parâmetros valem pela tela.
func countScreen(s *settings.Service) int {
	n := 0
	for _, g := range s.Groups() {
		for _, f := range g.Fields {
			if f.Source == settings.FromScreen {
				n++
			}
		}
	}
	return n
}

// liveSystem: o catálogo e a retenção da aba Sistema, como valem agora.
func liveSystem(s *settings.Service, database bool, retention func() store.Retention) func() (string, string) {
	interval := liveCatalogInterval(s)
	return func() (string, string) {
		catalog := "sem banco: sem catálogo"
		if database {
			if d := interval(); d == 0 {
				catalog = "sincronização automática desligada (patchd-server catalog sync)"
			} else {
				catalog = "sincronizado a cada " + d.String()
			}
		}
		return catalog, describeRetention(retention())
	}
}

// screenFromDB: o ambiente com os parâmetros da tela por cima, para os comandos de terminal
// (notify). Sem banco, ou sem conseguir ler, o ambiente puro. done fecha a conexão.
func screenFromDB(look config.Lookup) (config.Lookup, func()) {
	cfg, err := loadServerConfig(nil, look, io.Discard)
	if err != nil || cfg.DatabaseURL == "" {
		return look, func() {}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := store.Connect(ctx, cfg.DatabaseURL, 5*time.Second)
	if err != nil {
		return look, func() {}
	}
	return screenLookup(ctx, look, store.NewPostgres(pool)), pool.Close
}

// screenLookup: para os comandos de terminal que dependem de parâmetros da tela (retention),
// o mesmo valor que o servidor usa. Sem banco, o ambiente.
func screenLookup(ctx context.Context, look config.Lookup, st settings.Store) config.Lookup {
	s := settings.New(settingDefs(look), look, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := s.Refresh(ctx); err != nil {
		return look
	}
	return s.Lookup
}

const settingsUsage = `uso: patchd-server settings <comando>

Parâmetros que o admin muda em Configuração › Parâmetros (Aula 19.2). A tela vence; o .env é
o valor inicial e o "restaurar o padrão".

comandos:
  list              mostra cada parâmetro: o valor que vale, de onde vem e o do .env
  reset PARAMETRO   apaga o valor da tela; o .env (ou o padrão) volta a valer
  reset -all        restaura todos
`

func runSettings(args []string, look config.Lookup, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "list" && args[0] != "reset") || (args[0] == "list" && len(args) != 1) ||
		(args[0] == "reset" && len(args) != 2) {
		fmt.Fprint(stderr, settingsUsage)
		return exitConfig
	}
	cfg, err := loadServerConfig(nil, look, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: configuração inválida:\n%v\n", err)
		return exitConfig
	}
	if cfg.DatabaseURL == "" {
		fmt.Fprintln(stderr, "patchd-server: os parâmetros da tela exigem o PostgreSQL (PATCHD_DATABASE_URL ou PATCHD_DATABASE_URL_FILE)")
		return exitConfig
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := store.Connect(ctx, cfg.DatabaseURL, 10*time.Second)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	defer pool.Close()
	if err := store.Migrate(ctx, pool, slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))); err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	return settingsCommand(ctx, store.NewPostgres(pool), look, args, stdout, stderr)
}

func settingsCommand(ctx context.Context, st settings.Store, look config.Lookup, args []string, stdout, stderr io.Writer) int {
	s := settings.New(settingDefs(look), look, st, slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	if err := s.Refresh(ctx); err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	if args[0] == "list" {
		tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "PARÂMETRO\tVALE\tDE ONDE\tSEM A TELA")
		for _, g := range s.Groups() {
			for _, f := range g.Fields {
				src := string(f.Source)
				if f.Source == settings.FromScreen {
					src = fmt.Sprintf("tela (%s, %s)", f.UpdatedBy, f.UpdatedAt.UTC().Format("02/01/2006 15:04")+" UTC")
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s (%s)\n", f.Key, f.ShowValue, src, f.ShowEnv, f.EnvSource)
			}
		}
		_ = tw.Flush()
		return exitOK
	}
	keys := []string{args[1]}
	if args[1] == "-all" || args[1] == "--all" {
		keys = keys[:0]
		for _, d := range s.Defs() {
			keys = append(keys, d.Key)
		}
	}
	n := 0
	for _, k := range keys {
		c, ok, err := s.Reset(ctx, k, operator(look))
		if err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			if errors.Is(err, settings.ErrUnknown) {
				return exitConfig
			}
			return exitRuntime
		}
		if ok {
			fmt.Fprintln(stdout, "restaurado:", c)
			n++
		}
	}
	if n == 0 {
		fmt.Fprintln(stdout, "nada a restaurar: o valor da tela não existia")
	}
	fmt.Fprintln(stdout, "o servidor percebe em até 30 segundos")
	return exitOK
}
