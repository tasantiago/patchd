package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/ad"
	"github.com/tasantiago/patchd/internal/ldap/ldaptest"
)

func TestConfigDoAD(t *testing.T) {
	if _, on, err := adConfig(lookMap(nil)); on || err != nil {
		t.Errorf("sem PATCHD_LDAP_HOST: desligado (%t %v)", on, err)
	}
	cfg, on, err := adConfig(lookMap(map[string]string{
		"PATCHD_LDAP_HOST": "dc01.empresa.exemplo", "PATCHD_LDAP_DOMAIN": "empresa.exemplo", "PATCHD_LDAP_ADMIN_GROUP": "admins.patchd",
	}))
	if !on || err != nil || cfg.ReadGroup != "" {
		t.Fatalf("ligado: %+v %t %v", cfg, on, err)
	}
	// Só o host (como ficou o .env depois da 7.1): o servidor recusa partir e cita as variáveis.
	_, on, err = adConfig(lookMap(map[string]string{"PATCHD_LDAP_HOST": "dc01.empresa.exemplo"}))
	if !on || err == nil || !strings.Contains(err.Error(), "PATCHD_LDAP_DOMAIN") || !strings.Contains(err.Error(), "PATCHD_LDAP_ADMIN_GROUP") ||
		strings.Contains(err.Error(), "dc01") {
		t.Errorf("incompleto: %v", err)
	}
	if _, err := loadServerConfig(nil, lookMap(map[string]string{"PATCHD_LDAP_HOST": "dc01.empresa.exemplo"}), io.Discard); err == nil {
		t.Error("loadServerConfig deveria recusar o AD incompleto")
	}
}

func TestADCheck(t *testing.T) {
	const base = "DC=exemplo,DC=test"
	s := ldaptest.Start(t, ldaptest.Directory{
		Domain: "exemplo.test",
		Users: map[string]ldaptest.User{
			"fulano":   {DN: "CN=Fulano,OU=TI," + base, Password: "senha-do-fulano", MemberOf: []string{"CN=Equipe,OU=G," + base}},
			"beltrano": {DN: "CN=Beltrano,OU=TI," + base, Password: "senha-do-beltrano"},
		},
		Groups: map[string]ldaptest.Group{
			"equipe":        {DN: "CN=Equipe,OU=G," + base, MemberOf: []string{"CN=Admins Patchd,OU=G," + base}},
			"admins.patchd": {DN: "CN=Admins Patchd,OU=G," + base},
		},
	})
	cfg := ad.Config{Host: s.Host(), Domain: "exemplo.test", AdminGroup: "admins.patchd", RootCAs: s.CertPool(), Timeout: 3 * time.Second}
	dir, err := ad.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	roda := func(user, pw string) (int, string) {
		var out bytes.Buffer
		code := adCheck(context.Background(), dir, cfg, user, pw, &out)
		return code, out.String()
	}

	code, out := roda("fulano", "senha-do-fulano")
	if code != exitOK || !strings.Contains(out, "senha: conferida pelo AD") || !strings.Contains(out, "grupo admin (PATCHD_LDAP_ADMIN_GROUP): membro") ||
		!strings.Contains(out, "grupo leitura (PATCHD_LDAP_READ_GROUP): não configurado") || !strings.Contains(out, "perfil no painel: admin") {
		t.Errorf("fulano: %d\n%s", code, out)
	}
	if code, out := roda("beltrano", "senha-do-beltrano"); code != exitRuntime || !strings.Contains(out, "perfil no painel: nenhum") {
		t.Errorf("beltrano: %d\n%s", code, out)
	}
	if code, out := roda("fulano", "errada"); code != exitRuntime || !strings.Contains(out, "senha inválida (52e)") {
		t.Errorf("senha errada: %d\n%s", code, out)
	}
	cfg2 := cfg
	cfg2.Host = "127.0.0.1:1"
	dir2, _ := ad.New(cfg2)
	var buf bytes.Buffer
	if code := adCheck(context.Background(), dir2, cfg2, "fulano", "senha-do-fulano", &buf); code != exitRuntime ||
		!strings.Contains(buf.String(), "connection refused") || !strings.Contains(buf.String(), "contingência") {
		t.Errorf("fora do ar: %d\n%s", code, buf.String())
	}
	// Nada do servidor, do domínio ou dos DNs na saída.
	for _, o := range []string{out, buf.String()} {
		for _, proibido := range []string{"localhost", "127.0.0.1", "exemplo.test", "DC=", "CN="} {
			if strings.Contains(o, proibido) {
				t.Errorf("a saída expõe %q:\n%s", proibido, o)
			}
		}
	}
}
