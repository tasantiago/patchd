package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/oidc"
)

// oidcConfig lê o login pelo RHBK do ambiente (Aula 9.4). Sem PATCHD_OIDC_ISSUER,
// desligado. readSecret false só confere que o segredo foi indicado (a validação da
// configuração roda também nos subcomandos, que não precisam lê-lo).
//
// O endereço de volta, se não vier em PATCHD_OIDC_REDIRECT_URL, é a URL pública do
// servidor (a mesma dos agentes, Aula 9.2) com /painel/oidc/retorno.
func oidcConfig(look config.Lookup, publicURL string, readSecret bool) (oidc.Config, bool, error) {
	issuer := config.String(look, "PATCHD_OIDC_ISSUER", "")
	if issuer == "" {
		return oidc.Config{}, false, nil
	}
	cfg := oidc.Config{
		Issuer:      issuer,
		ClientID:    config.String(look, "PATCHD_OIDC_CLIENT_ID", "patchd-painel"),
		RedirectURL: config.String(look, "PATCHD_OIDC_REDIRECT_URL", ""),
		RolesClaim:  config.String(look, "PATCHD_OIDC_ROLES_CLAIM", "groups"),
		AdminRole:   config.String(look, "PATCHD_OIDC_ADMIN_ROLE", "patchd-admin"),
		ReadRole:    config.String(look, "PATCHD_OIDC_READ_ROLE", "patchd-leitura"),
	}
	if cfg.RedirectURL == "" && publicURL != "" {
		cfg.RedirectURL = strings.TrimRight(publicURL, "/") + "/painel/oidc/retorno"
	}
	if readSecret {
		s, err := config.Secret(look, "PATCHD_OIDC_CLIENT_SECRET")
		if err != nil {
			return cfg, true, fmt.Errorf("login pelo RHBK: %w", err)
		}
		cfg.ClientSecret = s
	} else if v, ok := look("PATCHD_OIDC_CLIENT_SECRET"); ok && v != "" {
		cfg.ClientSecret = "indicado"
	} else if v, ok := look("PATCHD_OIDC_CLIENT_SECRET_FILE"); ok && v != "" {
		cfg.ClientSecret = "indicado"
	}
	if err := cfg.Validate(); err != nil {
		return cfg, true, fmt.Errorf("login pelo RHBK (PATCHD_OIDC_ISSUER definido):\n%w", err)
	}
	return cfg, true, nil
}

// checkOIDC busca a descoberta logo na partida, só para avisar no log: o login funciona
// mesmo se o provedor estiver fora agora (a descoberta é refeita no primeiro login).
func checkOIDC(ctx context.Context, logger *slog.Logger, p *oidc.Provider) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := p.Discover(ctx); err != nil {
		logger.Warn("login pelo RHBK: o provedor não respondeu agora; o botão tenta de novo a cada login", "error", err)
		return
	}
	logger.Info("login pelo RHBK: descoberta do provedor conferida", "issuer", p.Issuer())
}
