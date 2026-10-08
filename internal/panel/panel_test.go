package panel

import (
	"strings"
	"testing"
	"time"
)

func TestHashEConferencia(t *testing.T) {
	h, err := hashPassword("correta-cavalo-bateria", minIterations)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "pbkdf2-sha256$100000$") || len(strings.Split(h, "$")) != 4 {
		t.Fatalf("formato: %s", h)
	}
	if !CheckPassword(h, "correta-cavalo-bateria") {
		t.Error("a senha certa deveria conferir")
	}
	for _, errada := range []string{"", "correta-cavalo-bateri", "Correta-cavalo-bateria", "correta-cavalo-bateria "} {
		if CheckPassword(h, errada) {
			t.Errorf("%q não deveria conferir", errada)
		}
	}
	// Mesmo texto, sal diferente: hashes diferentes.
	if h2, _ := hashPassword("correta-cavalo-bateria", minIterations); h2 == h {
		t.Error("o sal deveria ser aleatório")
	}
	// Hashes adulterados ou de outro esquema nunca conferem.
	p := strings.Split(h, "$")
	for _, ruim := range []string{
		"", "texto-puro", "bcrypt$" + strings.Join(p[1:], "$"),
		strings.Join([]string{p[0], "1", p[2], p[3]}, "$"),        // iterações baixas demais
		strings.Join([]string{p[0], "99999999", p[2], p[3]}, "$"), // altas demais
		strings.Join([]string{p[0], p[1], p[2], p[3][:10]}, "$"),  // hash curto
		strings.Join([]string{p[0], p[1], "!!", p[3]}, "$"),       // sal inválido
	} {
		if CheckPassword(ruim, "correta-cavalo-bateria") {
			t.Errorf("hash %q não deveria conferir", ruim)
		}
	}
}

func TestHashPadraoUsa600mil(t *testing.T) {
	if testing.Short() {
		t.Skip("lento")
	}
	h, err := HashPassword("uma-senha-de-teste")
	if err != nil || !strings.HasPrefix(h, "pbkdf2-sha256$600000$") || !CheckPassword(h, "uma-senha-de-teste") {
		t.Fatalf("%s %v", h, err)
	}
	if CheckPasswordUnknownUser("uma-senha-de-teste") {
		t.Error("usuário desconhecido nunca confere")
	}
}

func TestPoliticaDaSenha(t *testing.T) {
	for pw, ok := range map[string]bool{
		"curta":                      false,
		"onze-carac":                 false,
		"doze-caract!":               true,
		"ççççççççççç":                false, // 11 caracteres, 22 bytes: conta caracteres
		"çççççççççççç":               true,
		" com-espaco-na-ponta":       false,
		strings.Repeat("a", 1025):    false,
		"\xff\xfe-nao-e-utf8-valido": false,
	} {
		if err := CheckPasswordPolicy(pw); (err == nil) != ok {
			t.Errorf("%q: %v", pw, err)
		}
	}
}

func TestNomesEPerfis(t *testing.T) {
	for s, ok := range map[string]bool{
		"thyago": true, "t.santiago": true, "adm_01": true, "a": false, "-x": false,
		"Maiuscula": false, "com espaco": false, "dominio\\user": false, "user@dominio": false,
		strings.Repeat("a", 64): true, strings.Repeat("a", 65): false,
	} {
		if ValidUsername(s) != ok {
			t.Errorf("%q: esperado %t", s, ok)
		}
	}
	if NormalizeUsername("  Fulano.Tal ") != "fulano.tal" {
		t.Error("normalização")
	}
	if !ValidRole(RoleAdmin) || !ValidRole(RoleRead) || ValidRole("root") || ValidRole("") {
		t.Error("perfis")
	}
}

func TestValidadeDaSessao(t *testing.T) {
	t0 := time.Date(2026, 10, 8, 8, 0, 0, 0, time.UTC)
	s := Session{CreatedAt: t0, ExpiresAt: t0.Add(SessionMaxAge), LastSeenAt: t0}
	if !s.Valid(t0.Add(29 * time.Minute)) {
		t.Error("29 min sem uso ainda vale")
	}
	if s.Valid(t0.Add(30 * time.Minute)) {
		t.Error("30 min sem uso vence")
	}
	s.LastSeenAt = t0.Add(SessionMaxAge - time.Minute)
	if !s.Valid(t0.Add(SessionMaxAge - time.Second)) {
		t.Error("em uso, vale até o prazo absoluto")
	}
	if s.Valid(t0.Add(SessionMaxAge)) {
		t.Error("o prazo absoluto vence mesmo em uso")
	}
}
