// Package httpx reúne utilitários de HTTP compartilhados pela API e pelo cache de
// repositórios.
package httpx

import (
	"net/http"
	"time"
)

// ExtendWrites devolve um ResponseWriter que empurra o prazo de escrita para idle à frente a
// cada escrita. O servidor tem WriteTimeout (60 s) para respostas comuns; um download de
// centenas de MB (o wsusscn2.cab, um .deb grande) numa rede lenta passaria disso e seria
// cortado no meio. Com este writer, o limite vira "idle sem conseguir escrever nada", o
// mesmo critério do lado de quem baixa.
//
// https://pkg.go.dev/net/http#ResponseController.SetWriteDeadline
func ExtendWrites(w http.ResponseWriter, idle time.Duration) http.ResponseWriter {
	rc := http.NewResponseController(w)
	ew := &extendingWriter{ResponseWriter: w, rc: rc, idle: idle}
	ew.extend()
	return ew
}

type extendingWriter struct {
	http.ResponseWriter
	rc   *http.ResponseController
	idle time.Duration
}

// extend ignora http.ErrNotSupported (um ResponseWriter de teste, sem conexão).
func (e *extendingWriter) extend() { _ = e.rc.SetWriteDeadline(time.Now().Add(e.idle)) }

func (e *extendingWriter) WriteHeader(code int) {
	e.extend()
	e.ResponseWriter.WriteHeader(code)
}

func (e *extendingWriter) Write(b []byte) (int, error) {
	e.extend()
	return e.ResponseWriter.Write(b)
}

// Unwrap deixa o http.ResponseController alcançar a conexão por baixo deste writer.
func (e *extendingWriter) Unwrap() http.ResponseWriter { return e.ResponseWriter }
