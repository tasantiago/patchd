package main

import (
	"context"
	"errors"
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
// Docker) e a API em /api/.
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
func serve(ctx context.Context, logger *slog.Logger, addr string, handler http.Handler) error {
	// Abre a porta antes de servir: porta ocupada vira erro imediato, e o log
	// "API escutando" só sai quando a porta está aberta de verdade.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("não foi possível escutar em %s: %w", addr, err)
	}

	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		// Erros internos do net/http também saem pelo slog, em JSON.
		ErrorLog: slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	logger.Info("API escutando", "listen", ln.Addr().String())

	select {
	case err := <-errCh:
		// O servidor parou sem ter recebido sinal: é erro.
		return fmt.Errorf("servidor HTTP parou: %w", err)
	case <-ctx.Done():
		logger.Info("sinal de encerramento recebido; finalizando requisições em andamento")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("encerramento do servidor HTTP: %w", err)
	}
	// Depois do Shutdown, Serve devolve ErrServerClosed: é o caminho normal.
	if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("servidor HTTP: %w", err)
	}
	return nil
}

// healthcheckURL converte o endereço de escuta na URL local do /healthz.
// Endereços sem host ou curinga (0.0.0.0, ::) viram 127.0.0.1.
func healthcheckURL(listen string) (string, error) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", err
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/healthz", nil
}

// healthcheck consulta o /healthz do servidor local. É o que o HEALTHCHECK do Docker
// executa, já que a imagem final não tem shell nem curl.
func healthcheck(listen string) error {
	u, err := healthcheckURL(listen)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: healthcheckTimeout}
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
