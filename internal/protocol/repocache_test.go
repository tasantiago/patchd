package protocol

import "testing"

func TestRepoCacheValidate(t *testing.T) {
	ok := []RepoCache{
		{URL: "http://patchd.exemplo:8080", AptHosts: []string{"archive.ubuntu.com"}},
		{URL: "https://patchd.exemplo/", AptHosts: []string{"archive.ubuntu.com", "security.ubuntu.com"}},
		{URL: "http://127.0.0.1:18080", AptHosts: []string{"archive.ubuntu.com"}},
	}
	for _, rc := range ok {
		if err := rc.Validate(); err != nil {
			t.Errorf("%+v recusado: %v", rc, err)
		}
	}
	ruins := []RepoCache{
		{URL: `http://x";Acquire::http::Proxy "http://mal`, AptHosts: []string{"archive.ubuntu.com"}}, // injeção no apt.conf
		{URL: "http://x\nbaseurl=http://mal", AptHosts: []string{"archive.ubuntu.com"}},               // injeção no .repo
		{URL: "http://x/caminho", AptHosts: []string{"archive.ubuntu.com"}},
		{URL: "ftp://x", AptHosts: []string{"archive.ubuntu.com"}},
		{URL: "http://usuario:senha@x", AptHosts: []string{"archive.ubuntu.com"}},
		{URL: "http://x?a=1", AptHosts: []string{"archive.ubuntu.com"}},
		{URL: "http://x", AptHosts: []string{`archive.ubuntu.com" "x`}},
		{URL: "http://x", AptHosts: nil},
		{URL: "http://$x", AptHosts: []string{"archive.ubuntu.com"}},
	}
	for _, rc := range ruins {
		if err := rc.Validate(); err == nil {
			t.Errorf("%+v aceito", rc)
		}
	}
}
