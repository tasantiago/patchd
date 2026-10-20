package oidc

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/oidc/oidctest"
	"github.com/tasantiago/patchd/internal/panel"
)

// Aula 20.3: o ID token chega pelo navegador, na volta do RHBK. Dois alvos:
//
//   - FuzzIDToken: um texto qualquer no lugar do token. Só passa o token que o provedor
//     assinou, e procurar um kid inventado não dispara mais de uma leitura da JWKS.
//   - FuzzIDTokenClaims: conteúdo qualquer, assinado pelo provedor (o que um provedor com
//     defeito, ou mal configurado, mandaria). O que passa respeita todas as regras, e o
//     mapeamento para o usuário do painel não entra em pânico.

func provedor(f *testing.F) (*oidctest.Provider, *Provider) {
	p := oidctest.New(f)
	prov, err := New(configDe(p), nil)
	if err != nil {
		f.Fatal(err)
	}
	if err := prov.Discover(context.Background()); err != nil {
		f.Fatal(err)
	}
	p.Authorize(prov.AuthURL(Login{Nonce: "n1"}), "fulano", "patchd-admin")
	return p, prov
}

func FuzzIDToken(f *testing.F) {
	p, prov := provedor(f)
	bom := p.Sign(nil)
	assinado := bom[:strings.LastIndex(bom, ".")]
	f.Add(bom)
	f.Add(p.Sign(func(m map[string]any) { m["nonce"] = "n2" }))
	f.Add("eyJhbGciOiJub25lIn0.e30.")
	f.Add("..")
	f.Fuzz(func(t *testing.T, raw string) {
		antes := p.JWKSHits()
		_, err := prov.VerifyIDToken(context.Background(), raw, "n1")
		if d := p.JWKSHits() - antes; d > 1 {
			t.Fatalf("%d leituras da JWKS num token só", d)
		}
		if err == nil && !strings.HasPrefix(raw, assinado+".") {
			t.Fatalf("token que o provedor não assinou foi aceito: %q", raw)
		}
	})
}

func FuzzIDTokenClaims(f *testing.F) {
	p, prov := provedor(f)
	iss := p.Issuer()
	agora := time.Now().Unix()
	f.Add([]byte(`{"iss":"` + iss + `","aud":"` + oidctest.ClientID + `","sub":"s","nonce":"n1","exp":` +
		itoa(agora+300) + `,"iat":` + itoa(agora) + `,"preferred_username":"fulano","groups":["/patchd-admin"]}`))
	f.Add([]byte(`{"iss":"` + iss + `","aud":["` + oidctest.ClientID + `","x"],"azp":"` + oidctest.ClientID +
		`","sub":"s","nonce":"n1","exp":1e300,"iat":-1e300,"preferred_username":"A B","groups":"patchd-leitura"}`))
	f.Add([]byte(`[]`))
	f.Add([]byte(`null`))
	f.Fuzz(func(t *testing.T, conteudo []byte) {
		claims, err := prov.VerifyIDToken(context.Background(), p.SignRaw(conteudo), "n1")
		if err != nil {
			return
		}
		str := func(k string) string { s, _ := claims[k].(string); return s }
		var aud []string
		switch v := claims["aud"].(type) {
		case string:
			aud = []string{v}
		case []any:
			for _, x := range v {
				if s, ok := x.(string); ok {
					aud = append(aud, s)
				}
			}
		}
		exp, _ := claims["exp"].(float64)
		switch {
		case str("iss") != iss, !slices.Contains(aud, oidctest.ClientID), str("nonce") != "n1", str("sub") == "":
			t.Fatalf("aceito fora das regras: %s", conteudo)
		case len(aud) > 1 && str("azp") != oidctest.ClientID:
			t.Fatalf("várias audiências sem azp: %s", conteudo)
		case exp < float64(time.Now().Add(-clockSkew).Unix()):
			t.Fatalf("aceito vencido (exp %v): %s", exp, conteudo)
		}
		id, err := prov.identity(claims)
		if err == nil && (!panel.ValidUsername(id.Username) || (id.Role != panel.RoleAdmin && id.Role != panel.RoleRead)) {
			t.Fatalf("identidade inválida aceita: %+v", id)
		}
	})
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
