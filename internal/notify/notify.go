// Package notify manda e-mail para a equipe (Aula 18.5): pelo servidor SMTP da instituição,
// com STARTTLS por padrão, para uma lista fixa de destinatários. É o canal das notificações
// que precisam de alguém (pedido esperando a TI, campanha pausada), que o painel sozinho não
// garante que alguém veja a tempo.
package notify

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

// Modos de TLS.
const (
	TLSStart = "starttls" // padrão: a conexão começa em texto e sobe para TLS (porta 587 ou 25)
	TLSWrap  = "tls"      // TLS desde o início (porta 465)
	TLSNone  = "none"     // sem TLS: só um relay interno que não oferece STARTTLS; sem senha
)

// Config é o servidor e a lista.
type Config struct {
	Addr     string // host:porta
	From     string
	To       []string
	TLS      string
	User     string
	Password string // segredo: nunca no log nem no painel
	RootCAs  *x509.CertPool
	Hello    string // nome no EHLO; vazio: o nome da máquina
	Timeout  time.Duration
}

// Validate confere a configuração.
func (c Config) Validate() error {
	host, port, err := net.SplitHostPort(c.Addr)
	switch {
	case err != nil || host == "" || port == "":
		return fmt.Errorf("PATCHD_SMTP_ADDR: use host:porta (recebido %q)", c.Addr)
	case c.TLS != TLSStart && c.TLS != TLSWrap && c.TLS != TLSNone:
		return fmt.Errorf("PATCHD_SMTP_TLS: use starttls, tls ou none (recebido %q)", c.TLS)
	case c.TLS == TLSNone && c.User != "":
		return errors.New("PATCHD_SMTP_TLS=none com usuário e senha: a senha iria em texto pela rede; use starttls ou tls")
	case len(c.To) == 0:
		return errors.New("PATCHD_NOTIFY_TO: informe ao menos um destinatário")
	}
	if _, err := mail.ParseAddress(c.From); err != nil {
		return fmt.Errorf("PATCHD_SMTP_FROM: endereço inválido (%v)", err)
	}
	for _, t := range c.To {
		if _, err := mail.ParseAddress(t); err != nil {
			return fmt.Errorf("PATCHD_NOTIFY_TO: %q não é um endereço válido", t)
		}
	}
	return nil
}

// Mailer manda as mensagens.
type Mailer struct {
	cfg Config
	now func() time.Time
}

// New monta o Mailer com a configuração conferida.
func New(c Config) (*Mailer, error) {
	if c.TLS == "" {
		c.TLS = TLSStart
	}
	if c.Timeout == 0 {
		c.Timeout = 30 * time.Second
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &Mailer{cfg: c, now: time.Now}, nil
}

// Describe resume a configuração, sem segredos: "smtp.exemplo:587 (starttls) · 2 destinatário(s)".
func (m *Mailer) Describe() string {
	if m == nil {
		return "desligadas"
	}
	auth := ""
	if m.cfg.User != "" {
		auth = " com usuário"
	}
	return fmt.Sprintf("%s (%s%s) · de %s · %d destinatário(s)", m.cfg.Addr, m.cfg.TLS, auth, m.cfg.From, len(m.cfg.To))
}

// Send manda a mensagem (texto) para a lista.
func (m *Mailer) Send(ctx context.Context, subject, body string) error {
	msg, err := m.message(subject, body)
	if err != nil {
		return err
	}
	host, _, _ := net.SplitHostPort(m.cfg.Addr)
	tlsCfg := &tls.Config{ServerName: host, RootCAs: m.cfg.RootCAs, MinVersion: tls.VersionTLS12}
	d := net.Dialer{Timeout: m.cfg.Timeout}
	var conn net.Conn
	if m.cfg.TLS == TLSWrap {
		conn, err = (&tls.Dialer{NetDialer: &d, Config: tlsCfg}).DialContext(ctx, "tcp", m.cfg.Addr)
	} else {
		conn, err = d.DialContext(ctx, "tcp", m.cfg.Addr)
	}
	if err != nil {
		return fmt.Errorf("conectar ao SMTP: %w", err)
	}
	deadline := time.Now().Add(m.cfg.Timeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("SMTP: %w", err)
	}
	defer c.Close()
	hello := m.cfg.Hello
	if hello == "" {
		hello = "patchd"
	}
	if err := c.Hello(hello); err != nil {
		return fmt.Errorf("SMTP EHLO: %w", err)
	}
	if m.cfg.TLS == TLSStart {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("o servidor SMTP não oferece STARTTLS (PATCHD_SMTP_TLS=none, se for um relay interno sem TLS)")
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("SMTP STARTTLS: %w", err)
		}
	}
	if m.cfg.User != "" {
		if err := c.Auth(smtp.PlainAuth("", m.cfg.User, m.cfg.Password, host)); err != nil {
			return fmt.Errorf("SMTP: autenticação recusada: %w", err)
		}
	}
	from, _ := mail.ParseAddress(m.cfg.From)
	if err := c.Mail(from.Address); err != nil {
		return fmt.Errorf("SMTP MAIL FROM: %w", err)
	}
	for _, t := range m.cfg.To {
		a, _ := mail.ParseAddress(t)
		if err := c.Rcpt(a.Address); err != nil {
			return fmt.Errorf("SMTP RCPT TO %s: %w", a.Address, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("SMTP DATA: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("SMTP DATA: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("SMTP: mensagem recusada: %w", err)
	}
	return c.Quit()
}

// message monta a mensagem: cabeçalhos em UTF-8 (assunto codificado), corpo em
// quoted-printable. Quebra de linha no assunto sai (sem injeção de cabeçalho).
func (m *Mailer) message(subject, body string) ([]byte, error) {
	subject = strings.Join(strings.Fields(subject), " ")
	if subject == "" {
		return nil, errors.New("assunto vazio")
	}
	id := make([]byte, 12)
	_, _ = rand.Read(id)
	from, _ := mail.ParseAddress(m.cfg.From)
	domain := "patchd"
	if i := strings.LastIndex(from.Address, "@"); i >= 0 {
		domain = from.Address[i+1:]
	}
	var to []string
	for _, t := range m.cfg.To {
		a, _ := mail.ParseAddress(t)
		to = append(to, a.String())
	}
	var b bytes.Buffer
	h := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	h("From", from.String())
	h("To", strings.Join(to, ", "))
	h("Subject", mime.QEncoding.Encode("utf-8", "[patchd] "+subject))
	h("Date", m.now().Format(time.RFC1123Z))
	h("Message-ID", "<"+hex.EncodeToString(id)+"@"+domain+">")
	h("MIME-Version", "1.0")
	h("Content-Type", "text/plain; charset=utf-8")
	h("Content-Transfer-Encoding", "quoted-printable")
	h("Auto-Submitted", "auto-generated") // resposta automática (férias) não volta
	b.WriteString("\r\n")
	qp := quotedprintable.NewWriter(&b)
	body = strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n")
	if _, err := qp.Write([]byte(body + "\r\n\r\n--\r\nMensagem automática do patchd. Não responda.\r\n")); err != nil {
		return nil, err
	}
	if err := qp.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
