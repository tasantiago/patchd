package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// TLS do servidor (Aula 9.1). O certificado vem de arquivos (a CA interna emite; o patchd
// não gera certificado de produção) e é relido quando muda no disco: a renovação não pede
// reinício. A porta HTTP de transição, opcional, atende só o que os agentes antigos e o
// cache de repositórios precisam enquanto migram para HTTPS (Aula 9.2); o painel e a API
// de leitura nunca respondem sem TLS.

const (
	certCheckEvery = 30 * time.Second
	certWarnBefore = 30 * 24 * time.Hour
	// hstsValue: o navegador passa a recusar HTTP para este endereço por 180 dias.
	hstsValue = "max-age=15552000"
)

// certReloader entrega o certificado ao TLS e o relê quando o arquivo muda.
type certReloader struct {
	certFile, keyFile string
	logger            *slog.Logger
	now               func() time.Time

	mu      sync.Mutex
	cert    *tls.Certificate
	modCert time.Time
	modKey  time.Time
	checked time.Time
}

// newCertReloader carrega o par agora: certificado ilegível é erro de configuração.
func newCertReloader(certFile, keyFile string, logger *slog.Logger) (*certReloader, error) {
	c := &certReloader{certFile: certFile, keyFile: keyFile, logger: logger, now: time.Now}
	if err := c.load(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *certReloader) load() error {
	sc, err := os.Stat(c.certFile)
	if err != nil {
		return fmt.Errorf("certificado TLS: %w", err)
	}
	sk, err := os.Stat(c.keyFile)
	if err != nil {
		return fmt.Errorf("chave TLS: %w", err)
	}
	pair, err := tls.LoadX509KeyPair(c.certFile, c.keyFile)
	if err != nil {
		return fmt.Errorf("par TLS inválido (certificado e chave não combinam, ou PEM ilegível): %w", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return fmt.Errorf("certificado TLS: %w", err)
	}
	pair.Leaf = leaf
	c.cert, c.modCert, c.modKey = &pair, sc.ModTime(), sk.ModTime()
	return nil
}

// Info resume o certificado para o log: validade e quantos nomes, nunca os nomes (são do
// ambiente e ficam fora dos logs que se colam em chamados).
func (c *certReloader) Info() (expires time.Time, names int, chain int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	leaf := c.cert.Leaf
	return leaf.NotAfter, len(leaf.DNSNames) + len(leaf.IPAddresses), len(c.cert.Certificate)
}

// GetCertificate é o gancho do crypto/tls. Confere o disco no máximo a cada 30 s; um par
// novo ilegível (arquivo pela metade durante a cópia) é ignorado e o anterior continua.
func (c *certReloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if now.Sub(c.checked) >= certCheckEvery {
		c.checked = now
		sc, err1 := os.Stat(c.certFile)
		sk, err2 := os.Stat(c.keyFile)
		if err1 == nil && err2 == nil && (!sc.ModTime().Equal(c.modCert) || !sk.ModTime().Equal(c.modKey)) {
			old := c.cert
			if err := c.load(); err != nil {
				c.cert = old
				c.logger.Error("certificado TLS novo ilegível; o anterior continua", "error", err)
			} else {
				c.logger.Info("certificado TLS relido do disco", "expira_em_dias", int(time.Until(c.cert.Leaf.NotAfter).Hours()/24))
			}
		}
	}
	return c.cert, nil
}

// tlsConfig: TLS 1.2 ou mais novo; as suítes são as padrão do Go (só AEAD com sigilo
// direto no 1.2; o 1.3 não deixa escolher).
func tlsConfig(c *certReloader) *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: c.GetCertificate}
}

// withHSTS acrescenta o Strict-Transport-Security nas respostas que vieram por TLS.
func withHSTS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil {
			w.Header().Set("Strict-Transport-Security", hstsValue)
		}
		next.ServeHTTP(w, r)
	})
}

// transitionHandler é o que a porta HTTP de transição atende: o /healthz, o registro e as
// rotas do agente (/api/v1/enroll, /api/v1/agent/...) e o cache de repositórios (proxy do
// apt e /repo/, conteúdo assinado pelas distribuições). O resto (painel, API de leitura)
// recebe 403 com o motivo: senha e cookie nunca passam sem TLS.
func transitionHandler(full http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		allowed := p == "/healthz" || p == "/api/v1/enroll" || strings.HasPrefix(p, "/api/v1/agent/") ||
			strings.HasPrefix(p, "/repo/") || r.Method == http.MethodConnect || r.URL.IsAbs()
		if !allowed {
			http.Error(w, "esta porta é só para agentes em transição e para o cache de repositórios; o painel e a API abrem por HTTPS",
				http.StatusForbidden)
			return
		}
		full.ServeHTTP(w, r)
	})
}

// errTLSPair: um sem o outro.
var errTLSPair = errors.New("-tls-cert e -tls-key (PATCHD_TLS_CERT_FILE e PATCHD_TLS_KEY_FILE) vão juntos")
