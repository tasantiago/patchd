package api

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A compressão das respostas: só GET, só texto, só 200, só acima do mínimo, nunca as rotas
// do agente nem pedido com Range; o que sai comprimido volta igual ao original.
func TestCompressaoDasRespostas(t *testing.T) {
	grande := strings.Repeat("<tr><td>PC-00001</td><td>Windows 11 Pro 25H2</td></tr>\n", 400)
	mux := http.NewServeMux()
	mux.HandleFunc("/html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Length", "999999") // o do corpo puro: tem de sair
		for _, parte := range strings.SplitAfter(grande, "\n") {
			_, _ = io.WriteString(w, parte) // em pedaços pequenos, como o html/template escreve
		}
	})
	mux.HandleFunc("/pequena", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<p>ok</p>")
	})
	mux.HandleFunc("/imagem", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(bytes.Repeat([]byte{0x89, 'P', 'N', 'G'}, 1000))
	})
	mux.HandleFunc("/erro", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, grande)
	})
	mux.HandleFunc("/redireciona", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/html", http.StatusSeeOther) })
	mux.HandleFunc("/ja", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Content-Encoding", "br")
		_, _ = io.WriteString(w, grande)
	})
	mux.HandleFunc("/semtipo", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "<!doctype html><html>"+grande) })
	mux.HandleFunc("/partes", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<p>primeira parte</p>")
		w.(http.Flusher).Flush()
		_, _ = io.WriteString(w, grande)
	})
	mux.HandleFunc("/api/v1/agent/content/pacotes/x.msi", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, grande)
	})
	h := withGzip(mux)
	pede := func(method, path string, cab ...string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		for i := 0; i+1 < len(cab); i += 2 {
			r.Header.Set(cab[i], cab[i+1])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	abre := func(w *httptest.ResponseRecorder) string {
		t.Helper()
		zr, err := gzip.NewReader(w.Body)
		if err != nil {
			t.Fatalf("o corpo não é gzip: %v", err)
		}
		b, err := io.ReadAll(zr)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	w := pede("GET", "/html", "Accept-Encoding", "gzip, deflate, br")
	if w.Header().Get("Content-Encoding") != "gzip" || w.Header().Get("Content-Length") != "" || !strings.Contains(w.Header().Get("Vary"), "Accept-Encoding") ||
		w.Body.Len() > len(grande)/8 {
		t.Fatalf("página grande: cabeçalhos %v, %d bytes de %d", w.Header(), w.Body.Len(), len(grande))
	}
	if abre(w) != grande {
		t.Error("o corpo comprimido não volta igual")
	}
	if w := pede("GET", "/semtipo", "Accept-Encoding", "gzip"); w.Header().Get("Content-Encoding") != "gzip" || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") ||
		!strings.HasSuffix(abre(w), grande) {
		t.Errorf("sem Content-Type: %v", w.Header())
	}
	if w := pede("GET", "/partes", "Accept-Encoding", "gzip"); w.Header().Get("Content-Encoding") != "gzip" || abre(w) != "<p>primeira parte</p>"+grande {
		t.Errorf("resposta em partes: %v", w.Header())
	}

	// O que sai como veio.
	for nome, w := range map[string]*httptest.ResponseRecorder{
		"sem Accept-Encoding": pede("GET", "/html"),
		"gzip recusado (q=0)": pede("GET", "/html", "Accept-Encoding", "br, gzip;q=0"),
		"POST":                pede("POST", "/html", "Accept-Encoding", "gzip"),
		"com Range":           pede("GET", "/html", "Accept-Encoding", "gzip", "Range", "bytes=0-99"),
		"rota do agente":      pede("GET", "/api/v1/agent/content/pacotes/x.msi", "Accept-Encoding", "gzip"),
		"erro 404":            pede("GET", "/erro", "Accept-Encoding", "gzip"),
		"imagem":              pede("GET", "/imagem", "Accept-Encoding", "gzip"),
		"já codificada":       pede("GET", "/ja", "Accept-Encoding", "gzip"),
		"pequena":             pede("GET", "/pequena", "Accept-Encoding", "gzip"),
		"redirecionamento":    pede("GET", "/redireciona", "Accept-Encoding", "gzip"),
	} {
		if enc := w.Header().Get("Content-Encoding"); enc == "gzip" {
			t.Errorf("%s: saiu comprimida", nome)
		}
	}
	if w := pede("GET", "/pequena", "Accept-Encoding", "gzip"); w.Body.String() != "<p>ok</p>" || w.Code != 200 {
		t.Errorf("pequena: %d %q", w.Code, w.Body.String())
	}
	if w := pede("GET", "/erro", "Accept-Encoding", "gzip"); w.Code != 404 || w.Body.String() != grande {
		t.Errorf("erro: %d, %d bytes", w.Code, w.Body.Len())
	}
	if w := pede("GET", "/redireciona", "Accept-Encoding", "gzip"); w.Code != 303 || w.Header().Get("Location") != "/html" {
		t.Errorf("redirecionamento: %d %v", w.Code, w.Header())
	}
	if w := pede("GET", "/imagem", "Accept-Encoding", "gzip"); w.Body.Len() != 4000 {
		t.Errorf("imagem: %d bytes", w.Body.Len())
	}
}
