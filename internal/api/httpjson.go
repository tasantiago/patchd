package api

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/tasantiago/patchd/internal/protocol"
)

// Limites do corpo das requisições. Um inventário de 1500 pacotes tem perto de 500 KB, e
// comprimido, perto de 50 KB. O limite descomprimido existe por causa das "bombas de
// compressão": 40 KB de gzip podem virar 40 MB de zeros.
const (
	maxBodyBytes    = 8 << 20  // como chega (comprimido ou não)
	maxDecodedBytes = 32 << 20 // depois de descomprimir
)

// errDecodedTooLarge: o corpo descomprimido passou de maxDecodedBytes.
var errDecodedTooLarge = errors.New("corpo descomprimido acima do limite")

// capReader devolve errDecodedTooLarge quando passa de n bytes. Diferente do
// io.LimitReader, que só pararia em silêncio e deixaria um JSON truncado.
type capReader struct {
	r io.Reader
	n int64
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.n <= 0 {
		return 0, errDecodedTooLarge
	}
	if int64(len(p)) > c.n {
		p = p[:c.n]
	}
	k, err := c.r.Read(p)
	c.n -= int64(k)
	return k, err
}

// decodeJSON exige Content-Type JSON, aceita o corpo puro ou em gzip, limita o tamanho
// comprimido e o descomprimido, e lê exatamente um valor JSON. Campos desconhecidos são
// aceitos: um agente mais novo pode trazer campos opcionais que este servidor ainda não
// conhece (regra de schema da Aula 2.5). Em caso de erro, já respondeu.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "envie o corpo como application/json")
		return false
	}

	var body io.Reader = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	switch enc := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))); enc {
	case "", "identity":
	case "gzip":
		zr, err := gzip.NewReader(body)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_gzip", "corpo gzip inválido: "+err.Error())
			return false
		}
		defer zr.Close()
		body = &capReader{r: zr, n: maxDecodedBytes}
	default:
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_encoding",
			"Content-Encoding não suportado: "+enc+" (use gzip ou nenhum)")
		return false
	}

	dec := json.NewDecoder(body)
	if err := dec.Decode(dst); err != nil {
		var tooBig *http.MaxBytesError
		switch {
		case errors.As(err, &tooBig):
			writeError(w, http.StatusRequestEntityTooLarge, "body_too_large", "corpo acima do limite de 8 MB")
		case errors.Is(err, errDecodedTooLarge):
			writeError(w, http.StatusRequestEntityTooLarge, "body_too_large", "corpo descomprimido acima do limite de 32 MB")
		default:
			writeError(w, http.StatusBadRequest, "invalid_json", "JSON inválido: "+err.Error())
		}
		return false
	}
	// Exatamente um valor: dados depois do JSON indicam corpo corrompido ou concatenado.
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid_json", "o corpo tem dados além do JSON")
		return false
	}
	return true
}

// writeJSON responde com o valor em JSON.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError responde no formato de erro da API.
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, protocol.ErrorResponse{Error: protocol.ErrorDetail{Code: code, Message: message}})
}
