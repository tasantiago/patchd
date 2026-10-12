package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestConfiguracaoOIDC (Aula 9.4): desligado sem issuer; padrões dos grupos e do retorno
// pela URL pública; o segredo é conferido, mas só lido na partida do servidor.
func TestConfiguracaoOIDC(t *testing.T) {
	if _, on, err := oidcConfig(lookMap(nil), "", true); on || err != nil {
		t.Fatalf("sem issuer: %v %v", on, err)
	}
	secret := filepath.Join(t.TempDir(), "oidc_client_secret")
	_ = os.WriteFile(secret, []byte("s3gredo\n"), 0o600)
	env := map[string]string{"PATCHD_OIDC_ISSUER": "http://127.0.0.1:18180/realms/patchd", "PATCHD_OIDC_CLIENT_SECRET_FILE": secret}
	cfg, on, err := oidcConfig(lookMap(env), "https://127.0.0.1:18443", true)
	if err != nil || !on || cfg.ClientSecret != "s3gredo" || cfg.ClientID != "patchd-painel" || cfg.RolesClaim != "groups" ||
		cfg.AdminRole != "patchd-admin" || cfg.ReadRole != "patchd-leitura" || cfg.RedirectURL != "https://127.0.0.1:18443/painel/oidc/retorno" {
		t.Fatalf("padrões: %+v %v %v", cfg, on, err)
	}
	// Sem URL pública nem PATCHD_OIDC_REDIRECT_URL: falta o retorno.
	if _, _, err := oidcConfig(lookMap(env), "", true); err == nil || !strings.Contains(err.Error(), "PATCHD_OIDC_REDIRECT_URL") {
		t.Errorf("sem retorno: %v", err)
	}
	// Arquivo vazio: erro na partida.
	_ = os.WriteFile(secret, nil, 0o600)
	if _, _, err := oidcConfig(lookMap(env), "https://127.0.0.1:18443", true); err == nil || !strings.Contains(err.Error(), "vazio") {
		t.Errorf("segredo vazio: %v", err)
	}
	// Na validação da configuração (subcomandos), basta o segredo ter sido indicado.
	if _, _, err := oidcConfig(lookMap(env), "https://127.0.0.1:18443", false); err != nil {
		t.Errorf("validação sem ler: %v", err)
	}
	// Sem segredo nenhum: a configuração do servidor recusa.
	delete(env, "PATCHD_OIDC_CLIENT_SECRET_FILE")
	env["PATCHD_CATALOG_INTERVAL"] = "0"
	if _, err := loadServerConfig(nil, lookMap(env), io.Discard); err == nil || !strings.Contains(err.Error(), "PATCHD_OIDC_CLIENT_SECRET") {
		t.Errorf("sem segredo: %v", err)
	}
}
