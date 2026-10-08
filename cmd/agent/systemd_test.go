package main

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestSystemdQuote(t *testing.T) {
	casos := map[string]string{
		"-checkin-interval":     "-checkin-interval",
		"/var/lib/patchd":       "/var/lib/patchd",
		"/srv/dados com espaço": `"/srv/dados com espaço"`,
		`a"b`:                   `"a\"b"`,
		"100%":                  "100%%",
		"$HOME":                 "$$HOME",
		"":                      `""`,
	}
	for in, quer := range casos {
		if got := systemdQuote(in); got != quer {
			t.Errorf("systemdQuote(%q) = %s, esperado %s", in, got, quer)
		}
	}
}

func TestSystemdUnit(t *testing.T) {
	u := systemdUnit("/usr/local/sbin/patchd-agent", []string{"-checkin-interval", "2m", "-data-dir", "/srv/dados com espaço"})
	for _, quer := range []string{
		`ExecStart=/usr/local/sbin/patchd-agent service run -checkin-interval 2m -data-dir "/srv/dados com espaço"`,
		"Environment=PATCHD_SERVICE_MANAGER=systemd",
		"Restart=on-failure",
		"WantedBy=multi-user.target",
		// Com ProtectSystem=full, o /etc é só leitura: sem isto, o agente não grava a
		// configuração do cache de repositórios.
		"ReadWritePaths=-/etc/apt/apt.conf.d -/etc/dnf",
	} {
		if !strings.Contains(u, quer) {
			t.Errorf("a unidade deveria conter %q:\n%s", quer, u)
		}
	}
	// StartLimitIntervalSec é opção da seção [Unit]; na [Service], o systemd a ignora com aviso.
	unit, service, _ := strings.Cut(u, "[Service]")
	if !strings.Contains(unit, "StartLimitIntervalSec=0") || strings.Contains(service, "StartLimit") {
		t.Error("StartLimitIntervalSec precisa estar em [Unit]")
	}
}

func TestLaunchdPlistEXMLValidoComEscape(t *testing.T) {
	p := launchdPlist("/usr/local/sbin/patchd-agent", []string{"-data-dir", "/Library/Application Support/patchd", "-x", "a<b&c"}, "/Library/Application Support/patchd")

	// O plist é XML bem formado e os argumentos chegam intactos depois do escape.
	var doc struct {
		Dict struct {
			Strings []string `xml:"array>string"`
		} `xml:"dict"`
	}
	if err := xml.Unmarshal([]byte(p), &doc); err != nil {
		t.Fatalf("plist inválido: %v\n%s", err, p)
	}
	quer := []string{"/usr/local/sbin/patchd-agent", "service", "run", "-data-dir", "/Library/Application Support/patchd", "-x", "a<b&c"}
	if strings.Join(doc.Dict.Strings, "|") != strings.Join(quer, "|") {
		t.Errorf("ProgramArguments = %q", doc.Dict.Strings)
	}
	for _, trecho := range []string{"a&lt;b&amp;c", "<key>PATCHD_SERVICE_MANAGER</key>", "<key>SuccessfulExit</key>\n\t\t<false/>",
		"/Library/Application Support/patchd/logs/stderr.log"} {
		if !strings.Contains(p, trecho) {
			t.Errorf("o plist deveria conter %q", trecho)
		}
	}
}
