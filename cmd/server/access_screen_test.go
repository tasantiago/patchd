package main

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/store"
)

// Aula 19.4: os grupos de acesso pela tela. O garantido do .env fica sempre admin; um grupo
// não pode estar nas duas listas; o AD não fica sem grupo nenhum; travado sem o AD/RHBK.
func TestGruposDeAcessoPelaTela(t *testing.T) {
	for _, c := range []struct{ in, out string }{
		{"GG-Patchd-Leitura, EXEMPLO\\GG-Gabinetes ,gg-patchd-leitura", "GG-Patchd-Leitura, GG-Gabinetes"},
		{"", ""},
		{"CN=Admins Patchd,OU=Grupos,DC=exemplo,DC=test", "CN=Admins Patchd,OU=Grupos,DC=exemplo,DC=test"},
	} {
		if got, err := checkGroups(c.in); err != nil || got != c.out {
			t.Errorf("%q: %q %v", c.in, got, err)
		}
	}
	var muitos []string
	for i := 0; i < 21; i++ {
		muitos = append(muitos, "gg-"+string(rune('a'+i)))
	}
	for _, ruim := range []string{"GG<script>", "grupo@exemplo", "CN=sem-dc", strings.Join(muitos, ",")} {
		if _, err := checkGroups(ruim); err == nil {
			t.Errorf("%q: aceito", ruim)
		}
	}
	if got, err := checkRoles("/ti/infra, patchd-ti"); err != nil || got != "ti/infra, patchd-ti" {
		t.Errorf("papéis: %q %v", got, err)
	}

	ctx := context.Background()
	env := map[string]string{"PATCHD_LDAP_HOST": "dc.exemplo", "PATCHD_LDAP_DOMAIN": "exemplo.test", "PATCHD_LDAP_ADMIN_GROUP": "GG-Patchd-Admin",
		"PATCHD_LDAP_READ_GROUP": "CN=Consulta Patchd,OU=Grupos,DC=exemplo,DC=test"}
	look := envDeTeste(env)
	s, _ := newSettings(ctx, look, store.NewMemory(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	groups := adGroups(look, s.Value)
	if a, r := groups(); strings.Join(a, "|") != "GG-Patchd-Admin" || strings.Join(r, "|") != env["PATCHD_LDAP_READ_GROUP"] {
		t.Fatalf("partida (o DN do .env vale inteiro): %v %v", a, r)
	}
	if _, err := s.Apply(ctx, map[string]string{"PATCHD_LDAP_ADMIN_GROUPS": "GG-Infra", "PATCHD_LDAP_READ_GROUP": "GG-Infra"}, "admin"); err == nil ||
		!strings.Contains(err.Error(), "nas duas listas") {
		t.Errorf("duas listas: %v", err)
	}
	if _, err := s.Apply(ctx, map[string]string{"PATCHD_LDAP_READ_GROUP": "gg-patchd-admin"}, "admin"); err == nil {
		t.Error("o garantido na leitura: recusado")
	}
	if _, err := s.Apply(ctx, map[string]string{"PATCHD_LDAP_ADMIN_GROUPS": "GG-Infra, GG-Patchd-Admin", "PATCHD_LDAP_READ_GROUP": "GG-Gabinetes, GG-Corregedoria"}, "admin"); err != nil {
		t.Fatal(err)
	}
	if a, r := groups(); strings.Join(a, "|") != "GG-Patchd-Admin|GG-Infra" || strings.Join(r, "|") != "GG-Gabinetes|GG-Corregedoria" {
		t.Errorf("da tela, o garantido primeiro e sem repetir: %v %v", a, r)
	}

	// Sem o RHBK, os campos dele ficam travados; o help mostra o garantido do AD.
	for _, g := range s.Groups() {
		for _, f := range g.Fields {
			switch f.Key {
			case "PATCHD_OIDC_ADMIN_ROLES", "PATCHD_OIDC_READ_ROLE":
				if f.LockedWhy == "" {
					t.Errorf("%s sem o RHBK: travado", f.Key)
				}
			case "PATCHD_LDAP_ADMIN_GROUPS":
				if f.LockedWhy != "" || !strings.Contains(f.Help, "O grupo garantido, GG-Patchd-Admin") || f.Sessions != "ad" {
					t.Errorf("campo: %+v", f)
				}
			}
		}
	}
	if _, err := s.Apply(ctx, map[string]string{"PATCHD_OIDC_READ_ROLE": "x"}, "admin"); err == nil {
		t.Error("RHBK desligado: recusado")
	}

	// AD sem grupo garantido: a tela não pode deixar sem grupo nenhum.
	env2 := map[string]string{"PATCHD_LDAP_HOST": "dc.exemplo", "PATCHD_LDAP_DOMAIN": "exemplo.test", "PATCHD_LDAP_READ_GROUP": "GG-Leitura"}
	s2, _ := newSettings(ctx, envDeTeste(env2), store.NewMemory(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := s2.Apply(ctx, map[string]string{"PATCHD_LDAP_READ_GROUP": ""}, "admin"); err == nil || !strings.Contains(err.Error(), "sem grupo nenhum") {
		t.Errorf("sem grupo nenhum: %v", err)
	}

	// RHBK: o papel garantido sempre admin.
	env3 := map[string]string{"PATCHD_OIDC_ISSUER": "https://sso.exemplo/realms/x"}
	s3, _ := newSettings(ctx, envDeTeste(env3), store.NewMemory(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := s3.Apply(ctx, map[string]string{"PATCHD_OIDC_ADMIN_ROLES": "/ti/infra", "PATCHD_OIDC_READ_ROLE": "patchd-leitura, gabinetes"}, "admin"); err != nil {
		t.Fatal(err)
	}
	if a, r := oidcRoles(envDeTeste(env3), s3.Value)(); strings.Join(a, "|") != "patchd-admin|ti/infra" || strings.Join(r, "|") != "patchd-leitura|gabinetes" {
		t.Errorf("papéis: %v %v", a, r)
	}
}
