// Package authsigtest monta instaladores assinados de mentira para os testes: um executável
// (PE32+) mínimo com uma assinatura PKCS#7 cujo certificado tem a organização pedida. A
// assinatura não confere nada (não há hash do arquivo nela): é só para ler quem assinou.
package authsigtest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/binary"
	"math/big"
	"time"
)

type issuerSerial struct {
	Issuer asn1.RawValue
	Serial *big.Int
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

type contentInfo struct {
	ContentType asn1.ObjectIdentifier
	Content     asn1.RawValue
}

// Signature: a SignedData com o certificado de O=org (autoassinado).
func Signature(org string) []byte {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	now := time.Now()
	tpl := &x509.Certificate{SerialNumber: big.NewInt(42), Subject: pkix.Name{CommonName: org, Organization: []string{org}},
		NotBefore: now, NotAfter: now.Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}
	cert, _ := x509.ParseCertificate(der)
	sha := pkix.AlgorithmIdentifier{Algorithm: asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 1}}
	sd := signedData{Version: 1, DigestAlgs: []pkix.AlgorithmIdentifier{sha},
		Certs: asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: der},
		SignerInfos: []signerInfo{{Version: 1, Issuer: issuerSerial{Issuer: asn1.RawValue{FullBytes: cert.RawIssuer}, Serial: big.NewInt(42)},
			DigestAlg: sha, SigAlg: sha, Sig: []byte{1}}}}
	sd.Content.Type = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 311, 2, 1, 4}
	inner, err := asn1.Marshal(sd)
	if err != nil {
		panic(err)
	}
	out, err := asn1.Marshal(contentInfo{ContentType: asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2},
		Content: asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: inner}})
	if err != nil {
		panic(err)
	}
	return out
}

// Exe: um executável mínimo assinado por O=org (org vazia: sem assinatura).
func Exe(org string) []byte {
	b := make([]byte, 64+24+240)
	copy(b, "MZ")
	binary.LittleEndian.PutUint32(b[0x3C:], 64)
	copy(b[64:], "PE\x00\x00")
	binary.LittleEndian.PutUint16(b[64+4:], 0x8664)
	binary.LittleEndian.PutUint16(b[64+4+16:], 240)
	opt := 64 + 24
	binary.LittleEndian.PutUint16(b[opt:], 0x20b)
	binary.LittleEndian.PutUint32(b[opt+108:], 16)
	b = append(b, make([]byte, 512)...)
	if org == "" {
		return b
	}
	sig := Signature(org)
	off := len(b)
	cert := make([]byte, 8, 8+len(sig)+8)
	binary.LittleEndian.PutUint32(cert, uint32(8+len(sig)))
	binary.LittleEndian.PutUint16(cert[4:], 0x0200)
	binary.LittleEndian.PutUint16(cert[6:], 0x0002)
	cert = append(cert, sig...)
	for len(cert)%8 != 0 {
		cert = append(cert, 0)
	}
	b = append(b, cert...)
	binary.LittleEndian.PutUint32(b[opt+112+32:], uint32(off))
	binary.LittleEndian.PutUint32(b[opt+112+36:], uint32(len(cert)))
	return b
}
