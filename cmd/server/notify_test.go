package main

import (
	"bufio"
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/alerts"
	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/store"
)

func ambiente(m map[string]string) config.Lookup {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

// Aula 18.5: a configuração pelo ambiente.
func TestNotifyConfig(t *testing.T) {
	if _, on, err := notifyConfig(ambiente(nil)); on || err != nil {
		t.Fatalf("sem PATCHD_SMTP_ADDR: on=%v err=%v", on, err)
	}
	senha := filepath.Join(t.TempDir(), "smtp.pass")
	os.WriteFile(senha, []byte("segredo\n"), 0o600)
	c, on, err := notifyConfig(ambiente(map[string]string{"PATCHD_SMTP_ADDR": "smtp.exemplo:587", "PATCHD_SMTP_FROM": "patchd <patchd@exemplo>",
		"PATCHD_NOTIFY_TO": "ti@exemplo; Fulano <fulano@exemplo>, ", "PATCHD_SMTP_USER": "patchd", "PATCHD_SMTP_PASSWORD_FILE": senha, "PATCHD_SMTP_TLS": "STARTTLS"}))
	if !on || err != nil || len(c.To) != 2 || c.To[1] != "Fulano <fulano@exemplo>" || c.Password != "segredo" || c.TLS != "starttls" {
		t.Fatalf("completa: %+v %v", c, err)
	}
	base := map[string]string{"PATCHD_SMTP_ADDR": "smtp.exemplo:587", "PATCHD_SMTP_FROM": "patchd@exemplo", "PATCHD_NOTIFY_TO": "ti@exemplo"}
	for nome, extra := range map[string]map[string]string{
		"usuário sem senha":  {"PATCHD_SMTP_USER": "patchd"},
		"senha sem usuário":  {"PATCHD_SMTP_PASSWORD": "x"},
		"senha duas vezes":   {"PATCHD_SMTP_USER": "patchd", "PATCHD_SMTP_PASSWORD": "x", "PATCHD_SMTP_PASSWORD_FILE": senha},
		"CA que não existe":  {"PATCHD_SMTP_CA_FILE": "/nao/existe.pem"},
		"CA sem certificado": {"PATCHD_SMTP_CA_FILE": senha},
	} {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		if _, _, err := notifyConfig(ambiente(m)); err == nil {
			t.Errorf("%s: aceito", nome)
		}
	}
	// Relay sem login: o arquivo de senha vazio que o compose monta não atrapalha.
	vazio := filepath.Join(t.TempDir(), "smtp_password")
	os.WriteFile(vazio, nil, 0o600)
	m := map[string]string{"PATCHD_SMTP_PASSWORD_FILE": vazio}
	for k, v := range base {
		m[k] = v
	}
	if c, _, err := notifyConfig(ambiente(m)); err != nil || c.Password != "" {
		t.Errorf("relay com o arquivo vazio: %v", err)
	}
	// A validação do pacote notify vale na partida: destinatário faltando.
	if _, err := newMailer(ambiente(map[string]string{"PATCHD_SMTP_ADDR": "smtp.exemplo:587", "PATCHD_SMTP_FROM": "patchd@exemplo"})); err == nil {
		t.Error("sem PATCHD_NOTIFY_TO: aceito")
	}
}

// relaySemTLS: um relay interno mínimo (sem STARTTLS), que conta as mensagens.
func relaySemTLS(t *testing.T) (string, func() int) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var mu sync.Mutex
	n := 0
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				r := bufio.NewReader(c)
				c.Write([]byte("220 relay\r\n"))
				inData := false
				for {
					l, err := r.ReadString('\n')
					if err != nil {
						return
					}
					switch {
					case inData && l == ".\r\n":
						inData = false
						mu.Lock()
						n++
						mu.Unlock()
						c.Write([]byte("250 ok\r\n"))
					case inData:
					case strings.HasPrefix(l, "DATA"):
						inData = true
						c.Write([]byte("354 manda\r\n"))
					case strings.HasPrefix(l, "QUIT"):
						c.Write([]byte("221 tchau\r\n"))
						return
					default:
						c.Write([]byte("250 ok\r\n"))
					}
				}
			}()
		}
	}()
	return ln.Addr().String(), func() int { mu.Lock(); defer mu.Unlock(); return n }
}

func TestRunNotify(t *testing.T) {
	tr := &trilhaFalsa{}
	old := cliAuditOpen
	t.Cleanup(func() { cliAuditOpen = old })
	cliAuditOpen = func(config.Lookup) (auditRecorder, func(), error) { return tr, func() {}, nil }

	var out, errb bytes.Buffer
	if code := run([]string{"notify", "status"}, ambiente(nil), &out, &errb); code != exitOK || !strings.Contains(out.String(), "desligadas") {
		t.Errorf("status desligado: %d %s", code, out.String())
	}
	addr, enviadas := relaySemTLS(t)
	env := ambiente(map[string]string{"PATCHD_SMTP_ADDR": addr, "PATCHD_SMTP_FROM": "patchd@exemplo", "PATCHD_NOTIFY_TO": "ti@exemplo",
		"PATCHD_SMTP_TLS": "none", "PATCHD_OPERATOR": "thyago"})
	out.Reset()
	if code := run([]string{"notify", "status"}, env, &out, &errb); code != exitOK || !strings.Contains(out.String(), "(none) · de patchd@exemplo · 1 destinatário(s)") {
		t.Errorf("status: %d %s", code, out.String())
	}
	if len(tr.ev) != 0 {
		t.Error("status entrou na trilha")
	}
	out.Reset()
	if code := run([]string{"notify", "test"}, env, &out, &errb); code != exitOK || enviadas() != 1 || !strings.Contains(out.String(), "aceita pelo SMTP") {
		t.Fatalf("test: %d %s %s", code, out.String(), errb.String())
	}
	if len(tr.ev) != 1 || tr.ev[0].Action != "cli.notify.test" || tr.ev[0].Detail != "ok" {
		t.Errorf("trilha: %+v", tr.ev)
	}
	// O padrão é starttls: o relay sem TLS é recusado, e a falha também fica na trilha.
	env2 := ambiente(map[string]string{"PATCHD_SMTP_ADDR": addr, "PATCHD_SMTP_FROM": "patchd@exemplo", "PATCHD_NOTIFY_TO": "ti@exemplo"})
	errb.Reset()
	if code := run([]string{"notify", "test"}, env2, &out, &errb); code != exitRuntime || !strings.Contains(errb.String(), "não oferece STARTTLS") {
		t.Errorf("starttls exigido: %d %s", code, errb.String())
	}
	if len(tr.ev) != 2 || !strings.Contains(tr.ev[1].Detail, "falhou") {
		t.Errorf("falha na trilha: %+v", tr.ev)
	}
}

// Aula 18.6: a prévia pelo terminal e o tipo de aviso desconhecido.
func TestNotifyPending(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	var out, errb bytes.Buffer
	w := &alerts.Watcher{Store: st, Now: time.Now, PublicURL: "https://patchd.exemplo:8443"}
	if code := printPending(ctx, w, &out, &errb); code != exitOK || !strings.Contains(out.String(), "nada novo") {
		t.Errorf("vazio: %d %s", code, out.String())
	}
	_, _ = st.CreateSelfServiceRequest(ctx, store.SelfServiceRequest{MachineID: "x", Account: `EXEMPLO\fulano`, AppID: "7zip", Action: "instalar", Version: "24.08"})
	out.Reset()
	if code := printPending(ctx, w, &out, &errb); code != exitOK || !strings.HasPrefix(out.String(), "== [pedidos] [patchd] 1 pedido(s) aguardando a TI\n") ||
		!strings.Contains(out.String(), "https://patchd.exemplo:8443/painel/autoatendimento") {
		t.Errorf("prévia: %d %s", code, out.String())
	}
	errb.Reset()
	if code := run([]string{"notify", "status"}, ambiente(map[string]string{"PATCHD_NOTIFY_EVENTS": "pedidos,tudo"}), &out, &errb); code != exitConfig || !strings.Contains(errb.String(), `"tudo" desconhecido`) {
		t.Errorf("tipo desconhecido: %d %s", code, errb.String())
	}
}
