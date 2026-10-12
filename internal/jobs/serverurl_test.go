package jobs

import (
	"crypto/ed25519"
	"encoding/json"
	"testing"
	"time"
)

func TestServerURLParams(t *testing.T) {
	for raw, quer := range map[string]string{
		"https://patchd.exemplo:8443":  "https://patchd.exemplo:8443",
		"HTTPS://Patchd.Exemplo:8443/": "https://patchd.exemplo:8443",
		" https://127.0.0.1:18443 ":    "https://127.0.0.1:18443",
	} {
		if got, err := NormalizeServerURL(raw); err != nil || got != quer {
			t.Errorf("%q: %q %v (quer %q)", raw, got, err, quer)
		}
	}
	for _, ruim := range []string{
		"http://patchd.exemplo:8080", "https://", "https://:8443", "https://u:s@patchd.exemplo",
		"https://patchd.exemplo/api", "https://patchd.exemplo/?x=1", "https://patchd.exemplo#a", "patchd.exemplo:8443", "",
	} {
		if _, err := NormalizeServerURL(ruim); err == nil {
			t.Errorf("%q aceita", ruim)
		}
	}
	raw, err := ServerURLParamsJSON("HTTPS://Patchd.Exemplo:8443/")
	if err != nil || string(raw) != `{"url":"https://patchd.exemplo:8443"}` {
		t.Fatalf("params: %s %v", raw, err)
	}
	// Fora da forma normal, campo a mais ou http: recusado na conferência do agente.
	for _, ruim := range []string{`{"url":"https://Patchd.exemplo:8443"}`, `{"url":"https://p:1","x":1}`, `{"url":"http://p:1"}`, `{}`} {
		if _, err := ParseServerURLParams(json.RawMessage(ruim)); err == nil {
			t.Errorf("%s aceito", ruim)
		}
	}
}

func TestServerURLAssinadoEConferido(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	agora := time.Now().UTC()
	raw, _ := ServerURLParamsJSON("https://patchd.exemplo:8443")
	env, err := Sign(priv, Job{ID: 7, MachineID: "m", Type: TypeServerURL, Params: raw, IssuedAt: agora, NotAfter: agora.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	j, err := Verify(pub, env, "m", agora, 6)
	if err != nil || j.Type != TypeServerURL {
		t.Fatalf("verify: %+v %v", j, err)
	}
	if p, err := ParseServerURLParams(j.Params); err != nil || p.URL != "https://patchd.exemplo:8443" {
		t.Errorf("params: %+v %v", p, err)
	}
	// Sem parâmetros válidos, nem assina.
	if _, err := Sign(priv, Job{ID: 8, MachineID: "m", Type: TypeServerURL, Params: json.RawMessage(`{"url":"http://x"}`), IssuedAt: agora, NotAfter: agora.Add(time.Hour)}); err == nil {
		t.Error("assinou server_url com http")
	}
}
