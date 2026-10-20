package api

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"
)

// maxPanelForm: o maior formulário de texto do painel (os handlers têm limites menores, que
// continuam valendo: o corpo lido aqui volta para eles).
const maxPanelForm = 1 << 20

var errFormText = errors.New("o formulário tem caracteres inválidos (NUL ou UTF-8 inválido)")

// checkFormText (Aula 20.4b): todo formulário de texto do painel passa por aqui antes do
// handler (sameOrigin). NUL ou UTF-8 inválido num campo nunca é digitado por uma pessoa e o
// PostgreSQL recusa o INSERT: antes virava 500 (a varredura achou em /painel/patch/revogar).
// Agora é 400 na entrada, para todos os formulários de uma vez, inclusive os que vierem.
// O upload do logotipo (multipart) não passa aqui: é imagem, conferida pelo conteúdo.
func checkFormText(w http.ResponseWriter, r *http.Request) error {
	if !textOK(r.URL.Query()) {
		return errFormText
	}
	if r.Body == nil || r.Body == http.NoBody {
		return nil
	}
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if ct != "application/x-www-form-urlencoded" {
		return nil
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxPanelForm))
	if err != nil {
		return err
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	vals, err := url.ParseQuery(string(raw))
	if err != nil {
		return nil // o handler recusa com a mensagem dele
	}
	if !textOK(vals) {
		return errFormText
	}
	return nil
}

func textOK(v url.Values) bool {
	ok := func(s string) bool { return utf8.ValidString(s) && !strings.ContainsRune(s, 0) }
	for k, l := range v {
		if !ok(k) {
			return false
		}
		for _, s := range l {
			if !ok(s) {
				return false
			}
		}
	}
	return true
}
