package ad

import (
	"context"
	"crypto/x509"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/ldap/ldaptest"
	"github.com/tasantiago/patchd/internal/panel"
)

const base = "DC=exemplo,DC=test"

func servidor(t *testing.T) *ldaptest.Server {
	return ldaptest.Start(t, ldaptest.Directory{
		Domain: "exemplo.test",
		Users: map[string]ldaptest.User{
			"fulano":   {DN: "CN=Fulano de Tal,OU=TI," + base, Password: "senha-do-fulano", MemberOf: []string{"CN=Equipe Infra,OU=Grupos," + base}},
			"ciclano":  {DN: "CN=Ciclano,OU=Gabinetes," + base, Password: "senha-do-ciclano", MemberOf: []string{"CN=Consulta Patchd,OU=Grupos," + base}},
			"beltrano": {DN: "CN=Beltrano,OU=Gabinetes," + base, Password: "senha-do-beltrano"},
			"inativo":  {DN: "CN=Inativo,OU=TI," + base, Password: "senha-do-inativo", Disabled: true},
		},
		Groups: map[string]ldaptest.Group{
			// Fulano é admin por aninhamento: Equipe Infra → Admins Patchd.
			"equipe.infra":    {DN: "CN=Equipe Infra,OU=Grupos," + base, MemberOf: []string{"CN=Admins Patchd,OU=Grupos," + base}},
			"admins.patchd":   {DN: "CN=Admins Patchd,OU=Grupos," + base},
			"consulta.patchd": {DN: "CN=Consulta Patchd,OU=Grupos," + base},
		},
	})
}

func diretorio(t *testing.T, s *ldaptest.Server, mods ...func(*Config)) *Directory {
	t.Helper()
	cfg := Config{Host: s.Host(), Domain: "exemplo.test", AdminGroup: "admins.patchd", ReadGroup: "consulta.patchd", RootCAs: s.CertPool(), Timeout: 3 * time.Second}
	for _, m := range mods {
		m(&cfg)
	}
	d, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestPerfisPelosGrupos(t *testing.T) {
	s := servidor(t)
	d := diretorio(t, s)
	ctx := context.Background()

	rep, err := d.Check(ctx, "fulano", "senha-do-fulano")
	if err != nil || !rep.Admin || rep.Read || rep.Role() != panel.RoleAdmin || !strings.HasPrefix(rep.TLSVersion, "TLS 1.") {
		t.Fatalf("fulano (admin por grupo aninhado): %+v %v", rep, err)
	}
	if role, err := d.Authenticate(ctx, "ciclano", "senha-do-ciclano"); err != nil || role != panel.RoleRead {
		t.Errorf("ciclano: %q %v", role, err)
	}
	if _, err := d.Authenticate(ctx, "beltrano", "senha-do-beltrano"); !errors.Is(err, panel.ErrNoRole) {
		t.Errorf("beltrano, sem grupo: %v", err)
	}
	// Grupo pelo DN em vez do nome: o mesmo resultado.
	d2 := diretorio(t, s, func(c *Config) { c.AdminGroup = "CN=Admins Patchd,OU=Grupos," + base; c.ReadGroup = "" })
	if role, err := d2.Authenticate(ctx, "fulano", "senha-do-fulano"); err != nil || role != panel.RoleAdmin {
		t.Errorf("grupo por DN: %q %v", role, err)
	}
	if _, err := d2.Authenticate(ctx, "ciclano", "senha-do-ciclano"); !errors.Is(err, panel.ErrNoRole) {
		t.Errorf("sem grupo de leitura configurado: %v", err)
	}
	// Base de busca explícita (uma OU) que não contém o usuário: não encontrado.
	d3 := diretorio(t, s, func(c *Config) { c.BaseDN = "OU=Gabinetes," + base })
	var ue *UnavailableError
	if _, err := d3.Authenticate(ctx, "fulano", "senha-do-fulano"); !errors.As(err, &ue) || !strings.Contains(ue.Reason, "PATCHD_LDAP_BASE_DN") {
		t.Errorf("fora da base: %v", err)
	}
}

func TestSenhasRecusadas(t *testing.T) {
	s := servidor(t)
	d := diretorio(t, s)
	ctx := context.Background()
	for _, c := range []struct{ user, pw, motivo string }{
		{"fulano", "errada", "senha inválida (52e)"},
		{"ninguem", "qualquer", "senha inválida (52e)"}, // o AD não distingue no bind por UPN
		{"inativo", "senha-do-inativo", "conta desativada (533)"},
	} {
		_, err := d.Authenticate(ctx, c.user, c.pw)
		if !errors.Is(err, panel.ErrBadCredentials) || !strings.Contains(err.Error(), c.motivo) {
			t.Errorf("%s: %v", c.user, err)
		}
	}
	// Senha vazia e nome fora do padrão nem chegam ao AD.
	antes := s.Binds.Load()
	for _, c := range [][2]string{{"fulano", ""}, {"dominio\\fulano", "x"}, {"fulano@outro", "x"}, {"*", "x"}} {
		if _, err := d.Authenticate(ctx, c[0], c[1]); !errors.Is(err, panel.ErrBadCredentials) {
			t.Errorf("%q: %v", c[0], err)
		}
	}
	if s.Binds.Load() != antes {
		t.Error("nenhum desses deveria virar bind")
	}
}

func TestIndisponivelSemExporOServidor(t *testing.T) {
	s := servidor(t)
	ctx := context.Background()
	casos := map[string]func(*Config){
		"connection refused": func(c *Config) { c.Host = "127.0.0.1:1" },
		"CA desconhecida":    func(c *Config) { c.RootCAs = x509.NewCertPool() },
		"não confere":        func(c *Config) { c.Host = s.Addr() }, // pelo IP: fora do certificado
		"grupo admin":        func(c *Config) { c.AdminGroup = "grupo.que.nao.existe" },
		"DNS":                func(c *Config) { c.Host = "dc-inexistente.invalid" },
	}
	for want, mod := range casos {
		d := diretorio(t, s, mod)
		_, err := d.Authenticate(ctx, "fulano", "senha-do-fulano")
		t.Logf("%s → %v", want, err)
		var ue *UnavailableError
		// O texto exato do erro de DNS depende do resolvedor da rede; o tipo, não.
		if !errors.As(err, &ue) || (want != "DNS" && !strings.Contains(err.Error(), want)) {
			t.Errorf("%s: %v", want, err)
			continue
		}
		// A mensagem não traz endereço, porta nem nome do servidor.
		for _, proibido := range []string{"127.0.0", "localhost", "invalid", "exemplo.test", ":636"} {
			if strings.Contains(err.Error(), proibido) {
				t.Errorf("%s: a mensagem expõe %q: %v", want, proibido, err)
			}
		}
	}
}

func TestConfiguracao(t *testing.T) {
	ok := Config{Host: "dc01.empresa.exemplo", Domain: "empresa.exemplo", AdminGroup: "g"}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, a, _ := ok.addr(); a != "dc01.empresa.exemplo:636" {
		t.Errorf("porta padrão: %s", a)
	}
	if ok.baseDN() != "DC=empresa,DC=exemplo" {
		t.Errorf("base derivada: %s", ok.baseDN())
	}
	for nome, c := range map[string]Config{
		"sem host":    {Domain: "empresa.exemplo", AdminGroup: "g"},
		"url no host": {Host: "ldaps://dc01", Domain: "empresa.exemplo", AdminGroup: "g"},
		"domínio":     {Host: "dc01", Domain: "EMPRESA", AdminGroup: "g"},
		"sem grupos":  {Host: "dc01", Domain: "empresa.exemplo"},
		"base sem DN": {Host: "dc01", Domain: "empresa.exemplo", AdminGroup: "g", BaseDN: "empresa"},
		"porta vazia": {Host: "dc01:", Domain: "empresa.exemplo", AdminGroup: "g"},
	} {
		err := c.Validate()
		if err == nil {
			t.Errorf("%s: deveria falhar", nome)
			continue
		}
		if strings.Contains(err.Error(), "dc01") || strings.Contains(err.Error(), "EMPRESA") {
			t.Errorf("%s: a mensagem cita o valor: %v", nome, err)
		}
	}
}

func TestMotivosDoAD(t *testing.T) {
	for msg, want := range map[string]string{
		"80090308: LdapErr: DSID-0C090569, comment: AcceptSecurityContext error, data 775, v4f7c": "conta bloqueada (775)",
		"... data 773, v4563": "troca de senha obrigatória (773)",
		"... data 9999, v1":   "credenciais recusadas (9999)",
		"sem código":          "credenciais recusadas",
	} {
		if got := bindReason(msg); got != want {
			t.Errorf("%q: %q", msg, got)
		}
	}
}
