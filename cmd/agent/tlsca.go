package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// agentTransport monta o transporte HTTP do agente (Aula 9.2). Sem caFile, nil: o Go usa
// as CAs do sistema (no Windows, o repositório de certificados da máquina, onde a GPO põe a
// CA corporativa). Com caFile (-ca-file), o agente confia SÓ nas CAs do arquivo: nem uma
// CA pública nem outra CA instalada na máquina valida o servidor. O arquivo leva só
// certificados; uma chave privada nele é recusada.
func agentTransport(caFile string) (http.RoundTripper, int, error) {
	if caFile == "" {
		return nil, 0, nil
	}
	pool, n, err := loadCAFile(caFile)
	if err != nil {
		return nil, 0, err
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return t, n, nil
}

// loadCAFile lê os certificados em PEM do arquivo.
func loadCAFile(path string) (*x509.CertPool, int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, fmt.Errorf("-ca-file: %w", err)
	}
	pool := x509.NewCertPool()
	n := 0
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			break
		}
		if strings.Contains(block.Type, "PRIVATE KEY") {
			return nil, 0, errors.New("-ca-file: o arquivo tem uma chave privada; ele deve ter só o certificado da CA (ca.crt)")
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, 0, fmt.Errorf("-ca-file: certificado ilegível: %w", err)
		}
		pool.AddCert(cert)
		n++
	}
	if n == 0 {
		return nil, 0, errors.New("-ca-file: nenhum certificado em PEM no arquivo")
	}
	return pool, n, nil
}
