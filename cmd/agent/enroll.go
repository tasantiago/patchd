package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/agent"
	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/credential"
	"github.com/tasantiago/patchd/internal/evidence"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/platform"
	"github.com/tasantiago/patchd/internal/protocol"
)

const (
	enrollTimeout     = 2 * time.Minute
	enrollHTTPTimeout = 30 * time.Second
	enrollMaxResponse = 1 << 20
	enrollMaxSkew     = 5 * time.Minute
)

// runEnroll registra a máquina no servidor e grava a credencial na pasta de dados.
// Aceita as mesmas opções do agente (-server, -data-dir...). O token de enrollment vem
// do ambiente, nunca de flag: flags aparecem na lista de processos e no histórico do shell.
func runEnroll(args []string, look config.Lookup, stdout, stderr io.Writer, agentVersion string) int {
	cfg, err := loadAgentConfig(args, look, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return exitOK
	}
	var usageErr *config.UsageError
	if errors.As(err, &usageErr) {
		return exitConfig
	}
	if err != nil {
		fmt.Fprintf(stderr, "patchd-agent: configuração inválida:\n%v\n", err)
		return exitConfig
	}
	if cfg.ServerURL == "" {
		fmt.Fprintln(stderr, "patchd-agent: informe o servidor (-server ou PATCHD_SERVER_URL)")
		return exitConfig
	}
	token, err := enrollToken(look)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-agent: %v\n", err)
		return exitConfig
	}

	// Já registrada: não registra de novo por engano (criaria outra máquina no servidor).
	if c, err := credential.Load(cfg.DataDir); err == nil {
		fmt.Fprintf(stderr, "patchd-agent: esta máquina já está registrada como %s (em %s); para registrar de novo, remova %s\n",
			c.MachineID, c.ServerURL, credential.Path(cfg.DataDir))
		return exitRuntime
	} else if !errors.Is(err, credential.ErrNotEnrolled) {
		fmt.Fprintf(stderr, "patchd-agent: %v\n", err)
		return exitRuntime
	}
	if err := credential.CheckWritable(cfg.DataDir); err != nil {
		fmt.Fprintf(stderr, "patchd-agent: %v\n", err)
		return exitRuntime
	}

	ctx, cancel := context.WithTimeout(context.Background(), enrollTimeout)
	defer cancel()

	ev, problems := evidence.Collect(ctx, platform.ExecRunner{})
	for _, p := range problems {
		fmt.Fprintf(stderr, "patchd-agent: aviso: %s\n", p)
	}

	resp, err := postEnroll(ctx, cfg.ServerURL, token, protocol.EnrollRequest{AgentVersion: agentVersion, Evidence: ev})
	if err != nil {
		fmt.Fprintf(stderr, "patchd-agent: registro falhou: %v\n", err)
		return exitRuntime
	}
	if skew := time.Since(resp.ServerTime); skew > enrollMaxSkew || skew < -enrollMaxSkew {
		fmt.Fprintf(stderr, "patchd-agent: aviso: o relógio desta máquina difere do servidor em %s\n", skew.Round(time.Second))
	}

	c := credential.Credential{
		MachineID:  resp.MachineID,
		Credential: resp.Credential,
		ServerURL:  cfg.ServerURL,
		EnrolledAt: time.Now().UTC(),
	}
	if err := credential.Save(cfg.DataDir, c); err != nil {
		// O servidor já registrou a máquina; sem a credencial, ela vira um registro órfão.
		fmt.Fprintf(stderr, "patchd-agent: o servidor registrou a máquina %s, mas a credencial não foi gravada: %v\n", resp.MachineID, err)
		return exitRuntime
	}

	fmt.Fprintf(stderr, "patchd-agent: máquina registrada; credencial gravada em %s\n", credential.Path(cfg.DataDir))
	fmt.Fprintln(stdout, resp.MachineID)
	return exitOK
}

// enrollToken lê o token de PATCHD_ENROLL_TOKEN_FILE (preferido: o arquivo pode ser
// apagado pelo instalador logo depois) ou de PATCHD_ENROLL_TOKEN.
func enrollToken(look config.Lookup) (string, error) {
	file, hasFile := look("PATCHD_ENROLL_TOKEN_FILE")
	value, hasValue := look("PATCHD_ENROLL_TOKEN")
	switch {
	case hasFile && hasValue:
		return "", errors.New("defina PATCHD_ENROLL_TOKEN_FILE ou PATCHD_ENROLL_TOKEN, não os dois")
	case hasFile:
		data, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("PATCHD_ENROLL_TOKEN_FILE: %w", err)
		}
		value = string(data)
	case !hasValue:
		return "", errors.New("token de enrollment ausente: defina PATCHD_ENROLL_TOKEN_FILE ou PATCHD_ENROLL_TOKEN")
	}
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, identity.EnrollmentPrefix) {
		return "", fmt.Errorf("o token de enrollment deve começar com %s", identity.EnrollmentPrefix)
	}
	return value, nil
}

// postEnroll envia o pedido de registro e interpreta a resposta pelo código HTTP.
func postEnroll(ctx context.Context, serverURL, token string, req protocol.EnrollRequest) (protocol.EnrollResponse, error) {
	var out protocol.EnrollResponse
	body, err := json.Marshal(req)
	if err != nil {
		return out, err
	}
	url := strings.TrimRight(serverURL, "/") + "/api/v1/enroll"
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return out, err
	}
	requestID := newRequestID()
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Authorization", "Bearer "+token)
	hr.Header.Set("X-Request-ID", requestID)
	hr.Header.Set("User-Agent", "patchd-agent/"+req.AgentVersion)

	resp, err := (&http.Client{Timeout: enrollHTTPTimeout}).Do(hr)
	if agent.ClosedWithoutResponse(err) {
		return out, fmt.Errorf("%w: a conexão abriu, mas foi fechada sem resposta "+
			"(em geral, nada escutando atrás de uma porta publicada do Docker ou de um proxy)", err)
	}
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, enrollMaxResponse))
	if err != nil {
		return out, err
	}

	switch resp.StatusCode {
	case http.StatusCreated:
		if err := json.Unmarshal(data, &out); err != nil {
			return out, fmt.Errorf("resposta ilegível do servidor: %w", err)
		}
		if out.MachineID == "" || !strings.HasPrefix(out.Credential, identity.CredentialPrefix) {
			return out, errors.New("resposta do servidor sem ID ou credencial válidos")
		}
		return out, nil
	case http.StatusUnauthorized:
		return out, fmt.Errorf("o servidor recusou o token de enrollment (desconhecido, vencido, esgotado ou revogado; "+
			"o motivo está no log do servidor, request_id %s)", requestID)
	default:
		var e protocol.ErrorResponse
		_ = json.Unmarshal(data, &e)
		msg := e.Error.Message
		if msg == "" {
			msg = strings.TrimSpace(string(data))
		}
		return out, fmt.Errorf("o servidor respondeu %d: %s (request_id %s)", resp.StatusCode, msg, requestID)
	}
}

// newRequestID gera o X-Request-ID: o mesmo valor aparece no log do servidor.
func newRequestID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
