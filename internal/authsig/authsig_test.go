package authsig

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/binary"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/tasantiago/patchd/internal/authsig/authsigtest"
)

// assinatura monta uma SignedData com a AC primeiro e o signatário depois (o signatário é
// achado pelo emissor e número de série, não pela posição).
func assinatura(t *testing.T) []byte {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	agora := time.Now()
	caTpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "AC de Teste", Organization: []string{"Autoridade"}},
		NotBefore: agora, NotAfter: agora.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTpl, caTpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(caDER)
	leafTpl := &x509.Certificate{SerialNumber: big.NewInt(77), Subject: pkix.Name{CommonName: "Fabricante Teste", Organization: []string{"Fabricante Teste Ltda"},
		Country: []string{"BR"}}, NotBefore: agora, NotAfter: agora.Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTpl, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	type signerInfo struct {
		Version   int
		Issuer    issuerSerial
		DigestAlg pkix.AlgorithmIdentifier
		SigAlg    pkix.AlgorithmIdentifier
		Sig       []byte
	}
	type signedData struct {
		Version     int
		DigestAlgs  []pkix.AlgorithmIdentifier `asn1:"set"`
		Content     struct{ Type asn1.ObjectIdentifier }
		Certs       asn1.RawValue
		SignerInfos []signerInfo `asn1:"set"`
	}
	sha256 := pkix.AlgorithmIdentifier{Algorithm: asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 1}}
	sd := signedData{Version: 1, DigestAlgs: []pkix.AlgorithmIdentifier{sha256},
		Certs: asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: append(append([]byte{}, caDER...), leafDER...)},
		SignerInfos: []signerInfo{{Version: 1, Issuer: issuerSerial{Issuer: asn1.RawValue{FullBytes: ca.RawSubject}, Serial: big.NewInt(77)},
			DigestAlg: sha256, SigAlg: sha256, Sig: []byte{1, 2, 3}}}}
	sd.Content.Type = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 311, 2, 1, 4}
	inner, err := asn1.Marshal(sd)
	if err != nil {
		t.Fatal(err)
	}
	der, err := asn1.Marshal(contentInfo{ContentType: oidSignedData, Content: asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: inner}})
	if err != nil {
		t.Fatal(err)
	}
	return der
}

// executavel: um PE32+ mínimo com a tabela de certificados no fim (sem tabela: sem assinatura).
func executavel(sig []byte) []byte {
	b := make([]byte, 64+4+20+240)
	copy(b, "MZ")
	binary.LittleEndian.PutUint32(b[0x3C:], 64)
	copy(b[64:], "PE\x00\x00")
	binary.LittleEndian.PutUint16(b[64+4:], 0x8664)
	binary.LittleEndian.PutUint16(b[64+4+16:], 240)
	opt := 64 + 24
	binary.LittleEndian.PutUint16(b[opt:], 0x20b)
	binary.LittleEndian.PutUint32(b[opt+108:], 16)
	b = append(b, bytes.Repeat([]byte{0x90}, 1000)...) // "código"
	if sig == nil {
		return b
	}
	for len(b)%8 != 0 {
		b = append(b, 0)
	}
	off := len(b)
	cert := make([]byte, 8)
	binary.LittleEndian.PutUint32(cert, uint32(8+len(sig)))
	binary.LittleEndian.PutUint16(cert[4:], 0x0200)
	binary.LittleEndian.PutUint16(cert[6:], 0x0002)
	cert = append(cert, sig...)
	for len(cert)%8 != 0 {
		cert = append(cert, 0)
	}
	b = append(b, cert...)
	binary.LittleEndian.PutUint32(b[opt+112+4*8:], uint32(off))
	binary.LittleEndian.PutUint32(b[opt+112+4*8+4:], uint32(len(cert)))
	return b
}

// msi: um arquivo composto (versão 3, setores de 512) com o fluxo \x05DigitalSignature, no
// fluxo mínimo (menor que 4096) ou em setores comuns (preenchido até 5000 bytes).
func msi(t *testing.T, sig []byte, grande bool) []byte {
	t.Helper()
	const ss = 512
	sector := func() []byte { return make([]byte, ss) }
	le32 := binary.LittleEndian.PutUint32
	fat, dir := sector(), sector()
	for i := 0; i < ss/4; i++ {
		le32(fat[i*4:], cfbFree)
	}
	le32(fat[0:], 0xFFFFFFFD) // o próprio setor da FAT
	le32(fat[4:], cfbEnd)     // o diretório
	entry := func(i int, name string, typ byte, start uint32, size int) {
		e := dir[i*dirEntry : (i+1)*dirEntry]
		u := utf16.Encode([]rune(name))
		for k, c := range u {
			binary.LittleEndian.PutUint16(e[k*2:], c)
		}
		binary.LittleEndian.PutUint16(e[64:], uint16((len(u)+1)*2))
		e[66] = typ
		le32(e[116:], start)
		binary.LittleEndian.PutUint64(e[120:], uint64(size))
	}
	var data [][]byte // setores a partir do 2
	chain := func(first int, n int) {
		for k := 0; k < n; k++ {
			next := uint32(first + k + 1)
			if k == n-1 {
				next = cfbEnd
			}
			le32(fat[(first+k)*4:], next)
		}
	}
	firstMini := uint32(cfbEnd)
	if grande {
		stream := append(append([]byte{}, sig...), make([]byte, 5000-len(sig))...)
		n := (len(stream) + ss - 1) / ss
		for k := 0; k < n; k++ {
			s := sector()
			copy(s, stream[k*ss:])
			data = append(data, s)
		}
		chain(2, n)
		entry(0, "Root Entry", 5, cfbEnd, 0)
		entry(1, "\x05DigitalSignature", 2, 2, len(stream))
	} else {
		if len(sig) >= 4096 {
			t.Fatal("assinatura grande demais para o fluxo mínimo")
		}
		m := (len(sig) + 63) / 64
		minifat := sector()
		for i := 0; i < ss/4; i++ {
			le32(minifat[i*4:], cfbFree)
		}
		for k := 0; k < m; k++ {
			next := uint32(k + 1)
			if k == m-1 {
				next = cfbEnd
			}
			le32(minifat[k*4:], next)
		}
		data = append(data, minifat)
		chain(2, 1)
		firstMini = 2
		root := make([]byte, m*64)
		copy(root, sig)
		n := (len(root) + ss - 1) / ss
		for k := 0; k < n; k++ {
			s := sector()
			copy(s, root[k*ss:])
			data = append(data, s)
		}
		chain(3, n)
		entry(0, "Root Entry", 5, 3, len(root))
		entry(1, "\x05SummaryInformation", 2, cfbEnd, 0)
		entry(2, "\x05DigitalSignature", 2, 0, len(sig))
	}
	h := make([]byte, 512)
	copy(h, cfbMagic)
	binary.LittleEndian.PutUint16(h[0x18:], 0x3E)
	binary.LittleEndian.PutUint16(h[0x1A:], 3)
	binary.LittleEndian.PutUint16(h[0x1C:], 0xFFFE)
	binary.LittleEndian.PutUint16(h[0x1E:], 9)
	binary.LittleEndian.PutUint16(h[0x20:], 6)
	le32(h[0x2C:], 1)
	le32(h[0x30:], 1)
	le32(h[0x38:], 4096)
	le32(h[0x3C:], firstMini)
	le32(h[0x44:], cfbEnd)
	for k := 0; k < 109; k++ {
		le32(h[0x4C+k*4:], cfbFree)
	}
	le32(h[0x4C:], 0)
	out := append(append(h, fat...), dir...)
	for _, s := range data {
		out = append(out, s...)
	}
	return out
}

func confere(t *testing.T, nome string, b []byte) {
	t.Helper()
	s, err := Read(bytes.NewReader(b), int64(len(b)))
	if err != nil || s.Organization != "Fabricante Teste Ltda" || s.CommonName != "Fabricante Teste" || !strings.Contains(s.Subject, "O=Fabricante Teste Ltda") {
		t.Errorf("%s: %+v %v", nome, s, err)
	}
}

func TestLerAssinatura(t *testing.T) {
	sig := assinatura(t)
	confere(t, "executável", executavel(sig))
	confere(t, "MSI (fluxo mínimo)", msi(t, sig, false))
	confere(t, "MSI (setores comuns)", msi(t, sig, true))
	if _, err := Read(bytes.NewReader(executavel(nil)), int64(len(executavel(nil)))); !errors.Is(err, ErrUnsigned) {
		t.Errorf("sem assinatura: %v", err)
	}
	semFluxo := msi(t, sig, false)
	copy(semFluxo[512+512+2*dirEntry:], make([]byte, 40)) // apaga o nome do fluxo da assinatura
	if _, err := Read(bytes.NewReader(semFluxo), int64(len(semFluxo))); !errors.Is(err, ErrUnsigned) {
		t.Errorf("MSI sem assinatura: %v", err)
	}
	if _, err := Read(strings.NewReader("PK\x03\x04 um zip"), 15); !errors.Is(err, ErrFormat) {
		t.Errorf("zip: %v", err)
	}
	// Arquivo cortado: erro, sem pânico.
	exe := executavel(sig)
	if _, err := Read(bytes.NewReader(exe[:len(exe)-100]), int64(len(exe)-100)); err == nil {
		t.Error("cortado")
	}
}

func TestLerAssinaturaPelaRede(t *testing.T) {
	sig := assinatura(t)
	exe := executavel(sig)
	// Um executável de 300 KB: a assinatura no fim, lida por pedaços.
	grande := executavel(nil)
	grande = append(grande, make([]byte, 300<<10)...)
	for len(grande)%8 != 0 {
		grande = append(grande, 0)
	}
	off := len(grande)
	cert := make([]byte, 8)
	binary.LittleEndian.PutUint32(cert, uint32(8+len(sig)))
	binary.LittleEndian.PutUint16(cert[6:], 2)
	grande = append(append(grande, cert...), sig...)
	binary.LittleEndian.PutUint32(grande[64+24+112+32:], uint32(off))
	binary.LittleEndian.PutUint32(grande[64+24+112+36:], uint32(8+len(sig)))

	var pedidos int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pedidos++
		switch r.URL.Path {
		case "/grande.exe":
			http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(grande))
		case "/sem-range.exe": // ignora o Range: o arquivo pequeno vem inteiro
			w.Write(exe)
		case "/sem-range-grande.exe":
			w.Header().Set("Content-Length", "99999999")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	s, err := Remote(ctx, srv.Client(), srv.URL+"/grande.exe", "patchd-teste")
	if err != nil || s.Organization != "Fabricante Teste Ltda" || pedidos > 4 {
		t.Errorf("por pedaços: %+v %v (%d pedidos)", s, err, pedidos)
	}
	if s, err := Remote(ctx, srv.Client(), srv.URL+"/sem-range.exe", ""); err != nil || s.Organization != "Fabricante Teste Ltda" {
		t.Errorf("sem Range: %+v %v", s, err)
	}
	if _, err := Remote(ctx, srv.Client(), srv.URL+"/sem-range-grande.exe", ""); !errors.Is(err, ErrNoRange) {
		t.Errorf("sem Range, grande: %v", err)
	}
	if _, err := Remote(ctx, srv.Client(), srv.URL+"/nada.exe", ""); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("404: %v", err)
	}
}

func TestInstaladorDeTeste(t *testing.T) {
	b := authsigtest.Exe("Org de Teste")
	if s, err := Read(bytes.NewReader(b), int64(len(b))); err != nil || s.Organization != "Org de Teste" {
		t.Errorf("authsigtest: %+v %v", s, err)
	}
}
