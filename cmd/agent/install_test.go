package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/credential"
)

func TestServiceArgsTiraServidorEToken(t *testing.T) {
	casos := []struct{ args, quer []string }{
		{[]string{"-server", "https://s", "-enroll-token-file", "t.txt", "-checkin-interval", "30m"}, []string{"-checkin-interval", "30m"}},
		{[]string{"-server=https://s", "--enroll-token-file=t.txt", "-log-level", "debug"}, []string{"-log-level", "debug"}},
		{[]string{"--server", "https://s", "-data-dir", `C:\dados`}, []string{"-data-dir", `C:\dados`}},
		{nil, []string{}},
	}
	for _, c := range casos {
		if got := serviceArgs(c.args); strings.Join(got, "|") != strings.Join(c.quer, "|") {
			t.Errorf("serviceArgs(%q) = %q; quer %q", c.args, got, c.quer)
		}
	}
}

func TestLockDataDirEsperaEDesiste(t *testing.T) {
	dir := t.TempDir()
	var errs bytes.Buffer
	unlock, err := lockDataDir(dir, 0, &errs)
	if err != nil {
		t.Fatal(err)
	}

	// Com a trava tomada e prazo zero: desiste na hora, dizendo por quê.
	if _, err := lockDataDir(dir, 0, &errs); err == nil || !strings.Contains(err.Error(), "em andamento") {
		t.Fatalf("segunda trava: %v", err)
	}

	// Com prazo: avisa que está esperando e consegue assim que a primeira é solta.
	go func() { time.Sleep(700 * time.Millisecond); unlock() }()
	errs.Reset()
	unlock2, err := lockDataDir(dir, 10*time.Second, &errs)
	if err != nil || !strings.Contains(errs.String(), "aguardando") {
		t.Fatalf("espera: %v %q", err, errs.String())
	}
	unlock2()
}

func TestEnsureEnrolledRepetivel(t *testing.T) {
	srv, tok := servidorDeTeste(t) // token de um uso só
	dataDir := filepath.Join(t.TempDir(), "patchd")
	arq := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(arq, []byte(tok), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadAgentConfig([]string{"-server", srv.URL, "-data-dir", dataDir, "-enroll-token-file", arq}, ambienteFalso(nil), &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}

	var out, errs bytes.Buffer
	c, code := ensureEnrolled(cfg, ambienteFalso(nil), &out, &errs, "v-teste")
	if code == exitRuntime && strings.Contains(errs.String(), "422") {
		t.Skip("a máquina do teste não tem machine-id nem UUID legível (container sem root): " + errs.String())
	}
	if code != exitOK || !strings.Contains(out.String(), "registrada como "+c.MachineID) {
		t.Fatalf("primeiro registro: %d %s %s", code, out.String(), errs.String())
	}

	// Segunda vez, como no boot seguinte: não registra, nem precisa de token (o arquivo some).
	_ = os.Remove(arq)
	out.Reset()
	c2, code := ensureEnrolled(cfg, ambienteFalso(nil), &out, &errs, "v-teste")
	if code != exitOK || c2.MachineID != c.MachineID || !strings.Contains(out.String(), "já registrada") {
		t.Errorf("segunda vez: %d %s %s", code, out.String(), errs.String())
	}

	// Credencial de outro servidor: recusa, em vez de registrar de novo por conta própria.
	cfg.ServerURL = "http://127.0.0.1:1"
	errs.Reset()
	if _, code := ensureEnrolled(cfg, ambienteFalso(nil), &out, &errs, "v-teste"); code != exitConfig || !strings.Contains(errs.String(), "outro servidor") {
		t.Errorf("outro servidor: %d %s", code, errs.String())
	}
}

func TestEnsureEnrolledSemServidorOuToken(t *testing.T) {
	var out, errs bytes.Buffer
	cfg := agentConfig{DataDir: t.TempDir()}
	if _, code := ensureEnrolled(cfg, ambienteFalso(nil), &out, &errs, "v"); code != exitConfig || !strings.Contains(errs.String(), "-server") {
		t.Errorf("sem servidor: %d %s", code, errs.String())
	}
	errs.Reset()
	cfg.ServerURL = "http://127.0.0.1:1"
	if _, code := ensureEnrolled(cfg, ambienteFalso(nil), &out, &errs, "v"); code != exitConfig || !strings.Contains(errs.String(), "token de enrollment ausente") {
		t.Errorf("sem token: %d %s", code, errs.String())
	}
}

// Dois instaladores ao mesmo tempo (o script da GPO e um job do ITOM no mesmo boot), com
// um token de UM uso: com a trava, o segundo espera, encontra a credencial e não tenta
// registrar. Sem a trava, os dois tentariam, e um deles levaria 401 (ou, com um token de
// mais usos, a máquina seria registrada duas vezes).
func TestInstalacoesSimultaneasRegistramUmaVez(t *testing.T) {
	srv, tok := servidorDeTeste(t)
	dataDir := filepath.Join(t.TempDir(), "patchd")
	amb := ambienteFalso(map[string]string{"PATCHD_ENROLL_TOKEN": tok})
	cfg, err := loadAgentConfig([]string{"-server", srv.URL, "-data-dir", dataDir}, amb, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}

	type resultado struct {
		code     int
		out, err string
	}
	res := make([]resultado, 2)
	var wg sync.WaitGroup
	for i := range res {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var out, errs bytes.Buffer
			unlock, err := lockDataDir(dataDir, 30*time.Second, &errs)
			if err != nil {
				res[i] = resultado{exitRuntime, "", err.Error()}
				return
			}
			defer unlock()
			_, code := ensureEnrolled(cfg, amb, &out, &errs, "v-teste")
			res[i] = resultado{code, out.String(), errs.String()}
		}()
	}
	wg.Wait()

	for _, r := range res {
		if r.code == exitRuntime && strings.Contains(r.err, "422") {
			t.Skip("a máquina do teste não tem machine-id nem UUID legível (container sem root)")
		}
		if r.code != exitOK {
			t.Fatalf("as duas instalações terminam bem: %+v", res)
		}
	}
	novos := strings.Count(res[0].out+res[1].out, "máquina registrada como")
	existentes := strings.Count(res[0].out+res[1].out, "já registrada como")
	if novos != 1 || existentes != 1 {
		t.Errorf("um registro e um reaproveitamento: %+v", res)
	}
	if _, err := credential.Load(dataDir); err != nil {
		t.Error(err)
	}
}

func TestSameExecutable(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := sameExecutable(self); err != nil || !ok {
		t.Errorf("o próprio executável: %t %v", ok, err)
	}
	outro := filepath.Join(t.TempDir(), "outro")
	_ = os.WriteFile(outro, []byte("não é o agente"), 0o755)
	if ok, err := sameExecutable(outro); err != nil || ok {
		t.Errorf("outro arquivo: %t %v", ok, err)
	}
	if ok, err := sameExecutable(filepath.Join(t.TempDir(), "ausente")); err != nil || ok {
		t.Errorf("ausente: %t %v", ok, err)
	}
}

func TestInstallRecusaContainer(t *testing.T) {
	antes := inContainer
	inContainer = func() (bool, string) { return true, "/.dockerenv" }
	t.Cleanup(func() { inContainer = antes })
	dir := t.TempDir()
	var out, errs bytes.Buffer
	code := runInstall([]string{"-data-dir", dir}, ambienteFalso(nil), &out, &errs, "v")
	// Sem root, para antes (exige administrador); com root, para no container. Nos dois
	// casos, nada é registrado nem instalado.
	if code == exitOK || (!strings.Contains(errs.String(), "container") && !strings.Contains(errs.String(), "root")) {
		t.Errorf("install em container: %d %s", code, errs.String())
	}
	if _, err := credential.Load(dir); !errors.Is(err, credential.ErrNotEnrolled) {
		t.Errorf("nada registrado: %v", err)
	}
}
