package descoberta

import (
	"context"
	"net/netip"
	"slices"
	"strings"
	"testing"
)

func TestParseSpec(t *testing.T) {
	casos := []struct {
		in    string
		text  string
		count uint64
	}{
		{"10.0.0.0/24", "10.0.0.0/24", 256},
		{"10.0.0.0/30", "10.0.0.0/30", 4},
		{"10.1.0.0/16", "10.1.0.0/16", 65536},
		{"172.16.5.3", "172.16.5.3", 1},
		{"192.168.1.10-20", "192.168.1.10-192.168.1.20", 11},
		{"192.168.1.10-192.168.1.12", "192.168.1.10-192.168.1.12", 3},
		{"  10.0.0.1/32 ", "10.0.0.1/32", 1},
	}
	for _, c := range casos {
		sp, err := ParseSpec(c.in)
		if err != nil {
			t.Errorf("%s: %v", c.in, err)
			continue
		}
		if sp.Text != c.text || sp.Count() != c.count {
			t.Errorf("%s: %q count=%d; queria %q/%d", c.in, sp.Text, sp.Count(), c.text, c.count)
		}
	}
}

func TestParseSpecRecusa(t *testing.T) {
	for _, in := range []string{"", "lixo", "10.0.0.0/8", "::1/64", "10.0.0.5-10.0.0.1", "10.0.0.10-5", "256.1.1.1", "10.0.0.0/33"} {
		if sp, err := ParseSpec(in); err == nil {
			t.Errorf("%q devia recusar; passou como %q", in, sp.Text)
		}
	}
}

func TestScan(t *testing.T) {
	// Falso: .10 é Windows (445, 3389), .22 é SSH (22), .99 não atende. O resto é vazio.
	dial := func(_ context.Context, ip netip.Addr, port int) bool {
		b := ip.As4()
		switch {
		case b[3] == 10:
			return port == 445 || port == 3389
		case b[3] == 22:
			return port == 22
		}
		return false
	}
	a, _ := ParseSpec("10.0.0.0/24")
	b, _ := ParseSpec("10.0.0.8-10.0.0.24") // sobrepõe .10 e .22: não conta duas vezes
	found, scanned, partial := Scan(context.Background(), []Spec{a, b}, Options{Dial: dial, Concurrency: 32})
	if partial || scanned != 256 {
		t.Fatalf("sondados=%d parcial=%v", scanned, partial)
	}
	if len(found) != 2 {
		t.Fatalf("achou %d: %+v", len(found), found)
	}
	if found[0].IP.String() != "10.0.0.10" || found[0].OS != "windows" || !slices.Equal(found[0].Ports, []int{445, 3389}) {
		t.Errorf("primeiro: %+v", found[0])
	}
	if found[1].IP.String() != "10.0.0.22" || found[1].OS != "ssh" {
		t.Errorf("segundo: %+v", found[1])
	}
}

func TestScanCanceledCountsCompletedAddresses(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sp, _ := ParseSpec("192.0.2.0/24")
	found, scanned, _ := Scan(ctx, []Spec{sp}, Options{Dial: func(context.Context, netip.Addr, int) bool { t.Error("canceled scan dialed"); return false }})
	if len(found) != 0 || scanned != 0 {
		t.Fatalf("canceled scan: %d findings, %d completed", len(found), scanned)
	}
}

func TestScanTeto(t *testing.T) {
	sp, _ := ParseSpec("10.0.0.0/16")
	dialed := 0
	dial := func(_ context.Context, _ netip.Addr, _ int) bool { dialed++; return false }
	_, scanned, partial := Scan(context.Background(), []Spec{sp}, Options{Dial: dial, Concurrency: 64, Ports: []int{445}})
	if !partial || scanned != MaxScanHosts {
		t.Fatalf("sondados=%d parcial=%v (queria %d/true)", scanned, partial, MaxScanHosts)
	}
}

func TestInstallHints(t *testing.T) {
	win := InstallHints("10.0.0.10", "https://patchd.exemplo", "windows", false)
	if len(win) != 3 {
		t.Fatalf("windows: %d dicas", len(win))
	}
	metodos := []string{win[0].Method, win[1].Method, win[2].Method}
	if !slices.Equal(metodos, []string{"powershell", "psexec", "gpo"}) {
		t.Errorf("métodos: %v", metodos)
	}
	for _, h := range win {
		if !strings.Contains(h.Command, "https://patchd.exemplo") {
			t.Errorf("%s sem servidor: %s", h.Method, h.Command)
		}
		// A GPO mira uma OU inteira, não um IP; os outros levam o IP do host.
		if h.Method != "gpo" && !strings.Contains(h.Command, "10.0.0.10") {
			t.Errorf("%s sem IP: %s", h.Method, h.Command)
		}
		if strings.Contains(h.Command, "patchd-agent-linux") {
			t.Errorf("%s com binário Linux", h.Method)
		}
	}
	ssh := InstallHints("10.0.0.22", "https://patchd.exemplo", "ssh", false)
	if len(ssh) != 1 || ssh[0].Method != "ssh" || !strings.Contains(ssh[0].Command, "patchd-agent-linux-amd64") {
		t.Fatalf("ssh: %+v", ssh)
	}
	// Sem palpite: os dois lados. Com CA: o -ca-file aparece.
	amb := InstallHints("10.0.0.5", "https://s", "", true)
	if len(amb) != 4 {
		t.Fatalf("ambíguo: %d", len(amb))
	}
	if !strings.Contains(amb[0].Command, "-ca-file") || !strings.Contains(amb[3].Command, "/etc/patchd/ca.crt") {
		t.Error("CA não entrou nos comandos")
	}
}
