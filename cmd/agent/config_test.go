package main

import (
	"io"
	"slices"
	"strings"
	"testing"
)

// Aula 20.5b: a lista de organizações aceitas, pela flag ou pela variável.
func TestListaDeAssinaturas(t *testing.T) {
	look := ambienteFalso(map[string]string{"PATCHD_ALLOWED_SIGNERS": "Mozilla Corporation"})
	cfg, err := loadAgentConfig([]string{"-allowed-signers", " Google LLC ; Mozilla Corporation;;Google LLC "}, look, io.Discard)
	if err != nil || !slices.Equal(cfg.AllowedSigners, []string{"Google LLC", "Mozilla Corporation"}) {
		t.Fatalf("flag: %q %v", cfg.AllowedSigners, err)
	}
	if cfg, err := loadAgentConfig(nil, look, io.Discard); err != nil || !slices.Equal(cfg.AllowedSigners, []string{"Mozilla Corporation"}) {
		t.Errorf("variável: %q %v", cfg.AllowedSigners, err)
	}
	if cfg, err := loadAgentConfig(nil, ambienteFalso(nil), io.Discard); err != nil || cfg.AllowedSigners == nil || len(cfg.AllowedSigners) != 0 {
		t.Errorf("sem lista: %#v %v", cfg.AllowedSigners, err)
	}
	var muitas []string
	for i := range 33 {
		muitas = append(muitas, "Org "+strings.Repeat("x", i+1))
	}
	for _, ruim := range []string{"Google\tLLC", strings.Repeat("a", 129), strings.Join(muitas, ";")} {
		if _, err := loadAgentConfig([]string{"-allowed-signers", ruim}, ambienteFalso(nil), io.Discard); err == nil || !strings.Contains(err.Error(), "-allowed-signers") {
			t.Errorf("%q: %v", ruim, err)
		}
	}
}
