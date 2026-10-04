package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/credential"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

func ambienteFalso(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestEnrollToken(t *testing.T) {
	dir := t.TempDir()
	arq := filepath.Join(dir, "token")
	_ = os.WriteFile(arq, []byte("patchd_enr_abc\n"), 0o600)
	nada := ambienteFalso(nil)

	if tok, err := enrollToken(arq, nada); err != nil || tok != "patchd_enr_abc" {
		t.Errorf("pelo arquivo: %q %v", tok, err)
	}
	if tok, err := enrollToken("", ambienteFalso(map[string]string{"PATCHD_ENROLL_TOKEN": " patchd_enr_abc "})); err != nil || tok != "patchd_enr_abc" {
		t.Errorf("pela variável: %q %v", tok, err)
	}
	for nome, c := range map[string]struct {
		arquivo string
		amb     map[string]string
	}{
		"nenhum":           {"", nil},
		"os dois":          {arq, map[string]string{"PATCHD_ENROLL_TOKEN": "patchd_enr_abc"}},
		"tipo errado":      {"", map[string]string{"PATCHD_ENROLL_TOKEN": "patchd_mac_abc"}},
		"arquivo faltando": {filepath.Join(dir, "nao-existe"), nil},
	} {
		if _, err := enrollToken(c.arquivo, ambienteFalso(c.amb)); err == nil {
			t.Errorf("%s: deveria falhar", nome)
		}
	}

	// A variável PATCHD_ENROLL_TOKEN_FILE chega pela configuração, como padrão da flag.
	var errs bytes.Buffer
	cfg, err := loadAgentConfig(nil, ambienteFalso(map[string]string{"PATCHD_ENROLL_TOKEN_FILE": arq}), &errs)
	if err != nil || cfg.EnrollTokenFile != arq {
		t.Errorf("PATCHD_ENROLL_TOKEN_FILE: %q %v", cfg.EnrollTokenFile, err)
	}
	cfg, err = loadAgentConfig([]string{"-enroll-token-file", "outro"}, ambienteFalso(map[string]string{"PATCHD_ENROLL_TOKEN_FILE": arq}), &errs)
	if err != nil || cfg.EnrollTokenFile != "outro" {
		t.Errorf("a flag vence a variável: %q %v", cfg.EnrollTokenFile, err)
	}
}

// servidorDeTeste sobe a API de verdade (memória) num httptest.Server, com um token de 1 uso.
func servidorDeTeste(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	st := store.NewMemory()
	tok, h := identity.NewSecret(identity.EnrollmentPrefix)
	if _, err := st.CreateEnrollmentToken(context.Background(), h, "teste", time.Now().Add(time.Hour), 1); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.New(st, slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(srv.Close)
	return srv, tok
}

func TestPostEnroll(t *testing.T) {
	srv, tok := servidorDeTeste(t)
	req := protocol.EnrollRequest{AgentVersion: "v-teste",
		Evidence: protocol.IdentityEvidence{InstallID: "4b1f0c6e8a2d4e5f9a7b3c1d2e3f4a5b", OSFamily: "linux"}}

	resp, err := postEnroll(context.Background(), srv.URL+"/", tok, req)
	if err != nil || len(resp.MachineID) != 36 || !strings.HasPrefix(resp.Credential, identity.CredentialPrefix) {
		t.Fatalf("registro: %+v %v", resp, err)
	}
	_, err = postEnroll(context.Background(), srv.URL, tok, req)
	if err == nil || !strings.Contains(err.Error(), "recusou o token") || !strings.Contains(err.Error(), "request_id") {
		t.Errorf("token esgotado: %v", err)
	}
	req.Evidence = protocol.IdentityEvidence{OSFamily: "linux"}
	_, err = postEnroll(context.Background(), srv.URL, "patchd_enr_qualquer", req)
	if err == nil || !strings.Contains(err.Error(), "422") || !strings.Contains(err.Error(), "identidade da instalação") {
		t.Errorf("evidência insuficiente: %v", err)
	}
}

func TestRunEnrollFluxoNaMaquinaDoTeste(t *testing.T) {
	srv, tok := servidorDeTeste(t)
	dataDir := filepath.Join(t.TempDir(), "patchd")
	amb := ambienteFalso(map[string]string{"PATCHD_ENROLL_TOKEN": tok, "PATCHD_ALLOW_CONTAINER": "1"})
	args := []string{"-server", srv.URL, "-data-dir", dataDir}

	var out, errs bytes.Buffer
	code := runEnroll(args, amb, &out, &errs, "v-teste")
	if code == exitRuntime && strings.Contains(errs.String(), "422") {
		t.Skip("a máquina do teste não tem machine-id nem UUID legível (container sem root): " + errs.String())
	}
	if code != exitOK {
		t.Fatalf("código %d: %s", code, errs.String())
	}
	c, err := credential.Load(dataDir)
	if err != nil || c.MachineID != strings.TrimSpace(out.String()) || c.ServerURL != srv.URL {
		t.Fatalf("credencial gravada: %+v %v", c, err)
	}
	if strings.Contains(out.String()+errs.String(), c.Credential) {
		t.Error("a credencial nunca aparece na saída")
	}

	errs.Reset()
	if code := runEnroll(args, amb, &out, &errs, "v-teste"); code != exitRuntime || !strings.Contains(errs.String(), "já está registrada") {
		t.Errorf("segunda execução deve recusar: %d %s", code, errs.String())
	}
}

func TestIdentityImprimeJSON(t *testing.T) {
	var out, errs bytes.Buffer
	if code := runIdentity(nil, &out, &errs); code != exitOK {
		t.Fatalf("código %d: %s", code, errs.String())
	}
	var rep identityReport
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil || rep.Evidence.OSFamily == "" || rep.Problems == nil {
		t.Errorf("saída: %v %s", err, out.String())
	}
}

// fechaSemResponder simula a porta publicada do Docker com o servidor parado: aceita e
// fecha. Com lerAntes, fecha depois de receber o pedido; sem, fecha na hora.
func fechaSemResponder(t *testing.T, lerAntes bool) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			if lerAntes {
				buf := make([]byte, 4096)
				_, _ = c.Read(buf)
			}
			c.Close()
		}
	}()
	return "http://" + ln.Addr().String()
}

func TestPostEnrollConexaoFechadaSemResposta(t *testing.T) {
	// Os dois instantes de fechamento produzem erros diferentes no net/http; os dois
	// precisam virar a mesma explicação.
	for _, lerAntes := range []bool{true, false} {
		_, err := postEnroll(context.Background(), fechaSemResponder(t, lerAntes), "patchd_enr_x",
			protocol.EnrollRequest{Evidence: protocol.IdentityEvidence{OSFamily: "linux"}})
		if err == nil || !strings.Contains(err.Error(), "fechada sem resposta") {
			t.Errorf("lerAntes=%t: o erro deve explicar o fechamento: %v", lerAntes, err)
		}
	}
}

func TestClosedWithoutResponseNaoConfundeRecusa(t *testing.T) {
	// Porta fechada de verdade: recusa no "dial", que tem mensagem própria.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	_, err = postEnroll(context.Background(), "http://"+addr, "patchd_enr_x",
		protocol.EnrollRequest{Evidence: protocol.IdentityEvidence{OSFamily: "linux"}})
	if err == nil || strings.Contains(err.Error(), "fechada sem resposta") {
		t.Errorf("porta fechada não é \"fechada sem resposta\": %v", err)
	}
}
