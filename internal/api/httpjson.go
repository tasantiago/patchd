package api

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/tasantiago/patchd/internal/protocol"
)

// maxBodyBytes limita o corpo das requisições. Um inventário de 1500 pacotes tem perto
// de 500 KB; 8 MB dá folga sem permitir que um cliente esgote a memória do servidor.
const maxBodyBytes = 8 << 20

// decodeJSON exige Content-Type JSON, limita o tamanho e lê exatamente um valor JSON.
// Campos desconhecidos são aceitos: um agente mais novo pode trazer campos opcionais que
// este servidor ainda não conhece (regra de schema da Aula 2.5). Em caso de erro, já respondeu.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "envie o corpo como application/json")
		return false
	}

	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err := dec.Decode(dst); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "body_too_large", "corpo acima do limite de 8 MB")
			return false
		}
		writeError(w, http.StatusBadRequest, "invalid_json", "JSON inválido: "+err.Error())
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
