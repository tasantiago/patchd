// Package panel reúne o que identifica as pessoas no painel (Aula 7.2): os usuários
// locais, os perfis, as senhas e as sessões.
//
// Os usuários do AD (Aula 7.3) não ficam numa tabela: o AD confere a senha e diz o
// perfil pelos grupos a cada login. A sessão é a mesma para as duas origens.
package panel

import (
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Perfis. O admin administra (usuários, tokens, ações nas máquinas, nos módulos
// seguintes); a leitura só consulta.
const (
	RoleAdmin = "admin"
	RoleRead  = "leitura"
)

// Origens da sessão: quem conferiu a senha.
const (
	SourceLocal = "local"
	SourceAD    = "ad"
	SourceOIDC  = "oidc"      // RHBK (Aula 9.4)
	SourceScan  = "varredura" // aberta pela linha de comando para o ZAP (Aula 20.4)
)

// SessionPrefix identifica o segredo da sessão, como os prefixos de identity.
const SessionPrefix = "patchd_ses_"

// Prazos da sessão: absoluto (mesmo em uso, um novo login a cada expediente) e de
// inatividade (o navegador esquecido aberto).
const (
	SessionMaxAge = 8 * time.Hour
	SessionIdle   = 30 * time.Minute
)

// User é um usuário local do painel. PasswordHash só é lido no login.
type User struct {
	Name         string
	Role         string
	PasswordHash string
	CreatedAt    time.Time
}

// Session é uma sessão aberta no painel. Guardada pelo hash do segredo do cookie.
type Session struct {
	Username    string
	DisplayName string // nome de exibição do AD ou do RHBK (Aula 15.5); vazio na conta local
	Role        string
	Source      string
	CreatedAt   time.Time
	ExpiresAt   time.Time
	LastSeenAt  time.Time
}

// Valid diz se a sessão ainda vale em now: antes do prazo absoluto e sem passar do
// prazo de inatividade.
func (s Session) Valid(now time.Time) bool {
	return now.Before(s.ExpiresAt) && now.Sub(s.LastSeenAt) < SessionIdle
}

// Greeting é o nome do "Olá": o de exibição, ou o usuário quando não há.
func (s Session) Greeting() string {
	if s.DisplayName != "" {
		return s.DisplayName
	}
	return s.Username
}

// SourceLabel é a origem como o admin fala: AD, RHBK ou local.
func (s Session) SourceLabel() string { return SourceLabel(s.Source) }

// SourceLabel traduz a origem de uma sessão.
func SourceLabel(src string) string {
	switch src {
	case SourceAD:
		return "AD"
	case SourceOIDC:
		return "RHBK"
	case SourceLocal:
		return "local"
	case SourceScan:
		return "varredura"
	}
	return src
}

// MaxDisplayName limita o nome de exibição guardado na sessão.
const MaxDisplayName = 128

// CleanDisplayName prepara o nome que veio do AD ou do RHBK para a tela: sem caracteres
// de controle nem de formatação (os invisíveis que invertem o texto, por exemplo), com
// os espaços juntados e no máximo MaxDisplayName caracteres. O HTML já é escapado pelo
// modelo; aqui é para o nome não enganar quem lê.
func CleanDisplayName(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "")
	}
	s = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsSpace(r):
			return ' '
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r):
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > MaxDisplayName {
		s = strings.TrimSpace(string([]rune(s)[:MaxDisplayName]))
	}
	return s
}

// ErrUserExists: já há um usuário local com o nome.
var ErrUserExists = errors.New("o usuário já existe")

// Erros de quem confere a senha (o AD, na Aula 7.3). Qualquer outro erro quer dizer
// "não foi possível conferir": é quando a conta local de contingência pode entrar.
var (
	ErrBadCredentials = errors.New("credenciais recusadas")
	ErrNoRole         = errors.New("o usuário não está em nenhum grupo do painel")
)

// usernamePattern: minúsculas, dígitos, ponto, hífen e sublinhado, de 2 a 64 caracteres.
// Cabe no sAMAccountName do AD (Aula 7.3) e nunca precisa de escape no log ou no HTML.
var usernamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,63}$`)

// NormalizeUsername tira os espaços das pontas e passa para minúsculas: "Fulano " e
// "fulano" são a mesma pessoa, como no AD.
func NormalizeUsername(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// ValidUsername confere o nome já normalizado.
func ValidUsername(s string) bool { return usernamePattern.MatchString(s) }

// ValidRole diz se o perfil existe.
func ValidRole(s string) bool { return s == RoleAdmin || s == RoleRead }
