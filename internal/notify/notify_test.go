package notify

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"io"
	"math/big"
	"mime"
	"mime/quotedprintable"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// servidorSMTP é um SMTP de mentira: guarda o envelope e a mensagem; com cert, oferece STARTTLS.
type servidorSMTP struct {
	ln   net.Listener
	cert *tls.Certificate
	auth bool // exige AUTH PLAIN
	mu   sync.Mutex
	from string
	to   []string
	data string
	tls  bool
	user string
}

func novoSMTP(t *testing.T, cert *tls.Certificate, auth bool) *servidorSMTP {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &servidorSMTP{ln: ln, cert: cert, auth: auth}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.atende(c)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return s
}

func (s *servidorSMTP) atende(c net.Conn) {
	defer c.Close()
	r, w := bufio.NewReader(c), c
	say := func(l string) { _, _ = io.WriteString(w, l+"\r\n") }
	say("220 teste ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.TrimSpace(line)
		up := strings.ToUpper(cmd)
		switch {
		case strings.HasPrefix(up, "EHLO"):
			say("250-teste")
			if s.cert != nil && !s.tlsOn() {
				say("250-STARTTLS")
			}
			if s.auth {
				say("250-AUTH PLAIN")
			}
			say("250 OK")
		case up == "STARTTLS":
			say("220 vai")
			tc := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{*s.cert}})
			if err := tc.Handshake(); err != nil {
				return
			}
			c, r, w = tc, bufio.NewReader(tc), tc
			s.mu.Lock()
			s.tls = true
			s.mu.Unlock()
		case strings.HasPrefix(up, "AUTH PLAIN"):
			raw, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(cmd[len("AUTH PLAIN"):]))
			parts := strings.Split(string(raw), "\x00")
			s.mu.Lock()
			s.user = parts[1]
			s.mu.Unlock()
			if parts[2] != "segredo" {
				say("535 senha errada")
				continue
			}
			say("235 ok")
		case strings.HasPrefix(up, "MAIL FROM:"):
			s.mu.Lock()
			s.from = cmd[len("MAIL FROM:"):]
			s.mu.Unlock()
			say("250 OK")
		case strings.HasPrefix(up, "RCPT TO:"):
			s.mu.Lock()
			s.to = append(s.to, cmd[len("RCPT TO:"):])
			s.mu.Unlock()
			say("250 OK")
		case up == "DATA":
			say("354 manda")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			s.mu.Lock()
			s.data = b.String()
			s.mu.Unlock()
			say("250 aceito")
		case up == "QUIT":
			say("221 tchau")
			return
		default:
			say("502 ?")
		}
	}
}

func (s *servidorSMTP) tlsOn() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.tls }

func certDeTeste(t *testing.T) (*tls.Certificate, *x509.CertPool) {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "127.0.0.1"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(c)
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: k}, pool
}

func TestConfiguracao(t *testing.T) {
	ok := Config{Addr: "smtp.exemplo:587", From: "patchd <patchd@exemplo.jus.br>", To: []string{"ti@exemplo.jus.br"}}
	if _, err := New(ok); err != nil {
		t.Fatal(err)
	}
	for nome, c := range map[string]Config{
		"sem porta":      {Addr: "smtp.exemplo", From: ok.From, To: ok.To},
		"tls estranho":   {Addr: ok.Addr, From: ok.From, To: ok.To, TLS: "ssl"},
		"senha sem tls":  {Addr: ok.Addr, From: ok.From, To: ok.To, TLS: TLSNone, User: "u", Password: "p"},
		"sem destino":    {Addr: ok.Addr, From: ok.From},
		"destino ruim":   {Addr: ok.Addr, From: ok.From, To: []string{"não é e-mail"}},
		"remetente ruim": {Addr: ok.Addr, From: "x", To: ok.To},
	} {
		if _, err := New(c); err == nil {
			t.Errorf("%s: aceito", nome)
		}
	}
	m, _ := New(Config{Addr: ok.Addr, From: ok.From, To: ok.To, User: "patchd", Password: "segredo"})
	if d := m.Describe(); strings.Contains(d, "segredo") || !strings.Contains(d, "starttls com usuário") || !strings.Contains(d, "1 destinatário") {
		t.Errorf("descrição: %q", d)
	}
	if (*Mailer)(nil).Describe() != "desligadas" {
		t.Error("nil")
	}
}

func TestEnvioComStartTLS(t *testing.T) {
	cert, pool := certDeTeste(t)
	s := novoSMTP(t, cert, true)
	m, err := New(Config{Addr: s.ln.Addr().String(), From: "patchd <patchd@exemplo.jus.br>", To: []string{"ti@exemplo.jus.br", "Fulano <fulano@exemplo.jus.br>"},
		User: "patchd", Password: "segredo", RootCAs: pool})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.Send(ctx, "Pedido\r\nBcc: intruso@x esperando a TI", "1 pedido aguarda aprovação.\nAbra: https://patchd.exemplo/painel/autoatendimento\n.\nlinha com ponto"); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.tls || s.user != "patchd" || s.from != "<patchd@exemplo.jus.br>" || len(s.to) != 2 || s.to[1] != "<fulano@exemplo.jus.br>" {
		t.Fatalf("envelope: tls=%v user=%q from=%q to=%v", s.tls, s.user, s.from, s.to)
	}
	head, body, _ := strings.Cut(s.data, "\r\n\r\n")
	if strings.Contains(head, "\r\nBcc:") || !strings.Contains(head, "Subject: [patchd] Pedido Bcc: intruso@x esperando a TI\r\n") || !strings.Contains(head, "Auto-Submitted: auto-generated") {
		t.Errorf("cabeçalhos (sem injeção):\n%q\n%q", head, s.data)
	}
	plain, _ := io.ReadAll(quotedprintable.NewReader(strings.NewReader(strings.ReplaceAll(body, "\r\n..", "\r\n."))))
	if !strings.Contains(string(plain), "1 pedido aguarda aprovação.") || !strings.Contains(string(plain), "\r\n.\r\nlinha com ponto") {
		t.Errorf("corpo:\n%s", plain)
	}
}

func TestSemStartTLS(t *testing.T) {
	s := novoSMTP(t, nil, false) // relay sem TLS
	cfg := Config{Addr: s.ln.Addr().String(), From: "patchd@exemplo.jus.br", To: []string{"ti@exemplo.jus.br"}}
	m, _ := New(cfg)
	ctx := context.Background()
	if err := m.Send(ctx, "teste", "x"); err == nil || !strings.Contains(err.Error(), "não oferece STARTTLS") {
		t.Errorf("starttls exigido: %v", err)
	}
	cfg.TLS = TLSNone
	m, _ = New(cfg)
	if err := m.Send(ctx, "teste", "x"); err != nil {
		t.Errorf("relay sem TLS, com none: %v", err)
	}
	cert, _ := certDeTeste(t)
	s2 := novoSMTP(t, cert, false)
	m, _ = New(Config{Addr: s2.ln.Addr().String(), From: "patchd@exemplo.jus.br", To: []string{"ti@exemplo.jus.br"}})
	if err := m.Send(ctx, "teste", "x"); err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Errorf("certificado fora das CAs aceitas: %v", err)
	}
}

// Assunto com acento sai codificado (RFC 2047) e volta igual; o corpo, em quoted-printable.
func TestMensagemAcentuada(t *testing.T) {
	m, err := New(Config{Addr: "smtp.exemplo:587", From: "patchd@exemplo.jus.br", To: []string{"ti@exemplo.jus.br"}})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := m.message("3 pedidos aguardando aprovação", "Instalação do 7-Zip em máquina da SEAT.")
	if err != nil {
		t.Fatal(err)
	}
	head, body, _ := strings.Cut(string(msg), "\r\n\r\n")
	var subj string
	for _, l := range strings.Split(head, "\r\n") {
		if v, ok := strings.CutPrefix(l, "Subject: "); ok {
			subj = v
		}
	}
	if !strings.HasPrefix(subj, "=?utf-8?q?") {
		t.Fatalf("assunto sem codificação: %q", subj)
	}
	dec, err := new(mime.WordDecoder).DecodeHeader(subj)
	if err != nil || dec != "[patchd] 3 pedidos aguardando aprovação" {
		t.Errorf("assunto: %q %v", dec, err)
	}
	plain, _ := io.ReadAll(quotedprintable.NewReader(strings.NewReader(body)))
	if !strings.Contains(string(plain), "Instalação do 7-Zip") || !strings.Contains(string(plain), "Não responda.") {
		t.Errorf("corpo: %q", plain)
	}
	if _, err := m.message(" \r\n ", "x"); err == nil {
		t.Error("assunto vazio aceito")
	}
}
