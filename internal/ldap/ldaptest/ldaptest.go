// Package ldaptest é um servidor LDAPS de mentira que imita o que o login usa do Active
// Directory: bind por UPN com os códigos "data 52e/533/775" na mensagem, buscas só depois
// do bind, referências nas buscas a partir da raiz e a regra de grupos aninhados
// (1.2.840.113556.1.4.1941). Só para testes.
package ldaptest

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/ber"
	"github.com/tasantiago/patchd/internal/ldap"
)

// User é uma conta do diretório.
type User struct {
	DN       string
	Password string
	MemberOf []string // DNs dos grupos dos quais é membro direto
	Disabled bool
	Locked   bool
}

// Group é um grupo do diretório; MemberOf faz o aninhamento.
type Group struct {
	DN       string
	MemberOf []string
}

// Directory é o conteúdo do servidor, indexado pelo sAMAccountName em minúsculas.
type Directory struct {
	Domain string // sufixo do UPN, ex.: exemplo.test
	Users  map[string]User
	Groups map[string]Group
}

// Server é o servidor em execução.
type Server struct {
	dir   Directory
	ln    net.Listener
	pool  *x509.CertPool
	Binds atomic.Int32 // binds recebidos (para conferir que nada chegou ao diretório)
	mu    sync.Mutex
	// Filtros recebidos, decodificados como texto, na ordem.
	Filters []string
}

// Start sobe o servidor em 127.0.0.1, com certificado próprio só para o nome "localhost"
// (conectar pelo IP falha na conferência do nome), e o derruba no fim do teste.
func Start(t testing.TB, dir Directory) *Server {
	t.Helper()
	cert, pool := selfSigned(t)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{dir: dir, ln: ln, pool: pool}
	t.Cleanup(func() { ln.Close() })
	go s.serve()
	return s
}

// Addr é o endereço IP:porta.
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Host é "localhost:porta", o nome que o certificado cobre.
func (s *Server) Host() string {
	_, port, _ := net.SplitHostPort(s.Addr())
	return net.JoinHostPort("localhost", port)
}

// CertPool confia no certificado do servidor.
func (s *Server) CertPool() *x509.CertPool { return s.pool }

func (s *Server) serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(c)
	}
}

func reply(c net.Conn, id int64, op []byte) {
	_, _ = c.Write(ber.Cons(ber.TagSequence, ber.Int(ber.TagInteger, id), op))
}

func ldapResult(id byte, code int, msg string) []byte {
	return ber.Cons(id, ber.Int(ber.TagEnumerated, int64(code)), ber.Str(ber.TagOctetString, ""), ber.Str(ber.TagOctetString, msg))
}

// adBindError imita a mensagem do AD num bind recusado.
func adBindError(data string) string {
	return "80090308: LdapErr: DSID-0C090569, comment: AcceptSecurityContext error, data " + data + ", v4f7c\x00"
}

func (s *Server) handle(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	r := bufio.NewReader(c)
	bound := false
	for {
		raw, err := ber.ReadMessage(r)
		if err != nil {
			return
		}
		msg, err := ber.Parse(raw)
		if err != nil || len(msg.Children) < 2 {
			return
		}
		id, _ := msg.Children[0].Int()
		op := msg.Children[1]
		switch op.ID {
		case ber.ClassApplication | ber.Constructed | 0: // bind
			s.Binds.Add(1)
			name, pw := op.Children[1].String(), op.Children[2].String()
			code, diag := s.bind(name, pw)
			bound = code == 0
			reply(c, id, ldapResult(ber.ClassApplication|ber.Constructed|1, code, diag))
		case ber.ClassApplication | 2: // unbind
			return
		case ber.ClassApplication | ber.Constructed | 3: // search
			if !bound {
				reply(c, id, ldapResult(ber.ClassApplication|ber.Constructed|5, ldap.ResultOperationsError,
					"000004DC: LdapErr: DSID-0C090A5C, comment: In order to perform this operation a successful bind must be completed on the connection., data 0, v4f7c\x00"))
				continue
			}
			s.search(c, id, op)
		default:
			return
		}
	}
}

func (s *Server) bind(name, pw string) (int, string) {
	local, domain, ok := strings.Cut(name, "@")
	if !ok || !strings.EqualFold(domain, s.dir.Domain) {
		return ldap.ResultInvalidCredentials, adBindError("52e")
	}
	u, ok := s.dir.Users[strings.ToLower(local)]
	switch {
	case !ok || pw == "" || u.Password != pw:
		return ldap.ResultInvalidCredentials, adBindError("52e")
	case u.Disabled:
		return ldap.ResultInvalidCredentials, adBindError("533")
	case u.Locked:
		return ldap.ResultInvalidCredentials, adBindError("775")
	}
	return 0, ""
}

type object struct {
	dn       string
	class    []string
	sam      string
	memberOf []string
}

func (s *Server) objects() []object {
	var out []object
	for sam, u := range s.dir.Users {
		out = append(out, object{u.DN, []string{"top", "person", "organizationalPerson", "user"}, sam, u.MemberOf})
	}
	for sam, g := range s.dir.Groups {
		out = append(out, object{g.DN, []string{"top", "group"}, sam, g.MemberOf})
	}
	return out
}

// chain devolve os grupos alcançáveis a partir dos diretos (o aninhamento).
func (s *Server) chain(direct []string) map[string]bool {
	seen := map[string]bool{}
	queue := append([]string(nil), direct...)
	for len(queue) > 0 {
		dn := strings.ToLower(queue[0])
		queue = queue[1:]
		if seen[dn] {
			continue
		}
		seen[dn] = true
		for _, g := range s.dir.Groups {
			if strings.EqualFold(g.DN, dn) {
				queue = append(queue, g.MemberOf...)
			}
		}
	}
	return seen
}

func (s *Server) match(f *ber.Packet, o object) bool {
	switch f.ID {
	case ber.ClassContext | ber.Constructed | 0: // and
		for _, c := range f.Children {
			if !s.match(c, o) {
				return false
			}
		}
		return true
	case ber.ClassContext | ber.Constructed | 3: // equalityMatch
		attr, val := strings.ToLower(f.Children[0].String()), f.Children[1].String()
		switch attr {
		case "objectclass":
			for _, c := range o.class {
				if strings.EqualFold(c, val) {
					return true
				}
			}
		case "samaccountname":
			return strings.EqualFold(o.sam, val)
		}
		return false
	case ber.ClassContext | ber.Constructed | 9: // extensibleMatch
		var rule, attr, val string
		for _, c := range f.Children {
			switch c.ID {
			case ber.ClassContext | 1:
				rule = c.String()
			case ber.ClassContext | 2:
				attr = c.String()
			case ber.ClassContext | 3:
				val = c.String()
			}
		}
		return rule == ldap.MatchingRuleInChain && strings.EqualFold(attr, "memberOf") && s.chain(o.memberOf)[strings.ToLower(val)]
	}
	return false
}

func describe(f *ber.Packet) string {
	switch f.ID {
	case ber.ClassContext | ber.Constructed | 0:
		var b strings.Builder
		b.WriteString("(&")
		for _, c := range f.Children {
			b.WriteString(describe(c))
		}
		return b.String() + ")"
	case ber.ClassContext | ber.Constructed | 3:
		return "(" + f.Children[0].String() + "=" + f.Children[1].String() + ")"
	case ber.ClassContext | ber.Constructed | 9:
		var parts [3]string
		for _, c := range f.Children {
			if t := c.Tag(); t >= 1 && t <= 3 {
				parts[t-1] = c.String()
			}
		}
		return "(" + parts[1] + ":" + parts[0] + ":=" + parts[2] + ")"
	}
	return "(?)"
}

func (s *Server) search(c net.Conn, id int64, op *ber.Packet) {
	base := op.Children[0].String()
	scope, _ := op.Children[1].Int()
	limit, _ := op.Children[3].Int()
	filter := op.Children[6]
	s.mu.Lock()
	s.Filters = append(s.Filters, describe(filter))
	s.mu.Unlock()

	n := 0
	for _, o := range s.objects() {
		inScope := strings.EqualFold(o.dn, base) ||
			(scope == ldap.ScopeSubtree && strings.HasSuffix(strings.ToLower(o.dn), ","+strings.ToLower(base)))
		if !inScope || !s.match(filter, o) {
			continue
		}
		if limit > 0 && int64(n) >= limit {
			reply(c, id, ldapResult(ber.ClassApplication|ber.Constructed|5, 4, ""))
			return
		}
		reply(c, id, ber.Cons(ber.ClassApplication|ber.Constructed|4, ber.Str(ber.TagOctetString, o.dn), ber.Cons(ber.TagSequence)))
		n++
	}
	if scope == ldap.ScopeSubtree {
		// Como o AD numa busca a partir da raiz do domínio.
		reply(c, id, ber.Cons(ber.ClassApplication|ber.Constructed|19, ber.Str(ber.TagOctetString, "ldaps://ForestDnsZones.exemplo.test/DC=ForestDnsZones,DC=exemplo,DC=test")))
	}
	reply(c, id, ldapResult(ber.ClassApplication|ber.Constructed|5, 0, ""))
}

func selfSigned(t testing.TB) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"},
		DNSNames:  []string{"localhost"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}
