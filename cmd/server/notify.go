package main

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/tasantiago/patchd/internal/alerts"
	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/notify"
	"github.com/tasantiago/patchd/internal/store"
)

// Notificações por e-mail (Aula 18.5): o servidor SMTP da instituição, a lista da equipe e o
// teste pelo terminal. Nenhum valor aparece no log além do resumo do Describe (sem senha).

const notifyUsage = `uso: patchd-server notify <comando>

Notificações por e-mail (Aula 18.5). A configuração vem do ambiente (.env) e, desde a Aula
19.3, de Configuração › Parâmetros, que vale por cima do .env (exceto a senha e a CA):
  PATCHD_SMTP_ADDR        host:porta do SMTP (vazio: notificações desligadas)
  PATCHD_SMTP_FROM        o remetente, ex.: patchd <patchd@exemplo>
  PATCHD_NOTIFY_TO        os destinatários, separados por vírgula
  PATCHD_SMTP_TLS         starttls (padrão), tls (porta 465) ou none (relay interno sem TLS)
  PATCHD_SMTP_USER        usuário, se o SMTP exigir login
  PATCHD_SMTP_PASSWORD    a senha (ou PATCHD_SMTP_PASSWORD_FILE, o arquivo com ela)
  PATCHD_SMTP_CA_FILE     PEM da CA do SMTP, se for uma CA interna
  PATCHD_NOTIFY_EVENTS    os avisos (Aula 18.6): pedidos, campanhas, fontes (vazio: todos)

comandos:
  status   mostra a configuração (sem a senha)
  test     manda uma mensagem de teste para a lista
  pending  mostra o que a próxima passada mandaria (Aula 18.6), sem mandar nem marcar
`

// notifyConfig lê a configuração. on=false: PATCHD_SMTP_ADDR vazio (desligado).
func notifyConfig(look config.Lookup) (notify.Config, bool, error) {
	get := func(k string) string { v, _ := look(k); return strings.TrimSpace(v) }
	c := notify.Config{Addr: get("PATCHD_SMTP_ADDR"), From: get("PATCHD_SMTP_FROM"),
		TLS: strings.ToLower(get("PATCHD_SMTP_TLS")), User: get("PATCHD_SMTP_USER")}
	if c.Addr == "" {
		return c, false, nil
	}
	for _, t := range strings.FieldsFunc(get("PATCHD_NOTIFY_TO"), func(r rune) bool { return r == ',' || r == ';' }) {
		if t = strings.TrimSpace(t); t != "" {
			c.To = append(c.To, t)
		}
	}
	// A senha só é lida com usuário: o compose monta o arquivo (vazio) mesmo para um relay
	// sem login.
	if c.User != "" {
		pw, err := config.Secret(look, "PATCHD_SMTP_PASSWORD")
		if err != nil {
			return c, true, err
		}
		if pw == "" {
			return c, true, fmt.Errorf("PATCHD_SMTP_USER sem PATCHD_SMTP_PASSWORD (ou PATCHD_SMTP_PASSWORD_FILE)")
		}
		c.Password = pw
	} else if pw, _ := look("PATCHD_SMTP_PASSWORD"); pw != "" {
		return c, true, fmt.Errorf("PATCHD_SMTP_PASSWORD sem PATCHD_SMTP_USER")
	}
	if f := get("PATCHD_SMTP_CA_FILE"); f != "" {
		pem, err := os.ReadFile(f)
		if err != nil {
			return c, true, fmt.Errorf("PATCHD_SMTP_CA_FILE: %w", err)
		}
		c.RootCAs = x509.NewCertPool()
		if !c.RootCAs.AppendCertsFromPEM(pem) {
			return c, true, fmt.Errorf("PATCHD_SMTP_CA_FILE: nenhum certificado PEM no arquivo")
		}
	}
	return c, true, nil
}

// newMailer: nil, sem erro, quando as notificações estão desligadas.
func newMailer(look config.Lookup) (*notify.Mailer, error) {
	c, on, err := notifyConfig(look)
	if err != nil || !on {
		return nil, err
	}
	return notify.New(c)
}

// testMessage é a mensagem do botão do painel e do notify test.
func testMessage(who, origin, publicURL string) (subject, body string) {
	body = fmt.Sprintf("Mensagem de teste pedida por %s (%s) em %s.\n\nSe chegou, as notificações do patchd chegam a esta lista.",
		who, origin, time.Now().UTC().Format("02/01/2006 15:04")+" UTC")
	if publicURL != "" {
		body += "\n\nPainel: " + strings.TrimRight(publicURL, "/") + "/painel/configuracao"
	}
	return "Teste de notificação", body
}

func runNotify(args []string, look config.Lookup, stdout, stderr io.Writer) int {
	if len(args) != 1 || (args[0] != "status" && args[0] != "test" && args[0] != "pending") {
		fmt.Fprint(stderr, notifyUsage)
		return exitConfig
	}
	// Aula 19.3: com banco, o que vale é a tela por cima do .env (o mesmo que o servidor usa).
	look, done := screenFromDB(look)
	defer done()
	m, err := newMailer(look)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: notificações: %v\n", err)
		return exitConfig
	}
	kinds, err := notifyKinds(look)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: notificações: %v\n", err)
		return exitConfig
	}
	if args[0] == "pending" {
		return notifyPending(look, kinds, stdout, stderr)
	}
	if m == nil {
		fmt.Fprintln(stdout, "notificações desligadas (sem PATCHD_SMTP_ADDR)")
		if args[0] == "test" {
			return exitConfig
		}
		return exitOK
	}
	fmt.Fprintln(stdout, "notificações:", m.Describe())
	if args[0] == "status" {
		fmt.Fprintln(stdout, "avisos:", strings.Join(kinds, ", "), "· a cada", notifyEvery)
		return exitOK
	}
	publicURL, _ := look("PATCHD_PUBLIC_URL")
	subject, body := testMessage(operator(look), cliSource, strings.TrimSpace(publicURL))
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := m.Send(ctx, subject, body); err != nil {
		fmt.Fprintf(stderr, "patchd-server: o teste falhou: %v\n", err)
		return exitRuntime
	}
	fmt.Fprintln(stdout, "mensagem de teste aceita pelo SMTP; confira a caixa da lista (e a pasta de spam)")
	return exitOK
}

// Avisos automáticos (Aula 18.6).

// notifyEvery: o intervalo das passadas. Pedido esperando a TI é o caso que tem pressa.
const notifyEvery = 5 * time.Minute

// notifyKinds lê PATCHD_NOTIFY_EVENTS.
func notifyKinds(look config.Lookup) ([]string, error) {
	v, _ := look("PATCHD_NOTIFY_EVENTS")
	return alerts.ParseKinds(v)
}

// liveNotifier (Aula 19.3): as notificações como valem agora, com a tela por cima do .env. O
// Mailer é remontado quando a configuração muda (endereço, TLS, usuário, remetente, lista,
// avisos, a senha do arquivo ou a CA). Serve ao painel (api.Notifier) e ao laço dos avisos.
type liveNotifier struct {
	look config.Lookup

	mu    sync.Mutex
	key   string
	m     *notify.Mailer
	kinds []string
	err   error
}

func newLiveNotifier(look config.Lookup) *liveNotifier { return &liveNotifier{look: look} }

// current: o Mailer (nil: desligadas ou configuração com erro), os avisos e o erro.
func (l *liveNotifier) current() (*notify.Mailer, []string, error) {
	c, on, cerr := notifyConfig(l.look)
	kinds, kerr := notifyKinds(l.look)
	sum := sha256.Sum256([]byte(c.User + "\x00" + c.Password))
	key := fmt.Sprintf("%t|%v|%s|%s|%s|%s|%x|%t|%v|%v", on, cerr, c.Addr, c.TLS, c.From, strings.Join(c.To, ","), sum[:8],
		c.RootCAs != nil, kinds, kerr)
	l.mu.Lock()
	defer l.mu.Unlock()
	if key == l.key {
		return l.m, l.kinds, l.err
	}
	l.key, l.m, l.kinds, l.err = key, nil, kinds, errors.Join(cerr, kerr)
	if l.err == nil && on {
		l.m, l.err = notify.New(c)
	}
	return l.m, l.kinds, l.err
}

// Enabled: há para onde mandar.
func (l *liveNotifier) Enabled() bool {
	m, _, err := l.current()
	return m != nil && err == nil
}

// Describe: o resumo da aba Sistema, com os avisos ligados.
func (l *liveNotifier) Describe() string {
	m, kinds, err := l.current()
	switch {
	case err != nil:
		return "com erro na configuração: " + err.Error()
	case m == nil:
		return "desligadas"
	}
	return m.Describe() + " · avisos: " + strings.Join(kinds, ", ")
}

// Send manda pela configuração de agora.
func (l *liveNotifier) Send(ctx context.Context, subject, body string) error {
	m, _, err := l.current()
	switch {
	case err != nil:
		return err
	case m == nil:
		return errors.New("as notificações estão desligadas")
	}
	return m.Send(ctx, subject, body)
}

// notifyLoop faz uma passada a cada notifyEvery, com a trava (dois servidores no mesmo banco
// não mandam o mesmo aviso duas vezes). Erro igual ao anterior não se repete no log. Aula
// 19.3: a configuração é lida a cada passada (muda pela tela); desligadas, a passada pula.
func notifyLoop(ctx context.Context, logger *slog.Logger, lock campaignLocker, live *liveNotifier, w *alerts.Watcher) {
	t := time.NewTicker(notifyEvery)
	defer t.Stop()
	last, state := "", ""
	for {
		m, kinds, cerr := live.current()
		now := "ligadas"
		switch {
		case cerr != nil:
			now = "erro: " + cerr.Error()
		case m == nil:
			now = "desligadas"
		}
		if now != state {
			switch {
			case cerr != nil:
				logger.Warn("avisos por e-mail parados: a configuração das notificações tem erro", "error", cerr)
			case m == nil && state != "":
				logger.Info("avisos por e-mail desligados (sem servidor SMTP)")
			case m != nil && state != "":
				logger.Info("avisos por e-mail: configuração nova: "+m.Describe(), "avisos", strings.Join(kinds, ", "))
			}
			state = now
		}
		if m != nil && cerr == nil {
			w.Mail, w.Kinds = m, kinds
			ok, err := lock.WithAdvisoryLock(ctx, store.LockNotify, func(ctx context.Context) error {
				_, err := w.Step(ctx)
				return err
			})
			switch {
			case err != nil && ctx.Err() == nil:
				if err.Error() != last {
					logger.Warn("notificações: a passada falhou; os avisos não mandados saem na próxima", "error", err)
				}
				last = err.Error()
			case err == nil && ok && last != "":
				logger.Info("notificações: a passada voltou a funcionar")
				last = ""
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// notifyPending: patchd-server notify pending.
func notifyPending(look config.Lookup, kinds []string, stdout, stderr io.Writer) int {
	cfg, err := loadServerConfig(nil, look, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: configuração inválida:\n%v\n", err)
		return exitConfig
	}
	if cfg.DatabaseURL == "" {
		fmt.Fprintln(stderr, "patchd-server: os avisos exigem o PostgreSQL (PATCHD_DATABASE_URL ou PATCHD_DATABASE_URL_FILE)")
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
	publicURL, _ := look("PATCHD_PUBLIC_URL")
	return printPending(ctx, &alerts.Watcher{Store: store.NewPostgres(pool), Kinds: kinds, PublicURL: strings.TrimSpace(publicURL),
		Now: time.Now}, stdout, stderr)
}

func printPending(ctx context.Context, w *alerts.Watcher, stdout, stderr io.Writer) int {
	l, err := w.Preview(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	if len(l) == 0 {
		fmt.Fprintln(stdout, "nada novo: a próxima passada não manda e-mail")
		return exitOK
	}
	for i, m := range l {
		if i > 0 {
			fmt.Fprintln(stdout)
		}
		fmt.Fprintf(stdout, "== [%s] [patchd] %s\n%s\n", m.Kind, m.Subject, m.Body)
	}
	return exitOK
}
