package ldap_test

import (
	"context"
	"crypto/tls"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/ldap"
	"github.com/tasantiago/patchd/internal/ldap/ldaptest"
)

const base = "DC=exemplo,DC=test"

func diretorio() ldaptest.Directory {
	return ldaptest.Directory{
		Domain: "exemplo.test",
		Users: map[string]ldaptest.User{
			"fulano": {DN: "CN=Fulano,OU=TI," + base, Password: "senha-do-fulano", MemberOf: []string{"CN=Equipe,OU=Grupos," + base}},
		},
		Groups: map[string]ldaptest.Group{
			"equipe":  {DN: "CN=Equipe,OU=Grupos," + base, MemberOf: []string{"CN=Admins,OU=Grupos," + base}},
			"admins":  {DN: "CN=Admins,OU=Grupos," + base},
			"leitura": {DN: "CN=Leitura,OU=Grupos," + base},
		},
	}
}

func conecta(t *testing.T, s *ldaptest.Server) *ldap.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	c, err := ldap.DialTLS(ctx, s.Addr(), &tls.Config{RootCAs: s.CertPool(), ServerName: "localhost", MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestBindEBusca(t *testing.T) {
	s := ldaptest.Start(t, diretorio())
	c := conecta(t, s)
	if c.TLSVersion < tls.VersionTLS12 {
		t.Errorf("TLS %x", c.TLSVersion)
	}

	// Antes do bind, o AD recusa a busca.
	_, err := c.Search(ldap.SearchRequest{BaseDN: base, Scope: ldap.ScopeSubtree, Filter: ldap.Equal("sAMAccountName", "fulano")})
	var re *ldap.ResultError
	if !errors.As(err, &re) || re.Code != ldap.ResultOperationsError {
		t.Fatalf("busca sem bind: %v", err)
	}

	err = c.Bind("fulano@exemplo.test", "errada")
	if !errors.As(err, &re) || re.Code != ldap.ResultInvalidCredentials || !strings.Contains(re.Message, "data 52e") {
		t.Fatalf("senha errada: %v", err)
	}
	if err := c.Bind("fulano@exemplo.test", ""); err == nil || s.Binds.Load() != 1 {
		t.Fatalf("senha vazia nem chega ao servidor: %v (binds=%d)", err, s.Binds.Load())
	}
	if err := c.Bind("Fulano@exemplo.test", "senha-do-fulano"); err != nil {
		t.Fatal(err)
	}

	// Usuário: uma entrada, mesmo com a referência que o AD devolve na busca da raiz.
	dns, err := c.Search(ldap.SearchRequest{BaseDN: base, Scope: ldap.ScopeSubtree, SizeLimit: 2,
		Filter: ldap.And(ldap.Equal("objectClass", "user"), ldap.Equal("sAMAccountName", "fulano"))})
	if err != nil || len(dns) != 1 || dns[0] != "CN=Fulano,OU=TI,"+base {
		t.Fatalf("usuário: %v %v", dns, err)
	}
	// Grupo aninhado: Fulano → Equipe → Admins.
	for grupo, membro := range map[string]bool{"CN=Admins,OU=Grupos," + base: true, "CN=Leitura,OU=Grupos," + base: false} {
		dns, err := c.Search(ldap.SearchRequest{BaseDN: "CN=Fulano,OU=TI," + base, Scope: ldap.ScopeBase, SizeLimit: 1,
			Filter: ldap.InChain("memberOf", grupo)})
		if err != nil || (len(dns) == 1) != membro {
			t.Errorf("%s: %v %v", grupo, dns, err)
		}
	}
	// O valor digitado vai como dado: caracteres especiais de filtro não mudam a consulta.
	dns, err = c.Search(ldap.SearchRequest{BaseDN: base, Scope: ldap.ScopeSubtree,
		Filter: ldap.And(ldap.Equal("objectClass", "user"), ldap.Equal("sAMAccountName", "*)(objectClass=*"))})
	if err != nil || len(dns) != 0 {
		t.Errorf("injeção: %v %v", dns, err)
	}
	// Limite de tamanho: devolve o que veio e o código 4.
	dns, err = c.Search(ldap.SearchRequest{BaseDN: base, Scope: ldap.ScopeSubtree, SizeLimit: 1, Filter: ldap.Equal("objectClass", "group")})
	if !errors.As(err, &re) || re.Code != 4 || len(dns) != 1 {
		t.Errorf("sizeLimit: %v %v", dns, err)
	}
}

func TestCertificadoEPrazo(t *testing.T) {
	s := ldaptest.Start(t, diretorio())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Sem confiar na CA do servidor, nem conecta.
	if _, err := ldap.DialTLS(ctx, s.Addr(), &tls.Config{ServerName: "localhost"}); err == nil {
		t.Error("certificado não confiável deveria ser recusado")
	}
	// Nome diferente do certificado.
	if _, err := ldap.DialTLS(ctx, s.Addr(), &tls.Config{RootCAs: s.CertPool(), ServerName: "dc.outro"}); err == nil {
		t.Error("nome fora do certificado deveria ser recusado")
	}
	// Porta sem ninguém.
	curto, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if _, err := ldap.DialTLS(curto, "127.0.0.1:1", &tls.Config{}); err == nil {
		t.Error("porta fechada")
	}
}
