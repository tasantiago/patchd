package jobs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// TypeServerURL (Aula 9.2) passa o agente para outro endereço do MESMO servidor: a URL
// HTTPS, depois do período na porta HTTP de transição. O endereço vem assinado, como
// qualquer job: quem está no meio do caminho HTTP não consegue mandar o agente para um
// servidor dele. O agente só troca depois de entregar o resultado pelo endereço novo (TLS
// conferido e credencial aceita); se não der, fica onde está e conta por quê.
const TypeServerURL = "server_url"

// ServerURLParams são os parâmetros do job server_url.
type ServerURLParams struct {
	URL string `json:"url"`
}

// ParseServerURLParams lê e confere os parâmetros: só https, com host, sem usuário,
// caminho, query nem fragmento (o agente monta as rotas a partir da raiz).
func ParseServerURLParams(raw json.RawMessage) (ServerURLParams, error) {
	var p ServerURLParams
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return p, fmt.Errorf("parâmetros do server_url ilegíveis: %w", err)
	}
	norm, err := NormalizeServerURL(p.URL)
	if err != nil {
		return p, err
	}
	if norm != p.URL {
		return p, fmt.Errorf("URL fora da forma normal: %q (use %q)", p.URL, norm)
	}
	return p, nil
}

// NormalizeServerURL confere a URL e devolve a forma normal: esquema e host em minúsculas,
// sem barra no fim.
func NormalizeServerURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	switch {
	case err != nil || u.Host == "" || u.Hostname() == "":
		return "", errors.New("URL do servidor inválida")
	case !strings.EqualFold(u.Scheme, "https"):
		return "", fmt.Errorf("a URL nova do servidor tem de ser https (recebido %q)", u.Scheme)
	case u.User != nil:
		return "", errors.New("a URL do servidor não leva usuário nem senha")
	case (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "":
		return "", errors.New("a URL do servidor é só esquema, host e porta (sem caminho, query ou fragmento)")
	}
	return "https://" + strings.ToLower(u.Host), nil
}

// ServerURLParamsJSON monta os parâmetros para uma URL (normalizada).
func ServerURLParamsJSON(raw string) (json.RawMessage, error) {
	norm, err := NormalizeServerURL(raw)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(ServerURLParams{URL: norm})
	return b, nil
}
