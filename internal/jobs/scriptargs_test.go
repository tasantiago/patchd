package jobs

import (
	"reflect"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/bundle"
)

func TestScriptArgsCaminhosEAspas(t *testing.T) {
	line := `-Destino "C:\Program Files\Empresa\" /quiet MODO=1 '' 'valor "literal"'`
	want := []string{"-Destino", `C:\Program Files\Empresa\`, "/quiet", "MODO=1", "", `valor "literal"`}
	got, err := SplitScriptArgs(line)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("parâmetros: %q, %v", got, err)
	}
	for _, runner := range []string{bundle.RunnerPowerShell, bundle.RunnerBash, bundle.RunnerVBScript, bundle.RunnerExe} {
		if err := ValidateScriptArgs(runner, got); err != nil {
			t.Errorf("%s: %v", runner, err)
		}
	}
	values := append(want, `aspas 'simples' e "duplas"`)
	back, err := SplitScriptArgs(FormatScriptArgs(values))
	if err != nil || !reflect.DeepEqual(back, values) {
		t.Fatalf("formatação não preservou argumentos: %q, %v", back, err)
	}
}

func TestScriptArgsRecusaEntradaInvalida(t *testing.T) {
	for _, line := range []string{`"sem fim`, "x\ny", "x\x00y", strings.Repeat("x", MaxScriptArgBytes+1), strings.Repeat("x ", 33)} {
		if _, err := SplitScriptArgs(line); err == nil {
			t.Errorf("aceitou %q", line)
		}
	}
	if err := ValidateScriptArgs(bundle.RunnerBash, []string{string([]byte{0xff})}); err == nil {
		t.Error("aceitou UTF-8 inválido")
	}
}

func TestBatchArgsNaoExecutamOperadores(t *testing.T) {
	for _, arg := range []string{`x&whoami`, `x|whoami`, `>x`, `<x`, `^x`, `%COMSPEC%`, `!PATH!`, `"x"`} {
		if err := ValidateScriptArgs(bundle.RunnerCmd, []string{arg}); err == nil {
			t.Errorf("Batch aceitou %q", arg)
		}
	}
	if err := ValidateScriptArgs(bundle.RunnerCmd, []string{`C:\Program Files\Empresa`, "/quiet", "MODO=1", ""}); err != nil {
		t.Fatal(err)
	}
	args := make([]string, 7)
	for i := range args {
		args[i] = strings.Repeat("x", 1024)
	}
	if err := ValidateScriptArgs(bundle.RunnerCmd, args); err == nil {
		t.Fatal("aceitou parâmetros sem margem para o teto de linha do cmd.exe")
	}
}
