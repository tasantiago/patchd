// Comando patchd-server: API, catálogo, motor de compliance e fila de jobs.
package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/tasantiago/patchd/internal/ad"
	"github.com/tasantiago/patchd/internal/alerts"
	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/buildinfo"
	"github.com/tasantiago/patchd/internal/campaign"
	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/logging"
	"github.com/tasantiago/patchd/internal/machinebox"
	"github.com/tasantiago/patchd/internal/metrics"
	"github.com/tasantiago/patchd/internal/oidc"
	"github.com/tasantiago/patchd/internal/release"
	"github.com/tasantiago/patchd/internal/repocache"
	"github.com/tasantiago/patchd/internal/settings"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
)

// Códigos de saída do servidor.
const (
	exitOK      = 0
	exitRuntime = 1 // falha durante a execução (porta ocupada, banco indisponível etc.)
	exitConfig  = 2 // mesma convenção do pacote flag para erro de uso
)

func main() {
	os.Exit(run(os.Args[1:], os.LookupEnv, os.Stdout, os.Stderr))
}

// panelTitle limpa o nome do painel: espaços nas pontas e um par de aspas em volta
// ("patchd TJRO" ou 'patchd TJRO'). ok=false: vazio ou com mais de 40 caracteres.
func panelTitle(v string) (string, bool) {
	t := strings.TrimSpace(v)
	if len(t) >= 2 && (t[0] == '"' || t[0] == '\'') && t[len(t)-1] == t[0] {
		t = strings.TrimSpace(t[1 : len(t)-1])
	}
	n := len([]rune(t))
	return t, n > 0 && n <= 40
}

// run concentra a lógica do servidor. Argumentos, ambiente e saídas chegam como
// parâmetros para que a função possa ser testada sem criar um processo.
func run(args []string, look config.Lookup, stdout, stderr io.Writer) (code int) {
	info := buildinfo.Get()

	// Aula 18.1: subcomando que muda alguma coisa entra na trilha de auditoria, com o
	// resultado, depois de rodar.
	if action := cliAction(args); action != "" {
		defer func() { auditCLI(action, args, look, code, stderr) }()
	}

	// Subcomandos de administração: "patchd-server token create|list|revoke",
	// "patchd-server release current|publish|withdraw", "patchd-server catalog sync|list|builds|usn|fedora|apple|kev"
	// "patchd-server compliance -machine ID", "patchd-server repo-cache status|prune",
	// "patchd-server user create|list|delete|password", "patchd-server ad check" e
	// "patchd-server machine list|retire|restore", "patchd-server job keygen|create|list|cancel"
	// e "patchd-server package show" (Aula 14.1) e "patchd-server bios keygen|key" (Aula 21.4)
	// e "patchd-server inventory summary" (Aula 22.1).
	if len(args) > 0 && args[0] == "token" {
		return runToken(args[1:], look, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "release" {
		return runRelease(args[1:], look, stdout, stderr, info.Version)
	}
	if len(args) >= 2 && args[0] == "discovery" && args[1] == "keygen" {
		return discoveryKeygen(args[2:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "catalog" {
		return runCatalog(args[1:], look, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "compliance" {
		return runCompliance(args[1:], look, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "repo-cache" {
		return runRepoCache(args[1:], look, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "user" {
		return runUser(args[1:], look, os.Stdin, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "ad" {
		return runAD(args[1:], look, os.Stdin, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "machine" {
		return runMachine(args[1:], look, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "job" {
		return runJob(args[1:], look, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "ring" {
		return runRing(args[1:], look, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "campaign" {
		return runCampaign(args[1:], look, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "package" {
		return runPackage(args[1:], look, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "retention" {
		return runRetention(args[1:], look, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "audit" {
		return runAudit(args[1:], look, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "selfservice" {
		return runSelfService(args[1:], look, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "settings" {
		return runSettings(args[1:], look, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "notify" {
		return runNotify(args[1:], look, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "session" {
		return runSession(args[1:], look, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "bios" { // Aula 21.4: a chave da guarda da senha da BIOS
		return runBios(args[1:], look, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "inventory" { // Aula 22.1: resumo de inventários avulsos, sem banco
		return runInventory(args[1:], stdout, stderr)
	}

	cfg, err := loadServerConfig(args, look, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return exitOK
	}
	var usageErr *config.UsageError
	if errors.As(err, &usageErr) {
		// O pacote flag já mostrou a mensagem e a ajuda; não repetir.
		return exitConfig
	}
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: configuração inválida:\n%v\n", err)
		return exitConfig
	}
	if cfg.ShowVersion {
		fmt.Fprintf(stdout, "patchd-server %s\n", info)
		return exitOK
	}
	if cfg.HealthCheck {
		if err := healthcheck(cfg.ListenAddr, cfg.TLS()); err != nil {
			fmt.Fprintf(stderr, "patchd-server: healthcheck falhou: %v\n", err)
			return exitRuntime
		}
		return exitOK
	}

	logger, err := logging.New(stderr, cfg.LogLevel, cfg.LogFormat)
	if err != nil {
		// Não deveria acontecer: nível e formato já foram validados.
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitConfig
	}
	logger = logger.With("app", "patchd-server", "version", info.Version)
	slog.SetDefault(logger)

	// SIGTERM vem do "docker stop"; SIGINT, do Ctrl+C no terminal.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("servidor iniciado",
		"commit", info.Commit,
		"listen", cfg.ListenAddr,
		"database", redactDatabaseURL(cfg.DatabaseURL),
	)
	// TLS (Aula 9.1): o par é lido já; certificado ilegível não deixa o servidor subir.
	var certs *certReloader
	if cfg.TLS() {
		certs, err = newCertReloader(cfg.TLSCertFile, cfg.TLSKeyFile, logger)
		if err != nil {
			logger.Error("TLS", "error", err)
			return exitConfig
		}
		exp, names, chain := certs.Info()
		days := int(time.Until(exp).Hours() / 24)
		logger.Info("TLS ligado (TLS 1.2 ou mais novo; o certificado é relido quando muda no disco)",
			"expira_em_dias", days, "nomes", names, "cadeia", chain)
		if time.Until(exp) < certWarnBefore {
			logger.Warn("o certificado TLS vence em menos de 30 dias; peça a renovação à CA interna", "expira_em_dias", days)
		}
		switch {
		case cfg.HTTPListenAddr != "" && cfg.HTTPCacheOnly:
			logger.Info("porta HTTP só para o cache de repositórios: registro, agentes e painel só por HTTPS",
				"http_listen", cfg.HTTPListenAddr)
		case cfg.HTTPListenAddr != "":
			logger.Warn("porta HTTP de transição aberta: só agentes, registro e cache de repositórios; o painel responde 403 nela",
				"http_listen", cfg.HTTPListenAddr)
		}
	} else {
		logger.Warn("sem TLS: a senha e o cookie do painel trafegam em texto claro; mantenha a porta restrita a 127.0.0.1 ou ligue o TLS (PATCHD_TLS_CERT_FILE)")
	}

	// Banco primeiro: conecta, aplica as migrations e só então a porta é aberta.
	st, closeStore, err := openStore(ctx, logger, cfg.DatabaseURL)
	if err != nil {
		logger.Error("armazenamento indisponível", "error", err)
		return exitRuntime
	}
	defer closeStore()

	// Parâmetros pela tela (Aula 19.2): a tabela por cima do .env. Uma linha inválida fica de
	// fora (aviso no log) e o .env vale.
	ps, ok := st.(settings.Store)
	if !ok {
		logger.Error("parâmetros da tela: o armazenamento não guarda parâmetros")
		return exitRuntime
	}
	params, err := newSettings(ctx, look, ps, logger)
	if err != nil {
		logger.Error("parâmetros da tela", "error", err)
		return exitRuntime
	}
	go params.Loop(ctx, settingsRefresh)
	var apiOpts []api.Option
	apiOpts = append(apiOpts, api.WithSettings(params))
	if t, _ := look("PATCHD_PANEL_TITLE"); t != "" {
		// Nome no topo do painel (Aula 11.3). Aula 18.2: aspas que vieram do .env (o Compose
		// passa o valor como está escrito) saem; um nome comprido demais é avisado, não
		// ignorado em silêncio.
		if title, ok := panelTitle(t); ok {
			apiOpts = append(apiOpts, api.WithBrand(title))
		} else {
			logger.Warn("PATCHD_PANEL_TITLE ignorado: use de 1 a 40 caracteres; o painel fica com \"patchd\"", "caracteres", len([]rune(title)))
		}
	}
	if tz, _ := look("PATCHD_TIMEZONE"); tz != "" {
		apiOpts = append(apiOpts, api.WithTimezone(tz)) // sugerido no formulário da janela (Aula 11.1)
	}
	if n := countScreen(params); n > 0 {
		logger.Info("parâmetros da tela valendo no lugar do .env (Configuração › Parâmetros; patchd-server settings list)", "quantos", n)
	}
	// Métricas (Aula 10.2): registradas só com a porta configurada.
	var reg *metrics.Registry
	var retDeleted *metrics.CounterVec
	if cfg.MetricsListenAddr != "" {
		reg = metrics.NewRegistry()
		apiOpts = append(apiOpts, api.WithMetrics(api.NewMetrics(reg)))
		retDeleted = reg.NewCounter("patchd_retencao_apagados_total", "Linhas antigas apagadas pela retenção, por tabela (Aula 10.5).", "tabela")
	}
	// Login pelo AD (Aula 7.3): a configuração já foi validada em loadServerConfig.
	adCfg, adOn, _ := adConfig(look)
	adCfg.Groups = adGroups(look, params.Value) // Aula 19.4: os grupos da tela, lidos a cada login
	if adOn {
		dir, err := ad.New(adCfg)
		if err != nil {
			logger.Error("login pelo AD", "error", err)
			return exitConfig
		}
		apiOpts = append(apiOpts, api.WithDirectory(dir), api.WithADInfo(adInfo(adCfg, dir)))
		// Sem o servidor, o domínio e os grupos: esses valores ficam no .env.
		logger.Info("login pelo AD ligado (LDAPS, bind como o próprio usuário)",
			"grupos_admin", len(must(adCfg.GroupLists())), "grupos_leitura", len(second(adCfg.GroupLists())), "base_explicita", adCfg.BaseDN != "")
	} else {
		logger.Info("login pelo AD desligado (sem PATCHD_LDAP_HOST): o painel aceita só usuários locais")
	}
	// Login pelo RHBK (Aula 9.4), ao lado do AD e dos usuários locais.
	oidcCfg, oidcOn, err := oidcConfig(look, cfg.PublicURL, true)
	if err != nil {
		logger.Error("login pelo RHBK", "error", err)
		return exitConfig
	}
	if oidcOn {
		oidcCfg.Roles = oidcRoles(look, params.Value) // Aula 19.4
		prov, err := oidc.New(oidcCfg, nil)
		if err != nil {
			logger.Error("login pelo RHBK", "error", err)
			return exitConfig
		}
		apiOpts = append(apiOpts, api.WithOIDC(prov))
		logger.Info("login pelo RHBK ligado (código de autorização com PKCE; perfil pelo claim de grupos)",
			"issuer", oidcCfg.Issuer, "client_id", oidcCfg.ClientID, "claim", oidcCfg.RolesClaim,
			"papeis_admin", len(must(oidcCfg.Roles())), "papeis_leitura", len(second(oidcCfg.Roles())), "retorno", oidcCfg.RedirectURL)
		go checkOIDC(ctx, logger, prov)
	}
	checkPanelUsers(ctx, logger, st, adOn)
	// Jobs pelo painel (Aula 8.2): com a chave de jobs, o servidor assina os jobs que o
	// admin pede. Chave configurada e ilegível: o servidor não sobe (erro de configuração).
	jobKey, err := jobSigningKey(look)
	if err != nil {
		logger.Error("chave de jobs", "error", err)
		return exitConfig
	}
	if jobKey != nil {
		apiOpts = append(apiOpts, api.WithJobKey(jobKey))
		logger.Info("jobs pelo painel ligados: o servidor assina os jobs pedidos pelo admin",
			"key_id", jobs.KeyID(jobKey.Public().(ed25519.PublicKey)))
	} else {
		logger.Info("jobs pelo painel desligados (sem PATCHD_JOB_SIGNING_KEY_FILE): crie jobs com patchd-server job create")
	}
	discoverySecret, err := config.Secret(look, "PATCHD_DISCOVERY_KEY")
	if err != nil {
		logger.Error("ler chave de descoberta")
		return exitConfig
	}
	if discoverySecret != "" {
		key, keyErr := machinebox.ParsePrivate(discoverySecret)
		if keyErr != nil {
			logger.Error("chave de descoberta inválida")
			return exitConfig
		}
		apiOpts = append(apiOpts, api.WithDiscoveryKey(key))
	}
	// Guarda da senha da BIOS (Aula 21.4): com a chave, o painel sorteia, guarda e revela.
	// Chave configurada e ilegível: o servidor não sobe.
	escrow, err := biosEscrowKey(look)
	if err != nil {
		logger.Error("guarda da senha da BIOS", "error", err)
		return exitConfig
	}
	if escrow != nil {
		apiOpts = append(apiOpts, api.WithBIOSEscrow(escrow))
		pub, _ := machinebox.Public(escrow)
		logger.Info("guarda da senha da BIOS ligada: o painel sorteia, guarda e revela (só admin, com motivo)",
			"chave_guarda", machinebox.KeyID(pub))
	} else {
		logger.Info("guarda da senha da BIOS desligada (sem PATCHD_BIOS_ESCROW_KEY_FILE)")
	}
	// Troca dos agentes para HTTPS (Aula 9.2): a URL vai, assinada, no job server_url.
	if cfg.PublicURL != "" {
		apiOpts = append(apiOpts, api.WithPublicURL(cfg.PublicURL))
		logger.Info("URL pública anunciada nos jobs server_url (botão \"Passar para HTTPS\" no painel)", "public_url", cfg.PublicURL)
	}
	// Notificações por e-mail (Aula 18.5): configuração errada no .env não deixa o servidor
	// subir (melhor do que descobrir no dia em que o aviso não chegou). Aula 19.3: o que vale é
	// a tela por cima do .env, lido a cada uso; a tela confere o conjunto antes de gravar.
	if _, err := newMailer(look); err != nil {
		logger.Error("notificações por e-mail", "error", err)
		return exitConfig
	}
	if _, err := notifyKinds(look); err != nil {
		logger.Error("notificações por e-mail", "error", err)
		return exitConfig
	}
	live := newLiveNotifier(params.Lookup)
	apiOpts = append(apiOpts, api.WithNotifier(live))
	switch m, kinds, err := live.current(); {
	case err != nil:
		logger.Warn("notificações por e-mail paradas: a configuração da tela não fecha; corrija em Configuração › Parâmetros", "error", err)
	case m != nil:
		logger.Info("notificações por e-mail ligadas: "+m.Describe(), "avisos", strings.Join(kinds, ", "))
	default:
		logger.Info("notificações por e-mail desligadas (sem servidor SMTP no .env nem em Configuração › Parâmetros)")
	}
	// Avisos automáticos (Aula 18.6): só com o PostgreSQL (as chaves mandadas ficam nele). O
	// laço roda sempre e pula a passada enquanto as notificações estiverem desligadas.
	if pg, ok := st.(*store.Postgres); ok {
		go notifyLoop(ctx, logger, pg, live, &alerts.Watcher{Store: pg, PublicURL: cfg.PublicURL, Logger: logger, Now: time.Now})
	}
	retention := liveRetention(params, cfg.Retention)
	if p, ok := st.(pruner); ok {
		logger.Info("retenção: " + describeRetention(retention()) + "; aplicada de hora em hora (patchd-server retention mostra antes)")
		go retentionLoop(ctx, logger, p, retention, retDeleted)
	}
	if pg, ok := st.(*store.Postgres); ok { // sem banco não há catálogo
		srcs := newCatalogSources(look)
		if srcs.ContentDirErr != nil {
			logger.Error("o wsusscn2.cab fica fora do agendamento", "error", srcs.ContentDirErr)
		}
		go catalogLoop(ctx, logger, pg, srcs, liveCatalogInterval(params), firstCatalogRun(), params.Changed())
	}

	// Campanhas (Aula 8.5): o servidor libera os anéis sozinho; precisa da chave de jobs.
	switch pg, ok := st.(*store.Postgres); {
	case !ok:
	case jobKey == nil:
		logger.Info("campanhas paradas (sem PATCHD_JOB_SIGNING_KEY_FILE): nenhum anel é liberado")
	default:
		var tp campaign.ThirdPartyPlanner
		switch c, err := newCampaigner(pg, look); {
		case err != nil:
			logger.Error("campanhas de terceiros paradas (pausam no primeiro anel)", "error", err)
		default:
			tp = c
		}
		go campaignLoop(ctx, logger, pg, newCampaignEngine(pg, jobKey, tp, logger))
		logger.Info("campanhas ligadas: o servidor libera os anéis a cada minuto")
	}

	// Estado de compliance (RF-23, Aula 7.4): avaliado contra o catálogo do mesmo banco.
	var tpRules *thirdparty.Manifest // Aula 23.3: as mesmas regras para o download pelo painel
	if pg, ok := st.(*store.Postgres); ok {
		cs := newComplianceService(pg, logger)
		// Programas de terceiros no painel (Aula 13.3): o mesmo manifesto da linha de comando.
		path, _ := look("PATCHD_THIRDPARTY_MANIFEST")
		manifest, err := thirdparty.Load(path)
		if err != nil {
			logger.Error("manifesto dos programas de terceiros", "error", err)
			return exitConfig
		}
		cs.manifest = &manifest
		apiOpts = append(apiOpts, api.WithCompliance(cs))
		// O mesmo manifesto confere, no inventário, o efeito do job thirdparty (Aula 14.4).
		apiOpts = append(apiOpts, api.WithThirdPartyManifest(&manifest))
		tpRules = &manifest
	}
	// O catálogo offline do Windows é distribuído pelo servidor que o baixa (o mesmo banco
	// e a mesma pasta), mesmo com o agendamento desligado (o catalog sync manual também baixa).
	if pg, ok := st.(*store.Postgres); ok {
		switch dir, err := contentDir(look); {
		case err != nil:
			logger.Error("catálogo offline não distribuído", "error", err)
		case dir != "":
			apiOpts = append(apiOpts, api.WithContent(dir, pg))
			logger.Info("catálogo offline do Windows distribuído aos agentes", "content_dir", dir)
			// Aula 23.3: o painel baixa instaladores de terceiros, pelo mesmo caminho do
			// package fetch (o mesmo winget, a mesma conferência do SHA-256, a mesma pasta).
			if tpRules != nil {
				apiOpts = append(apiOpts, api.WithPackageFetch(panelFetcher(pg, packagesDir(dir), info.Version)))
			}
		}
	}
	if cfg.ReleasesDir != "" {
		apiOpts = append(apiOpts, api.WithReleases(release.Dir(cfg.ReleasesDir), info.Version))
		logReleases(logger, release.Dir(cfg.ReleasesDir), info.Version)
	}
	// Configuração › Sistema e › Agente (Aula 15.2).
	si := systemInfo(info, cfg, st, certs)
	si.Live = liveSystem(params, si.Database, retention) // Aula 19.2: o que a tela muda aparece na hora
	apiOpts = append(apiOpts, api.WithSystemInfo(si))
	if path, _ := look("PATCHD_AGENT_CA_FILE"); path != "" {
		pem, n, err := loadAgentCA(path)
		if err != nil {
			logger.Error("PATCHD_AGENT_CA_FILE", "error", err)
			return exitConfig
		}
		apiOpts = append(apiOpts, api.WithAgentCA(pem))
		logger.Info("CA do servidor oferecida em Configuração › Agente", "certificados", n)
	}
	var repo http.Handler
	if cfg.RepoCacheDir != "" {
		c, err := repocache.New(cfg.RepoCacheDir, cfg.RepoCacheHosts, "patchd-server/"+info.Version, logger)
		if err != nil {
			logger.Error("cache de repositórios", "error", err)
			return exitConfig
		}
		repo = c
		limits, err := repoCacheLimits(look)
		if err != nil {
			logger.Error("limpeza do cache de repositórios", "error", err)
			return exitConfig
		}
		go pruneRepoCacheLoop(ctx, logger, c, limits)
		// Aula 19.2: a URL anunciada muda pela tela; o anúncio é montado a cada check-in.
		ann := liveRepoCache(params, cfg.RepoCacheHosts)
		apiOpts = append(apiOpts, api.WithRepoCacheLive(ann))
		if a := ann(); a != nil {
			logger.Info("cache de repositórios anunciado aos agentes Linux no check-in", "url", a.URL)
		} else {
			logger.Info("cache de repositórios sem anúncio: defina a URL em Configuração › Parâmetros (ou PATCHD_REPO_CACHE_URL) para os agentes Linux se configurarem")
		}
		logger.Info("cache de repositórios ligado (apt: proxy HTTP nesta porta; Fedora: /repo/fedora/; contagens: /repo/stats)",
			"dir", cfg.RepoCacheDir, "hosts", cfg.RepoCacheHosts)
	}
	handler := newHandler(api.New(st, logger, apiOpts...), repo)
	ls := []listener{{name: "API", addr: cfg.ListenAddr, handler: handler}}
	if certs != nil {
		ls[0].handler, ls[0].tls = withHSTS(handler), tlsConfig(certs)
		if cfg.HTTPListenAddr != "" {
			ls = append(ls, listener{name: "HTTP de transição", addr: cfg.HTTPListenAddr, handler: transitionHandler(handler, logger, cfg.HTTPCacheOnly)})
		}
	}
	if reg != nil {
		var snap *snapshotter
		if pg, ok := st.(*store.Postgres); ok {
			snap = newSnapshotter(pg, newComplianceService(pg, logger), logger)
		}
		var exp certExpiry
		if certs != nil {
			exp = certs
		}
		registerGauges(reg, snap, exp, info, time.Now())
		m := listener{name: "Métricas", addr: cfg.MetricsListenAddr, handler: reg.Handler(cfg.MetricsToken)}
		if certs != nil {
			// Com TLS, o token também não trafega em claro.
			m.tls = tlsConfig(certs)
		}
		ls = append(ls, m)
		logger.Info("métricas ligadas: GET /metrics com o token (Bearer), na porta só delas",
			"metrics_listen", cfg.MetricsListenAddr, "tls", certs != nil)
	} else {
		logger.Info("métricas desligadas (sem PATCHD_METRICS_LISTEN_ADDR)")
	}
	if err := serve(ctx, logger, ls); err != nil {
		// Para os laços (catálogo, campanhas) antes de o banco fechar: sem isso, um laço
		// no meio do passo registra "closed pool" (Aula 8.5b, porta já ocupada).
		stop()
		logger.Error("servidor parou com erro", "error", err)
		return exitRuntime
	}
	logger.Info("servidor encerrado")
	return exitOK
}
