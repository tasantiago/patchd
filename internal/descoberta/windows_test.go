package descoberta

import (
	"context"
	"strings"
	"testing"
)

func TestWindowsInstallRejectsInvalidPayload(t *testing.T) {
	for _, in := range []WindowsInstall{
		{IP: "192.0.2.1", Server: "http://server", Binary: []byte("binary")},
		{IP: "localhost", Server: "https://server"},
		{IP: "192.0.2.1", Server: "https://server", Username: "user", Password: "secret", Token: "token", Binary: []byte("tampered"), SHA256: strings.Repeat("0", 64)},
	} {
		if err := InstallWindows(context.Background(), in); err == nil {
			t.Fatal("accepted invalid installation payload")
		}
	}
}

func TestSpecContains(t *testing.T) {
	s, err := ParseSpec("192.0.2.10-20")
	if err != nil {
		t.Fatal(err)
	}
	for ip, want := range map[string]bool{"192.0.2.10": true, "192.0.2.20": true, "192.0.2.9": false, "192.0.2.21": false, "::1": false, "invalid": false} {
		if got := s.Contains(ip); got != want {
			t.Errorf("%s: %t", ip, got)
		}
	}
}
