package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/tasantiago/patchd/internal/repocache"
)

// Tempos do servidor HTTP.
const (
	// Limita clientes que enviam os cabeçalhos devagar para prender conexões (slowloris).
	readHeaderTimeout = 5 * time.Second
	// Prazo para ler a requisição inteira e para escrever a resposta.
	readTimeout  = 60 * time.Second
	writeTimeout = 60 * time.Second
	// Conexões ociosas mantidas abertas (keep-alive) são fechadas depois disso.
	idleTimeout = 120 * time.Second
	// Tempo para concluir as requisições em andamento ao receber o sinal de encerramento.
	shutdownTimeout = 10 * time.Second
	// Tempo máximo da verificação feita pelo modo -healthcheck.
	healthcheckTimeout = 3 * time.Second
)

// newHandler monta as rotas: /healthz (fora do log de acesso, chamado a cada 15 s pelo
// Docker), a API em /api/ e o painel em /painel/ (a raiz leva ao painel).
//
// Pedidos de proxy (forma absoluta, "GET http://archive.ubuntu.com/..."; ou CONNECT) vão
// para o cache de repositórios (Aula 6.6, parte 4b) quando ligado, e são recusados quando
// não: sem essa separação, o ServeMux casaria só o caminho e um
// "GET http://qualquer/api/..." cairia na API. Com o cache ligado, /repo/ (o Fedora e
// /repo/stats, parte 4c) também é dele.
func newHandler(apiHandler, repoCache http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "ok")
	})
	mux.Handle("/api/", apiHandler)
	mux.Handle("/painel/", apiHandler)
	mux.Handle("GET /{$}", http.RedirectHandler("/painel/", http.StatusFound))
	if repoCache != nil {
		// Fedora (baseurl apontando para o servidor) e as contagens do cache.
		mux.Handle("/repo/", repoCache)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !repocache.IsProxyRequest(r) {
			mux.ServeHTTP(w, r)
			return
		}
		if repoCache == nil {
			http.Error(w, "o cache de repositórios está desligado (PATCHD_REPO_CACHE_DIR)", http.StatusForbidden)
			return
		}
		repoCache.ServeHTTP(w, r)
	})
}

// serve atende HTTP em addr até ctx ser cancelado (SIGTERM ou SIGINT) e então encerra
// de forma limpa, esperando as requisições em andamento por até shutdownTimeout.
// listener é uma porta do servidor: o endereço, o handler e, para HTTPS, o TLS.
type listener struct {
	name    string // "API" (a principal) ou "HTTP de transição"
	addr    string
	handler http.Handler
	tls     *tls.Config // nil: HTTP
}

// serve abre todas as portas antes de servir (porta ocupada vira erro imediato, e o log
// "escutando" só sai quando ela está aberta de verdade) e encerra todas juntas.
func serve(ctx context.Context, logger *slog.Logger, ls []listener) error {
	type running struct {
		srv *http.Server
		ln  net.Listener
	}
	var all []running
	for _, l := range ls {
		ln, err := net.Listen("tcp", l.addr)
		if err != nil {
			for _, r := range all {
				r.ln.Close()
			}
			return fmt.Errorf("não foi possível escutar em %s: %w", l.addr, err)
		}
		if l.tls != nil {
			ln = tls.NewListener(ln, l.tls)
		}
		all = append(all, running{ln: ln, srv: &http.Server{
			Handler:           l.handler,
			TLSConfig:         l.tls,
			ReadHeaderTimeout: readHeaderTimeout,
			ReadTimeout:       readTimeout,
			WriteTimeout:      writeTimeout,
			IdleTimeout:       idleTimeout,
			// Erros internos do net/http também saem pelo slog (inclusive os de handshake TLS).
			ErrorLog: slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
		}})
	}

	errCh := make(chan error, len(all))
	for i, r := range all {
		go func() { errCh <- r.srv.Serve(r.ln) }()
		scheme := "http"
		if ls[i].tls != nil {
			scheme = "https"
		}
		logger.Info(ls[i].name+" escutando", "listen", r.ln.Addr().String(), "scheme", scheme)
	}

	var stopErr error
	select {
	case err := <-errCh:
		// Uma porta parou sem ter recebido sinal: é erro, e as outras fecham também.
		stopErr = fmt.Errorf("servidor HTTP parou: %w", err)
	case <-ctx.Done():
		logger.Info("sinal de encerramento recebido; finalizando requisições em andamento")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	for _, r := range all {
		if err := r.srv.Shutdown(shutdownCtx); err != nil && stopErr == nil {
			stopErr = fmt.Errorf("encerramento do servidor HTTP: %w", err)
		}
	}
	return stopErr
}

// healthcheckURL converte o endereço de escuta na URL local do /healthz.
// Endereços sem host ou curinga (0.0.0.0, ::) viram 127.0.0.1.
func healthcheckURL(listen string, useTLS bool) (string, error) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", err
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	scheme := "http"
	if useTLS {
		scheme = "https"
	}
	return scheme + "://" + net.JoinHostPort(host, port) + "/healthz", nil
}

// healthcheck consulta o /healthz do servidor local. É o que o HEALTHCHECK do Docker
// executa, já que a imagem final não tem shell nem curl. Com TLS, o certificado não é
// conferido: o pedido vai para 127.0.0.1, que não está no certificado, e a pergunta é só
// "o processo responde?"; quem confere a identidade do servidor são os agentes e o navegador.
func healthcheck(listen string, useTLS bool) error {
	u, err := healthcheckURL(listen, useTLS)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: healthcheckTimeout}
	if useTLS {
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // só liveness em 127.0.0.1
	}
	resp, err := client.Get(u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s respondeu %d", u, resp.StatusCode)
	}
	return nil
}
