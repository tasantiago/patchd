package inventory

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// Saída real do Mac Studio M2 Ultra do laboratório (Aula 0.3).
const systemVersionReal = `{"ProductBuildVersion":"25F84","ProductVersion":"26.5.2","BuildID":"3FF138BC-7035-11F1-A491-5FE68ADA1E26","ProductCopyright":"1983-2026 Apple Inc.","ProductName":"macOS","iOSSupportVersion":"26.5","ProductUserVisibleVersion":"26.5.2"}`

const comandoPlutil = "/usr/bin/plutil -convert json -o - /System/Library/CoreServices/SystemVersion.plist"

func TestDarwinOSComSaidaReal(t *testing.T) {
	run := runnerFalso{
		comandoPlutil:       {saida: systemVersionReal},
		"/usr/bin/uname -r": {saida: "25.5.0\n"},
	}
	got, err := collectDarwinOS(context.Background(), run, hostnameFalso)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if got.ID != "macos" || got.Name != "macOS" || got.Version != "26.5.2" || got.Build != "25F84" {
		t.Errorf("identificação errada: %+v", got)
	}
	if got.Kernel != "25.5.0" || got.Hostname != "maquina-teste" {
		t.Errorf("kernel ou hostname errados: %+v", got)
	}
	quer := []string{"/System/Library/CoreServices/SystemVersion.plist", "/usr/bin/uname -r"}
	if !reflect.DeepEqual(got.Sources, quer) {
		t.Errorf("fontes = %v, esperado %v", got.Sources, quer)
	}
}

func TestDarwinOSSemKernel(t *testing.T) {
	run := runnerFalso{
		comandoPlutil:       {saida: systemVersionReal},
		"/usr/bin/uname -r": {err: errors.New("falhou")},
	}
	got, err := collectDarwinOS(context.Background(), run, hostnameFalso)
	if err != nil {
		t.Fatalf("o kernel é informativo, não deveria falhar: %v", err)
	}
	if got.Kernel != "" || len(got.Sources) != 1 {
		t.Errorf("esperado sem kernel e com uma fonte: %+v", got)
	}
}

func TestDarwinOSFalhaNoPlutil(t *testing.T) {
	run := runnerFalso{comandoPlutil: {err: errors.New("plutil: arquivo não encontrado")}}
	if _, err := collectDarwinOS(context.Background(), run, hostnameFalso); err == nil {
		t.Error("esperado erro quando o plutil falha")
	}
}

func TestParseSystemVersionIncompleto(t *testing.T) {
	if _, err := parseSystemVersion([]byte(`{"ProductName":"macOS"}`)); err == nil {
		t.Error("esperado erro sem versão e build")
	}
	if _, err := parseSystemVersion([]byte(`não é json`)); err == nil {
		t.Error("esperado erro com JSON inválido")
	}
}
