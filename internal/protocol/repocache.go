package protocol

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var repoHostPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// Validate confere o anúncio do cache de repositórios. Vale nos dois lados: o servidor
// recusa a configuração, e o agente recusa o anúncio. O agente escreve esses valores em
// arquivos de configuração do apt e do dnf: aspas, ponto e vírgula, espaços ou quebras de
// linha permitiriam injetar outras opções.
func (rc RepoCache) Validate() error {
	if strings.ContainsAny(rc.URL, "\"';\\ \t\r\n{}$") {
		return fmt.Errorf("URL do cache com caracteres não permitidos: %q", rc.URL)
	}
	u, err := url.Parse(rc.URL)
	switch {
	case err != nil:
		return fmt.Errorf("URL do cache inválida: %w", err)
	case u.Scheme != "http" && u.Scheme != "https":
		return fmt.Errorf("URL do cache: use http:// ou https:// (recebido %q)", rc.URL)
	case u.Host == "" || u.User != nil:
		return fmt.Errorf("URL do cache: precisa de host e não pode ter usuário (recebido %q)", rc.URL)
	case (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "":
		return fmt.Errorf("URL do cache: só esquema, host e porta, sem caminho (recebido %q)", rc.URL)
	}
	for _, h := range rc.AptHosts {
		if !repoHostPattern.MatchString(h) {
			return fmt.Errorf("origem do apt inválida: %q", h)
		}
	}
	if len(rc.AptHosts) == 0 {
		return errors.New("anúncio do cache sem origens do apt")
	}
	return nil
}

// Base devolve a URL sem a barra final.
func (rc RepoCache) Base() string { return strings.TrimSuffix(rc.URL, "/") }
