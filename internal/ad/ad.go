// Package ad confere o login do painel no Active Directory por LDAPS (Aula 7.3).
//
// O bind é feito como o próprio usuário (UPN implícito "usuario@dominio"): o patchd
// não tem conta de serviço nem guarda senha do AD. Com a mesma conexão autenticada, o
// patchd acha o DN do usuário e pergunta ao AD se ele é membro, direto ou por grupos
// aninhados, do grupo do perfil admin ou do perfil leitura.
package ad

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/ldap"
	"github.com/tasantiago/patchd/internal/panel"
)

// DefaultPort é a porta do LDAPS.
const DefaultPort = "636"

// DefaultTimeout é o prazo de um login inteiro (conexão, bind e buscas).
const DefaultTimeout = 10 * time.Second

// Config é a configuração do login pelo AD.
type Config struct {
	Host       string // nome do controlador de domínio (ou do domínio), com ou sem :porta
	Domain     string // nome DNS do domínio: o sufixo do UPN implícito
	BaseDN     string // vazio: derivado do domínio (DC=...,DC=...)
	AdminGroup string // grupo do perfil admin: sAMAccountName ou DN
	ReadGroup  string // grupo do perfil leitura: sAMAccountName ou DN
	Timeout    time.Duration
	RootCAs    *x509.CertPool // nil: as CAs do sistema (no container, inclui a corporativa)
	// Groups (Aula 19.4): os grupos de cada perfil, lidos a cada login (mudam pela tela).
	// nil: AdminGroup e ReadGroup. Os do .env (AdminGroup, ReadGroup) entram pela lista como
	// os outros; a diferença é que um grupo do .env que não existe torna o AD indisponível
	// (configuração errada), e um da tela que não existe só fica de fora (com aviso).
	Groups func() (admin, read []string)
}

var domainPattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,62}\.)+[A-Za-z0-9-]{1,63}$`)

// Validate confere a configuração. As mensagens citam a variável, nunca o valor: o
// nome do domínio e do servidor são do ambiente e não vão para o log nem para o chat.
func (c Config) Validate() error {
	var problems []error
	if _, _, err := c.addr(); err != nil {
		problems = append(problems, errors.New("PATCHD_LDAP_HOST: use nome ou nome:porta"))
	}
	if !domainPattern.MatchString(c.Domain) {
		problems = append(problems, errors.New("PATCHD_LDAP_DOMAIN: informe o nome DNS do domínio (ex.: empresa.exemplo)"))
	}
	if c.BaseDN != "" && !strings.Contains(c.BaseDN, "=") {
		problems = append(problems, errors.New("PATCHD_LDAP_BASE_DN: não parece um DN (ex.: DC=empresa,DC=exemplo)"))
	}
	if c.AdminGroup == "" && c.ReadGroup == "" {
		problems = append(problems, errors.New("PATCHD_LDAP_ADMIN_GROUP e PATCHD_LDAP_READ_GROUP: defina pelo menos um"))
	}
	return errors.Join(problems...)
}

func (c Config) addr() (host, addr string, err error) {
	h := strings.TrimSpace(c.Host)
	if h == "" || strings.ContainsAny(h, "/ ") {
		return "", "", errors.New("host vazio ou inválido")
	}
	if host, port, err := net.SplitHostPort(h); err == nil {
		if host == "" || port == "" {
			return "", "", errors.New("host ou porta vazios")
		}
		return host, h, nil
	}
	return h, net.JoinHostPort(h, DefaultPort), nil
}

// baseDN devolve a base de busca: a configurada ou a derivada do domínio.
func (c Config) baseDN() string {
	if c.BaseDN != "" {
		return c.BaseDN
	}
	parts := strings.Split(c.Domain, ".")
	for i, p := range parts {
		parts[i] = "DC=" + p
	}
	return strings.Join(parts, ",")
}

// Directory faz os logins.
type Directory struct {
	cfg  Config
	host string
	addr string
	tls  *tls.Config
}

// New valida a configuração e prepara o TLS: TLS 1.2 ou mais, com o certificado do
// servidor conferido pelo nome em Host.
func New(cfg Config) (*Directory, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	host, addr, _ := cfg.addr()
	return &Directory{cfg: cfg, host: host, addr: addr,
		tls: &tls.Config{ServerName: host, RootCAs: cfg.RootCAs, MinVersion: tls.VersionTLS12}}, nil
}

// Report é o resultado de um login conferido, para o login e para o "patchd-server ad check".
type Report struct {
	TLSVersion string
	Name       string // displayName da conta, já limpo (Aula 15.5); vazio se o AD não tem
	Admin      bool   // membro de um grupo admin (direto ou aninhado)
	Read       bool   // membro de um grupo leitura
	// Aula 19.4: por qual grupo (o primeiro da lista em que ele está) e os grupos da tela que
	// o AD não achou (ficaram de fora).
	AdminVia, ReadVia string
	Missing           []string
}

// GroupLists: as listas de agora.
func (c Config) GroupLists() (admin, read []string) {
	if c.Groups != nil {
		return c.Groups()
	}
	if c.AdminGroup != "" {
		admin = []string{c.AdminGroup}
	}
	if c.ReadGroup != "" {
		read = []string{c.ReadGroup}
	}
	return admin, read
}

// Role é o perfil do painel: admin vence leitura; nenhum dos dois, vazio.
func (r Report) Role() string {
	switch {
	case r.Admin:
		return panel.RoleAdmin
	case r.Read:
		return panel.RoleRead
	}
	return ""
}

// UnavailableError: o AD não respondeu como deveria (rede, certificado, configuração).
// É o caso em que a conta local de contingência pode entrar.
type UnavailableError struct {
	Reason string
}

func (e *UnavailableError) Error() string { return "AD indisponível: " + e.Reason }

// Authenticate confere usuário e senha e devolve o perfil e o nome de exibição. Erros:
// panel.ErrBadCredentials (senha, conta desativada ou bloqueada), panel.ErrNoRole (fora
// dos grupos) ou *UnavailableError.
func (d *Directory) Authenticate(ctx context.Context, user, password string) (role, name string, err error) {
	rep, err := d.Check(ctx, user, password)
	if err != nil {
		return "", "", err
	}
	role = rep.Role()
	if role == "" {
		return "", "", panel.ErrNoRole
	}
	return role, rep.Name, nil
}

// Check faz o login e confere os dois grupos.
func (d *Directory) Check(ctx context.Context, user, password string) (Report, error) {
	var rep Report
	if password == "" || !panel.ValidUsername(user) {
		return rep, fmt.Errorf("%w: usuário fora do padrão ou senha vazia", panel.ErrBadCredentials)
	}
	ctx, cancel := context.WithTimeout(ctx, d.cfg.Timeout)
	defer cancel()

	c, err := ldap.DialTLS(ctx, d.addr, d.tls)
	if err != nil {
		return rep, &UnavailableError{Reason: describe(err)}
	}
	defer c.Close()
	rep.TLSVersion = tls.VersionName(c.TLSVersion)

	// UPN implícito: sAMAccountName@<nome DNS do domínio>, aceito pelo AD mesmo quando o
	// userPrincipalName da conta tem outro sufixo.
	if err := c.Bind(user+"@"+d.cfg.Domain, password); err != nil {
		var re *ldap.ResultError
		if errors.As(err, &re) && re.Code == ldap.ResultInvalidCredentials {
			return rep, fmt.Errorf("%w: %s", panel.ErrBadCredentials, bindReason(re.Message))
		}
		return rep, &UnavailableError{Reason: "bind: " + describe(err)}
	}

	base := d.cfg.baseDN()
	// O displayName vem na mesma busca: é o nome que o Outlook e o Teams mostram.
	entry, err := d.findEntry(c, base, ldap.And(ldap.Equal("objectClass", "user"), ldap.Equal("sAMAccountName", user)), "displayName")
	userDN := entry.DN
	rep.Name = panel.CleanDisplayName(entry.First("displayName"))
	if errors.Is(err, errNotFound) {
		return rep, &UnavailableError{Reason: "usuário autenticado, mas fora da base de busca (confira PATCHD_LDAP_BASE_DN)"}
	}
	if err != nil {
		return rep, &UnavailableError{Reason: "busca do usuário: " + err.Error()}
	}
	admins, reads := d.cfg.GroupLists()
	check := func(list []string, label, env string) (string, error) {
		for _, g := range list {
			ok, err := d.memberOf(c, base, userDN, g)
			switch {
			case errors.Is(err, errGroupNotFound) && !strings.EqualFold(g, env):
				rep.Missing = append(rep.Missing, g) // da tela: fica de fora, o login segue
			case err != nil:
				return "", &UnavailableError{Reason: label + ": " + err.Error()}
			case ok:
				return g, nil
			}
		}
		return "", nil
	}
	if rep.AdminVia, err = check(admins, "grupo admin (PATCHD_LDAP_ADMIN_GROUP)", d.cfg.AdminGroup); err != nil {
		return rep, err
	}
	if rep.ReadVia, err = check(reads, "grupo leitura (PATCHD_LDAP_READ_GROUP)", d.cfg.ReadGroup); err != nil {
		return rep, err
	}
	rep.Admin, rep.Read = rep.AdminVia != "", rep.ReadVia != ""
	return rep, nil
}

// errGroupNotFound: o grupo não existe na base de busca.
var errGroupNotFound = errors.New("grupo não encontrado (confira o nome do grupo e a base de busca)")

// errNotFound: a busca não achou a entrada.
var errNotFound = errors.New("não encontrado")

// findOne exige exatamente uma entrada e devolve o DN.
func (d *Directory) findOne(c *ldap.Conn, base string, f ldap.Filter) (string, error) {
	e, err := d.findEntry(c, base, f)
	return e.DN, err
}

// findEntry exige exatamente uma entrada e devolve os atributos pedidos.
func (d *Directory) findEntry(c *ldap.Conn, base string, f ldap.Filter, attrs ...string) (ldap.Entry, error) {
	es, err := c.SearchEntries(ldap.SearchRequest{BaseDN: base, Scope: ldap.ScopeSubtree, Filter: f, SizeLimit: 2,
		TimeLimit: int(d.cfg.Timeout.Seconds()), Attributes: attrs})
	switch {
	case err != nil:
		return ldap.Entry{}, errors.New(describe(err))
	case len(es) == 0:
		return ldap.Entry{}, errNotFound
	case len(es) > 1:
		return ldap.Entry{}, errors.New("mais de uma entrada com o mesmo nome")
	}
	return es[0], nil
}

// memberOf diz se o usuário é membro do grupo, direto ou aninhado. Grupo vazio: não.
func (d *Directory) memberOf(c *ldap.Conn, base, userDN, group string) (bool, error) {
	if group == "" {
		return false, nil
	}
	groupDN := group
	if !strings.Contains(group, "=") {
		dn, err := d.findOne(c, base, ldap.And(ldap.Equal("objectClass", "group"), ldap.Equal("sAMAccountName", group)))
		if errors.Is(err, errNotFound) {
			return false, errGroupNotFound
		}
		if err != nil {
			return false, err
		}
		groupDN = dn
	}
	// Busca na própria entrada do usuário: volta 1 se ele está na cadeia do grupo.
	dns, err := c.Search(ldap.SearchRequest{BaseDN: userDN, Scope: ldap.ScopeBase, Filter: ldap.InChain("memberOf", groupDN), SizeLimit: 1, TimeLimit: int(d.cfg.Timeout.Seconds())})
	if err != nil {
		return false, errors.New(describe(err))
	}
	return len(dns) == 1, nil
}

// adData acha o código do motivo na mensagem do AD ("..., data 52e, v4563").
var adData = regexp.MustCompile(`data ([0-9a-fA-F]{3,8})`)

// bindReason traduz o código do AD (Microsoft, "LDAP Error 49" / códigos de
// AcceptSecurityContext) num motivo para o log. Nunca vai para a tela de login.
func bindReason(msg string) string {
	m := adData.FindStringSubmatch(msg)
	if m == nil {
		return "credenciais recusadas"
	}
	reasons := map[string]string{
		"525": "usuário não encontrado",
		"52e": "senha inválida",
		"530": "fora do horário permitido",
		"531": "estação não permitida",
		"532": "senha vencida",
		"533": "conta desativada",
		"701": "conta vencida",
		"773": "troca de senha obrigatória",
		"775": "conta bloqueada",
	}
	if r, ok := reasons[strings.ToLower(m[1])]; ok {
		return r + " (" + strings.ToLower(m[1]) + ")"
	}
	return "credenciais recusadas (" + strings.ToLower(m[1]) + ")"
}

// describe resume um erro de rede, TLS ou LDAP sem o nome nem o IP do servidor: o log
// do patchd vai para o chat e para chamados, e esses valores ficam só no .env.
func describe(err error) string {
	var (
		dnsErr   *net.DNSError
		opErr    *net.OpError
		hostErr  x509.HostnameError
		authErr  x509.UnknownAuthorityError
		certErr  x509.CertificateInvalidError
		verifErr *tls.CertificateVerificationError
		resErr   *ldap.ResultError
	)
	switch {
	case errors.As(err, &resErr):
		return fmt.Sprintf("resposta LDAP %d", resErr.Code)
	case errors.Is(err, context.DeadlineExceeded) || isTimeout(err):
		return "tempo esgotado"
	case errors.As(err, &dnsErr):
		return "DNS: " + dnsErr.Err
	case errors.As(err, &hostErr):
		return "certificado: o nome do servidor não confere com o certificado"
	case errors.As(err, &authErr):
		return "certificado: emitido por CA desconhecida (a CA corporativa está no container?)"
	case errors.As(err, &certErr):
		return "certificado inválido: " + certInvalidReason(certErr.Reason)
	case errors.As(err, &verifErr):
		return "certificado não verificado"
	case errors.As(err, &opErr):
		if opErr.Err != nil {
			return opErr.Op + ": " + opErr.Err.Error()
		}
		return opErr.Op
	case errors.Is(err, ldap.ErrDisconnected):
		return "o AD encerrou a conexão"
	}
	// Erros do próprio cliente (formato, TLS) não carregam endereços.
	return err.Error()
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func certInvalidReason(r x509.InvalidReason) string {
	if r == x509.Expired {
		return "vencido ou ainda não válido"
	}
	return fmt.Sprintf("motivo %d", r)
}
