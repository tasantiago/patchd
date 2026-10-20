package ldap

import (
	"bufio"
	"bytes"
	"net"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/ber"
)

// Aula 20.3: as respostas do servidor LDAP, com o cliente de verdade. Para qualquer fluxo
// de bytes, o Bind e a busca terminam (com erro ou não) e não entram em pânico.
func FuzzRespostas(f *testing.F) {
	msg := func(id int64, op []byte) []byte { return ber.Cons(ber.TagSequence, ber.Int(ber.TagInteger, id), op) }
	ok := func(op byte) []byte {
		return ber.Cons(op, ber.Int(ber.TagEnumerated, 0), ber.Str(ber.TagOctetString, ""), ber.Str(ber.TagOctetString, ""))
	}
	entry := ber.Cons(opSearchEntry, ber.Str(ber.TagOctetString, "CN=Fulano,DC=exemplo"),
		ber.Cons(ber.TagSequence, ber.Cons(ber.TagSequence, ber.Str(ber.TagOctetString, "memberOf"),
			ber.Cons(ber.TagSet, ber.Str(ber.TagOctetString, "CN=g1,DC=exemplo")))))
	var bom []byte
	bom = append(bom, msg(1, ok(opBindResponse))...)
	bom = append(bom, msg(2, entry)...)
	bom = append(bom, msg(2, ber.Cons(opSearchReference, ber.Str(ber.TagOctetString, "ldap://x")))...)
	bom = append(bom, msg(2, ok(opSearchDone))...)
	f.Add(bom)
	f.Add(msg(0, ber.Cons(opExtendedResp, ber.Int(ber.TagEnumerated, 52))))
	f.Fuzz(func(t *testing.T, b []byte) {
		c := &Conn{c: fakeConn{}, r: bufio.NewReader(bytes.NewReader(b))}
		if err := c.Bind("fulano@exemplo", "senha"); err != nil {
			return
		}
		es, _ := c.SearchEntries(SearchRequest{BaseDN: "DC=exemplo", Filter: Equal("sAMAccountName", "fulano"),
			Attributes: []string{"memberOf"}})
		for _, e := range es {
			_ = e.First("memberof")
		}
	})
}

// fakeConn: o que o cliente escreve vai para lugar nenhum.
type fakeConn struct{}

func (fakeConn) Read([]byte) (int, error)         { return 0, net.ErrClosed }
func (fakeConn) Write(p []byte) (int, error)      { return len(p), nil }
func (fakeConn) Close() error                     { return nil }
func (fakeConn) LocalAddr() net.Addr              { return nil }
func (fakeConn) RemoteAddr() net.Addr             { return nil }
func (fakeConn) SetDeadline(time.Time) error      { return nil }
func (fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (fakeConn) SetWriteDeadline(time.Time) error { return nil }
