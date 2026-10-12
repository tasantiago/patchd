package config

import "testing"

func TestBool(t *testing.T) {
	look := func(m map[string]string) Lookup {
		return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
	}
	for v, quer := range map[string]bool{"true": true, "1": true, "SIM": true, " false ": false, "0": false, "não": false, "nao": false} {
		if got, err := Bool(look(map[string]string{"X": v}), "X", !quer); err != nil || got != quer {
			t.Errorf("%q: %v %v", v, got, err)
		}
	}
	if got, err := Bool(look(nil), "X", true); err != nil || !got {
		t.Errorf("ausente: %v %v", got, err)
	}
	if got, err := Bool(look(map[string]string{"X": ""}), "X", true); err != nil || !got {
		t.Errorf("vazio: %v %v", got, err)
	}
	if _, err := Bool(look(map[string]string{"X": "talvez"}), "X", false); err == nil {
		t.Error("talvez aceito")
	}
}
