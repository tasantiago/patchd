package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/version"
)

func TestRun(t *testing.T) {
	in := "1:3.5.17-1ubuntu0.26.04.2 3.5.17-1ubuntu0.26.04.2\n\n1.0~rc1\t1.0\n2.0 2.0\n:x 1.0\n"
	var out, errOut bytes.Buffer
	bad, err := run(version.Dpkg, strings.NewReader(in), &out, &errOut)
	if err != nil || bad != 1 {
		t.Fatalf("bad=%d err=%v", bad, err)
	}
	quer := "1:3.5.17-1ubuntu0.26.04.2 3.5.17-1ubuntu0.26.04.2 >\n1.0~rc1 1.0 <\n2.0 2.0 =\n:x 1.0 !\n"
	if out.String() != quer {
		t.Errorf("saída:\n%s\nesperado:\n%s", out.String(), quer)
	}
	if !strings.Contains(errOut.String(), "linha 5") {
		t.Errorf("erro sem a linha: %q", errOut.String())
	}

	if _, err := run(version.Scheme("deb"), strings.NewReader(""), &out, &errOut); err == nil {
		t.Error("esquema desconhecido deveria falhar")
	}
	if _, err := run(version.RPM, strings.NewReader("1.0 2.0 3.0\n"), &out, &errOut); err == nil {
		t.Error("três versões na linha deveria falhar")
	}
}
