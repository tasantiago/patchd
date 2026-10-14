// Package ldap é um cliente LDAPv3 mínimo (RFC 4511) para o login do painel pelo AD
// (Aula 7.3): conexão LDAPS, bind simples, busca e unbind. Nada de StartTLS, SASL,
// paginação ou escrita: o patchd só pergunta "esta senha confere?" e "é membro do grupo?".
//
// Os filtros são montados como estrutura (Equal, And, InChain), nunca a partir de texto:
// o valor digitado vai como OCTET STRING dentro do filtro e não há o que escapar, nem
// como um "*" ou um ")" no nome do usuário mudar a consulta (injeção de filtro LDAP).
package ldap

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/ber"
)

// Operações (RFC 4511, seção 4.2 em diante): [APPLICATION n].
const (
	opBindRequest     = ber.ClassApplication | ber.Constructed | 0
	opBindResponse    = ber.ClassApplication | ber.Constructed | 1
	opUnbindRequest   = ber.ClassApplication | 2
	opSearchRequest   = ber.ClassApplication | ber.Constructed | 3
	opSearchEntry     = ber.ClassApplication | ber.Constructed | 4
	opSearchDone      = ber.ClassApplication | ber.Constructed | 5
	opSearchReference = ber.ClassApplication | ber.Constructed | 19
	opExtendedResp    = ber.ClassApplication | ber.Constructed | 24
)

// Códigos de resultado usados aqui (RFC 4511, apêndice A).
const (
	ResultSuccess            = 0
	ResultOperationsError    = 1
	ResultInvalidCredentials = 49
	ResultBusy               = 51
	ResultUnavailable        = 52
)

// Escopos da busca.
const (
	ScopeBase    = 0
	ScopeSubtree = 2
)

// MatchingRuleInChain é a regra do AD que segue os grupos aninhados (LDAP_MATCHING_RULE_IN_CHAIN):
// "memberOf:1.2.840.113556.1.4.1941:=<DN do grupo>" casa com quem é membro do grupo
// direta ou indiretamente.
const MatchingRuleInChain = "1.2.840.113556.1.4.1941"

// ResultError é uma resposta do servidor diferente de sucesso.
type ResultError struct {
	Code    int
	Message string // diagnosticMessage: no AD, traz o motivo ("data 52e" etc.)
}

func (e *ResultError) Error() string {
	return fmt.Sprintf("LDAP resultado %d: %s", e.Code, e.Message)
}

// ErrDisconnected: o servidor avisou que vai encerrar a conexão (Notice of Disconnection).
var ErrDisconnected = errors.New("o servidor LDAP encerrou a conexão")

// Conn é uma conexão LDAPS. Não é segura para uso concorrente: cada login abre a sua.
type Conn struct {
	c      net.Conn
	r      *bufio.Reader
	nextID int64
	// TLSVersion é a versão negociada (tls.VersionTLS12, tls.VersionTLS13).
	TLSVersion uint16
}

// DialTLS conecta em addr (host:porta) com TLS desde o primeiro byte (LDAPS) e confere
// o certificado com cfg. O prazo do contexto vale para a conexão e para todas as
// operações seguintes.
func DialTLS(ctx context.Context, addr string, cfg *tls.Config) (*Conn, error) {
	d := tls.Dialer{NetDialer: &net.Dialer{}, Config: cfg}
	nc, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = nc.SetDeadline(dl)
	} else {
		_ = nc.SetDeadline(time.Now().Add(30 * time.Second))
	}
	tc := nc.(*tls.Conn)
	return &Conn{c: nc, r: bufio.NewReader(nc), TLSVersion: tc.ConnectionState().Version}, nil
}

// Close envia o unbind (sem resposta, RFC 4511 4.3) e fecha a conexão.
func (c *Conn) Close() error {
	_ = c.send(ber.TLV(opUnbindRequest, nil))
	return c.c.Close()
}

func (c *Conn) send(op []byte) error {
	c.nextID++
	_, err := c.c.Write(ber.Cons(ber.TagSequence, ber.Int(ber.TagInteger, c.nextID), op))
	return err
}

// read devolve a próxima resposta da operação em andamento (ID atual). Um aviso de
// desconexão (ID 0) vira ErrDisconnected.
func (c *Conn) read() (*ber.Packet, error) {
	for {
		raw, err := ber.ReadMessage(c.r)
		if err != nil {
			return nil, err
		}
		msg, err := ber.Parse(raw)
		if err != nil {
			return nil, err
		}
		if msg.ID != ber.TagSequence || len(msg.Children) < 2 {
			return nil, errors.New("ldap: mensagem fora do formato")
		}
		id, err := msg.Children[0].Int()
		if err != nil {
			return nil, err
		}
		op := msg.Children[1]
		if id == 0 && op.ID == opExtendedResp {
			return nil, ErrDisconnected
		}
		if id != c.nextID {
			continue // resposta atrasada de outra operação: não existe neste cliente, mas não confunde
		}
		return op, nil
	}
}

// result interpreta um LDAPResult (resultCode, matchedDN, diagnosticMessage).
func result(op *ber.Packet) error {
	if len(op.Children) < 3 || op.Children[0].ID != ber.TagEnumerated {
		return errors.New("ldap: LDAPResult fora do formato")
	}
	code, err := op.Children[0].Int()
	if err != nil {
		return err
	}
	if code == ResultSuccess {
		return nil
	}
	return &ResultError{Code: int(code), Message: op.Children[2].String()}
}

// Bind autentica com DN (ou, no AD, UPN "usuario@dominio") e senha. Senha vazia é
// recusada aqui: no LDAP, um bind simples com nome e sem senha é um bind "não
// autenticado" que o servidor pode aceitar sem conferir nada (RFC 4513, 5.1.2).
func (c *Conn) Bind(name, password string) error {
	if password == "" {
		return errors.New("ldap: senha vazia não é aceita (bind não autenticado)")
	}
	if err := c.send(ber.Cons(opBindRequest,
		ber.Int(ber.TagInteger, 3),
		ber.Str(ber.TagOctetString, name),
		ber.Str(ber.ClassContext|0, password), // simple [0]
	)); err != nil {
		return err
	}
	op, err := c.read()
	if err != nil {
		return err
	}
	if op.ID != opBindResponse {
		return fmt.Errorf("ldap: resposta inesperada ao bind (0x%02x)", op.ID)
	}
	return result(op)
}

// Filter é um filtro de busca já codificado.
type Filter []byte

// And casa quando todos os filtros casam.
func And(fs ...Filter) Filter {
	parts := make([][]byte, len(fs))
	for i, f := range fs {
		parts[i] = f
	}
	return Filter(ber.Cons(ber.ClassContext|0, parts...))
}

// Equal casa atributo = valor (equalityMatch).
func Equal(attr, value string) Filter {
	return Filter(ber.Cons(ber.ClassContext|3, ber.Str(ber.TagOctetString, attr), ber.Str(ber.TagOctetString, value)))
}

// InChain casa quem tem attr apontando para dn direta ou indiretamente (regra do AD).
func InChain(attr, dn string) Filter {
	return Filter(ber.Cons(ber.ClassContext|9, // extensibleMatch
		ber.Str(ber.ClassContext|1, MatchingRuleInChain),
		ber.Str(ber.ClassContext|2, attr),
		ber.Str(ber.ClassContext|3, dn),
	))
}

// SearchRequest é uma busca. Attributes vazio pede só os DNs (atributo "1.1").
type SearchRequest struct {
	BaseDN     string
	Scope      int
	Filter     Filter
	SizeLimit  int
	TimeLimit  int      // segundos
	Attributes []string // atributos pedidos (Aula 15.5); vazio: nenhum
}

// Entry é uma entrada encontrada: o DN e os atributos pedidos, pelo nome em minúsculas.
type Entry struct {
	DN    string
	Attrs map[string][]string
}

// First devolve o primeiro valor do atributo, ou vazio.
func (e Entry) First(attr string) string {
	if v := e.Attrs[strings.ToLower(attr)]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// Search devolve os DNs das entradas encontradas, sem atributos. Com um resultado de
// erro no fim (limite de tamanho, por exemplo), devolve também o que já veio.
func (c *Conn) Search(req SearchRequest) ([]string, error) {
	req.Attributes = nil
	entries, err := c.SearchEntries(req)
	dns := make([]string, len(entries))
	for i, e := range entries {
		dns[i] = e.DN
	}
	return dns, err
}

// SearchEntries devolve as entradas encontradas com os atributos pedidos. As referências
// (o AD devolve as de ForestDnsZones e DomainDnsZones numa busca a partir da raiz do
// domínio) são ignoradas.
func (c *Conn) SearchEntries(req SearchRequest) ([]Entry, error) {
	attrs := [][]byte{ber.Str(ber.TagOctetString, "1.1")} // nenhum atributo: só o DN
	if len(req.Attributes) > 0 {
		attrs = attrs[:0]
		for _, a := range req.Attributes {
			attrs = append(attrs, ber.Str(ber.TagOctetString, a))
		}
	}
	if err := c.send(ber.Cons(opSearchRequest,
		ber.Str(ber.TagOctetString, req.BaseDN),
		ber.Int(ber.TagEnumerated, int64(req.Scope)),
		ber.Int(ber.TagEnumerated, 0), // neverDerefAliases
		ber.Int(ber.TagInteger, int64(req.SizeLimit)),
		ber.Int(ber.TagInteger, int64(req.TimeLimit)),
		ber.Bool(ber.TagBoolean, false),
		req.Filter,
		ber.Cons(ber.TagSequence, attrs...),
	)); err != nil {
		return nil, err
	}
	var entries []Entry
	for {
		op, err := c.read()
		if err != nil {
			return nil, err
		}
		switch op.ID {
		case opSearchEntry:
			e, err := parseEntry(op)
			if err != nil {
				return nil, err
			}
			entries = append(entries, e)
		case opSearchReference:
			// continuação em outro servidor: o login não segue referências
		case opSearchDone:
			return entries, result(op)
		default:
			return nil, fmt.Errorf("ldap: resposta inesperada à busca (0x%02x)", op.ID)
		}
	}
}

// parseEntry lê um SearchResultEntry: objectName e a lista de PartialAttribute (tipo e
// conjunto de valores).
func parseEntry(op *ber.Packet) (Entry, error) {
	if len(op.Children) < 1 || op.Children[0].ID != ber.TagOctetString {
		return Entry{}, errors.New("ldap: SearchResultEntry fora do formato")
	}
	e := Entry{DN: op.Children[0].String()}
	if len(op.Children) < 2 {
		return e, nil
	}
	for _, pa := range op.Children[1].Children {
		if pa.ID != ber.TagSequence || len(pa.Children) < 2 || pa.Children[0].ID != ber.TagOctetString || pa.Children[1].ID != ber.TagSet {
			return Entry{}, errors.New("ldap: atributo fora do formato")
		}
		if e.Attrs == nil {
			e.Attrs = map[string][]string{}
		}
		name := strings.ToLower(pa.Children[0].String())
		for _, v := range pa.Children[1].Children {
			e.Attrs[name] = append(e.Attrs[name], v.String())
		}
	}
	return e, nil
}
