package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestServiceUso(t *testing.T) {
	var out, errs bytes.Buffer
	if code := runService(nil, ambienteFalso(nil), &out, &errs); code != exitConfig || !strings.Contains(errs.String(), "install") {
		t.Errorf("sem comando: %d %s", code, errs.String())
	}
	errs.Reset()
	if code := runService([]string{"reiniciar"}, ambienteFalso(nil), &out, &errs); code != exitConfig || !strings.Contains(errs.String(), "desconhecido") {
		t.Errorf("comando desconhecido: %d %s", code, errs.String())
	}
}

func TestServiceRunSemRegistro(t *testing.T) {
	var out, errs bytes.Buffer
	code := runService([]string{"run", "-data-dir", t.TempDir(), "-log-format", "text"}, ambienteFalso(map[string]string{"PATCHD_ALLOW_CONTAINER": "1"}), &out, &errs)
	if code != exitRuntime || !strings.Contains(errs.String(), "patchd-agent enroll") {
		t.Errorf("sem credencial, o laço não começa e o log diz o que fazer: %d %s", code, errs.String())
	}
}

func TestContainerRecusadoNoLacoENoRegistro(t *testing.T) {
	antes := inContainer
	inContainer = func() (bool, string) { return true, "/.dockerenv" }
	t.Cleanup(func() { inContainer = antes })

	var out, errs bytes.Buffer
	code := runService([]string{"run", "-data-dir", t.TempDir(), "-log-format", "text"}, ambienteFalso(nil), &out, &errs)
	if code != exitConfig || !strings.Contains(errs.String(), "container") || !strings.Contains(errs.String(), "dockerenv") {
		t.Errorf("o laço recusa rodar em container e diz a evidência: %d %s", code, errs.String())
	}

	errs.Reset()
	amb := ambienteFalso(map[string]string{"PATCHD_ENROLL_TOKEN": "patchd_enr_x"})
	if code := runEnroll([]string{"-server", "http://127.0.0.1:1", "-data-dir", t.TempDir()}, amb, &out, &errs, "v"); code != exitConfig ||
		!strings.Contains(errs.String(), "container") {
		t.Errorf("o registro recusa container: %d %s", code, errs.String())
	}

	// Com a exceção de desenvolvimento, segue em frente (e para adiante, por falta de registro).
	errs.Reset()
	code = runService([]string{"run", "-data-dir", t.TempDir(), "-log-format", "text"},
		ambienteFalso(map[string]string{"PATCHD_ALLOW_CONTAINER": "1"}), &out, &errs)
	if code != exitRuntime || !strings.Contains(errs.String(), "PATCHD_ALLOW_CONTAINER") {
		t.Errorf("a exceção avisa e segue: %d %s", code, errs.String())
	}
}
