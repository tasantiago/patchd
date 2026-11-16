package appmain

import (
	"bytes"
	"strings"
	"testing"
)

// A ajuda e o erro de uso saem antes de abrir qualquer coisa (sem ícone, sem porta).
func TestUso(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Run([]string{"-h"}, &out, &errb, nil, ""); code != ExitOK || !strings.Contains(errb.String(), "uso: patchd-app") {
		t.Errorf("-h: %d %q", code, errb.String())
	}
	if code := Run([]string{"-nada"}, &out, &errb, nil, ""); code != ExitConfig {
		t.Errorf("opção desconhecida: %d", code)
	}
}
