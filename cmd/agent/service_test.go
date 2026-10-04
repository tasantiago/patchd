package main

import (
	"bytes"
	"runtime"
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
	code := runService([]string{"run", "-data-dir", t.TempDir(), "-log-format", "text"}, ambienteFalso(nil), &out, &errs)
	if code != exitRuntime || !strings.Contains(errs.String(), "patchd-agent enroll") {
		t.Errorf("sem credencial, o laço não começa e o log diz o que fazer: %d %s", code, errs.String())
	}
}

func TestServiceInstallForaDoWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no Windows, install fala com o gerenciador de serviços de verdade")
	}
	var out, errs bytes.Buffer
	if code := runService([]string{"install"}, ambienteFalso(nil), &out, &errs); code != exitConfig || !strings.Contains(errs.String(), "5.2") {
		t.Errorf("install fora do Windows: %d %s", code, errs.String())
	}
}
