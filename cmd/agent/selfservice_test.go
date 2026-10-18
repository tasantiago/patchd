package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/localapi"
	"github.com/tasantiago/patchd/internal/protocol"
)

// Aula 17.3: o "patchd-agent selfservice" fala pelo canal local; aqui, um canal falso.
func TestSelfServiceLinhaDeComando(t *testing.T) {
	var got localapi.Request
	resp := localapi.Response{}
	old := localCall
	t.Cleanup(func() { localCall = old })
	localCall = func(_ context.Context, r localapi.Request) (localapi.Response, error) {
		got = r
		return resp, nil
	}
	roda := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		rc := run(append([]string{"selfservice"}, args...), func(string) (string, bool) { return "", false }, &out, &errb)
		return rc, out.String(), errb.String()
	}

	resp = localapi.Response{OK: true, Account: `EXEMPLO\teste`, Groups: []string{`EXEMPLO\Domain Users`, `EXEMPLO\GG-Exemplo`}}
	if rc, out, _ := roda("whoami"); rc != 0 || got.Op != "whoami" || !strings.Contains(out, "grupos: 2") || strings.Contains(out, "GG-Exemplo") {
		t.Errorf("whoami: %d %q", rc, out)
	}
	if rc, out, _ := roda("whoami", "-groups"); rc != 0 || !strings.Contains(out, `  EXEMPLO\GG-Exemplo`) {
		t.Errorf("whoami -groups: %d %q", rc, out)
	}

	resp = localapi.Response{OK: true, Catalog: &protocol.SelfServiceCatalog{Items: []protocol.SelfServiceItem{
		{App: "7zip", Installed: "26.01", Target: "26.04", Action: "atualizar"}}, OpenJob: 9}}
	if rc, out, _ := roda("catalog"); rc != 0 || !strings.Contains(out, "7zip") || !strings.Contains(out, "1 programa(s)") || !strings.Contains(out, "job 9") {
		t.Errorf("catalog: %d %q", rc, out)
	}

	if rc, _, _ := roda("request", "-app", "7zip"); rc != exitConfig {
		t.Errorf("request sem -action: %d", rc)
	}
	resp = localapi.Response{OK: true, Result: &protocol.SelfServiceResponse{Status: "criado", JobID: 50, Message: "atualização pedida"}}
	rc, out, errs := roda("request", "-app", "7zip", "-action", "atualizar")
	if rc != 0 || got.App != "7zip" || got.Action != "atualizar" || !strings.Contains(out, "criado:") || !strings.Contains(errs, "job 50") {
		t.Errorf("request: %d %q %q", rc, out, errs)
	}
	resp = localapi.Response{OK: true, Result: &protocol.SelfServiceResponse{Status: "recusado", Message: "não disponível"}}
	if rc, _, _ := roda("request", "-app", "7zip", "-action", "instalar"); rc != exitRuntime {
		t.Errorf("recusado: %d", rc)
	}
	resp = localapi.Response{Code: localapi.CodeLimit, Error: "pedidos demais"}
	if rc, _, errs := roda("catalog"); rc != exitRuntime || !strings.Contains(errs, "pedidos demais (limite)") {
		t.Errorf("erro do canal: %d %q", rc, errs)
	}
	if rc, _, _ := roda("apagar"); rc != exitConfig {
		t.Errorf("comando desconhecido: %d", rc)
	}
}

// Aula 17.4: o app do usuário vem ligado; PATCHD_USER_APP=0 ou -user-app=false o tiram.
func TestConfigUserApp(t *testing.T) {
	env := func(m map[string]string) func(string) (string, bool) {
		return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
	}
	cfg, err := loadAgentConfig(nil, env(nil), io.Discard)
	if err != nil || !cfg.UserApp {
		t.Errorf("padrão: %v %v", cfg.UserApp, err)
	}
	if cfg, _ := loadAgentConfig(nil, env(map[string]string{"PATCHD_USER_APP": "0"}), io.Discard); cfg.UserApp {
		t.Error("PATCHD_USER_APP=0")
	}
	if cfg, _ := loadAgentConfig([]string{"-user-app=false"}, env(nil), io.Discard); cfg.UserApp {
		t.Error("-user-app=false")
	}
	if _, err := loadAgentConfig(nil, env(map[string]string{"PATCHD_USER_APP": "talvez"}), io.Discard); err == nil {
		t.Error("valor inválido")
	}
}
