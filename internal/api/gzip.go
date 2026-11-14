package api

import (
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"sync"
)

// Compressão das respostas do painel. As telas são HTML com tabelas grandes (Alertas com
// centenas de linhas, a página de uma correção, a padronização) e os CSV e JSON do parque
// passam de 1 MB: iam inteiros pela rede, e é o que mais pesa para quem abre o painel por VPN
// ou de outra unidade. Tabela em HTML encolhe umas dez vezes.
//
// O que NÃO é comprimido, e por quê:
//   - nada além de GET: as respostas que mostram um segredo uma única vez (o token de
//     registro, a senha revelada da BIOS, a senha nova de um usuário local) são respostas de
//     POST. Comprimir junto, na mesma resposta, um segredo e um texto que o atacante escolhe
//     é a condição do ataque BREACH; as páginas de GET não têm segredo no corpo, e o cookie
//     da sessão é SameSite=Strict (outro site não consegue fazer o navegador pedir uma página
//     autenticada para medir o tamanho);
//   - as rotas do agente (/api/v1/agent/): os instaladores e pacotes já são comprimidos, e o
//     download deles usa Range e Content-Length para retomar;
//   - pedido com Range, resposta que não é 200, que já vem codificada, ou de tipo que não é
//     texto (imagens, binários);
//   - resposta pequena (menos de gzipMin bytes): o cabeçalho do gzip custaria mais que o ganho.
const gzipMin = 1024

// gzipTypes: os tipos que valem a pena (o prefixo do Content-Type).
var gzipTypes = []string{"text/html", "text/csv", "text/css", "text/plain", "application/json", "image/svg+xml"}

var gzipPool = sync.Pool{New: func() any {
	// BestSpeed: a página sai em 1 a 3 ms de CPU a mais, e a tabela já encolhe 8 a 10 vezes;
	// os níveis acima custam o dobro de CPU por mais 10 a 20% de redução.
	w, _ := gzip.NewWriterLevel(io.Discard, gzip.BestSpeed)
	return w
}}

func withGzip(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Range") != "" || strings.HasPrefix(r.URL.Path, "/api/v1/agent/") ||
			!acceptsGzip(r.Header.Get("Accept-Encoding")) {
			next.ServeHTTP(w, r)
			return
		}
		// Quem guarda a resposta em cache precisa saber que ela muda com o pedido.
		w.Header().Add("Vary", "Accept-Encoding")
		g := &gzipWriter{ResponseWriter: w}
		defer g.close()
		next.ServeHTTP(g, r)
	})
}

func acceptsGzip(h string) bool {
	for _, part := range strings.Split(h, ",") {
		enc, q, _ := strings.Cut(strings.TrimSpace(part), ";")
		if strings.EqualFold(strings.TrimSpace(enc), "gzip") {
			q = strings.TrimSpace(q)
			return q != "q=0" && q != "q=0.0" && q != "q=0.00" && q != "q=0.000"
		}
	}
	return false
}

// gzipWriter segura o começo da resposta até saber se vale comprimir (o tipo, o status e
// pelo menos gzipMin bytes); daí em diante comprime, ou repassa como veio.
type gzipWriter struct {
	http.ResponseWriter
	status  int
	buf     []byte
	gz      *gzip.Writer
	decided bool // já escolheu: gz != nil comprime; senão repassa
}

func (g *gzipWriter) WriteHeader(code int) {
	if g.status == 0 {
		g.status = code
	}
	if code != http.StatusOK { // 304, 206, erros, redirecionamentos: como vieram
		_ = g.pass() // o erro de escrita aparece para quem escreve o corpo
	}
}

func (g *gzipWriter) Write(b []byte) (int, error) {
	if g.status == 0 {
		g.status = http.StatusOK
	}
	if g.decided {
		if g.gz != nil {
			return g.gz.Write(b)
		}
		return g.ResponseWriter.Write(b)
	}
	g.buf = append(g.buf, b...)
	if len(g.buf) < gzipMin {
		return len(b), nil
	}
	if !g.compressible() {
		return len(b), g.pass()
	}
	return len(b), g.start()
}

func (g *gzipWriter) compressible() bool {
	h := g.Header()
	if g.status != http.StatusOK || h.Get("Content-Encoding") != "" || h.Get("Content-Range") != "" {
		return false
	}
	ct := strings.ToLower(h.Get("Content-Type"))
	if ct == "" {
		ct = strings.ToLower(http.DetectContentType(g.buf))
	}
	for _, t := range gzipTypes {
		if strings.HasPrefix(ct, t) {
			return true
		}
	}
	return false
}

// start: a resposta vai comprimida. O tamanho anunciado, se houver, era o do corpo puro.
func (g *gzipWriter) start() error {
	g.decided = true
	h := g.Header()
	if h.Get("Content-Type") == "" {
		h.Set("Content-Type", http.DetectContentType(g.buf))
	}
	h.Del("Content-Length")
	h.Set("Content-Encoding", "gzip")
	g.ResponseWriter.WriteHeader(g.status)
	g.gz = gzipPool.Get().(*gzip.Writer)
	g.gz.Reset(g.ResponseWriter)
	_, err := g.gz.Write(g.buf)
	g.buf = nil
	return err
}

// pass: a resposta vai como veio (com o que já estava guardado).
func (g *gzipWriter) pass() error {
	if g.decided {
		return nil
	}
	g.decided = true
	if g.status != 0 {
		g.ResponseWriter.WriteHeader(g.status)
	}
	if len(g.buf) == 0 {
		return nil
	}
	_, err := g.ResponseWriter.Write(g.buf)
	g.buf = nil
	return err
}

// Flush: quem pede o envio na hora (uma resposta em partes) não espera os gzipMin bytes.
func (g *gzipWriter) Flush() {
	if !g.decided {
		if len(g.buf) > 0 && g.compressible() {
			_ = g.start()
		} else {
			_ = g.pass()
		}
	}
	if g.gz != nil {
		_ = g.gz.Flush()
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// close termina a resposta: a pequena (nunca chegou a gzipMin) sai pura.
func (g *gzipWriter) close() {
	if !g.decided {
		_ = g.pass()
	}
	if g.gz != nil {
		_ = g.gz.Close()
		g.gz.Reset(io.Discard)
		gzipPool.Put(g.gz)
		g.gz = nil
	}
}

// Unwrap deixa o http.ResponseController (prazo de escrita) alcançar a conexão.
func (g *gzipWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }
