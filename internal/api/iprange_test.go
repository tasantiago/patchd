package api

import (
	"net/netip"
	"testing"
)

func TestParseIPRange(t *testing.T) {
	for in, want := range map[string][2]string{
		"10.1.2.0/24":          {"10.1.2.0", "10.1.2.255"},
		"10.1.2.77/24":         {"10.1.2.0", "10.1.2.255"},
		"172.16.0.0/12":        {"172.16.0.0", "172.31.255.255"},
		"10.1.2.10-10.1.2.50":  {"10.1.2.10", "10.1.2.50"},
		"10.1.2.10 - 10.1.3.5": {"10.1.2.10", "10.1.3.5"},
		"10.1.2.10-50":         {"10.1.2.10", "10.1.2.50"},
		"10.1.2.*":             {"10.1.2.0", "10.1.2.255"},
		"10.1.*":               {"10.1.0.0", "10.1.255.255"},
		"0.0.0.0/0":            {"0.0.0.0", "255.255.255.255"},
	} {
		r, ok := parseIPRange(in)
		if !ok || r.lo.String() != want[0] || r.hi.String() != want[1] {
			t.Errorf("%q → %v-%v %v, quero %v", in, r.lo, r.hi, ok, want)
		}
	}
	for _, ruim := range []string{"", "10.1.2.10", "pc-0042", "10.1.2.50-10", "10.1.2.50-10.1.2.10", "10.1.2.10-300", "2001:db8::/64",
		"10.1.2.0/33", "*", "10.*.2.*", "a.b.c.*", "10.1.2.3.*", "fulano-de-tal"} {
		if _, ok := parseIPRange(ruim); ok {
			t.Errorf("%q aceito como faixa", ruim)
		}
	}
	r, _ := parseIPRange("10.1.2.10-50")
	for ip, dentro := range map[string]bool{"10.1.2.10": true, "10.1.2.50": true, "10.1.2.30": true, "10.1.2.9": false, "10.1.2.51": false, "::ffff:10.1.2.20": true} {
		if r.contains(netip.MustParseAddr(ip)) != dentro {
			t.Errorf("%s dentro = %v", ip, !dentro)
		}
	}
}
