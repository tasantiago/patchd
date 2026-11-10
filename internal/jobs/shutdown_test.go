package jobs

import "testing"

func TestShutdownParams(t *testing.T) {
	for raw, ok := range map[string]bool{"": true, "null": true, "{}": true, `{"agora":true}`: false, "[]": false} {
		if _, err := ParseShutdownParams([]byte(raw)); (err == nil) != ok {
			t.Errorf("%q: %v", raw, err)
		}
	}
	if !KnownType(TypeShutdown) {
		t.Error("tipo desconhecido")
	}
}
