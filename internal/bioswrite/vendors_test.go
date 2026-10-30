package bioswrite

import (
	"strings"
	"testing"
)

func TestRespostasDosFabricantes(t *testing.T) {
	for _, c := range []struct {
		r                    Reply
		ok, wrong, uncertain bool
	}{
		{hpReply(0), true, false, false},
		{hpReply(6), false, true, false},
		{hpReply(3), false, false, true},
		{hpReply(99), false, false, true},
		{dellReply(0), true, false, false},
		{dellReply(3), false, true, false},
		{dellReply(2), false, false, false},
		{dellReply(1), false, false, true},
		{dellReply(42), false, false, true},
		{lenovoReply(" Success "), true, false, false},
		{lenovoReply("Access Denied"), false, true, false},
		{lenovoReply("Not Supported"), false, false, false},
		{lenovoReply("System Busy"), false, false, false},
		{lenovoReply(""), false, false, true},
	} {
		if c.r.OK != c.ok || c.r.WrongPassword != c.wrong || c.r.Uncertain != c.uncertain {
			t.Errorf("%+v", c.r)
		}
	}
}

func TestLinhasDaLenovo(t *testing.T) {
	if l, _ := lenovoPasswordLine([]byte("abcd2345efgh"), []byte("Frota#2024")); l != "pap,Frota#2024,abcd2345efgh,ascii,us;" {
		t.Error(l)
	}
	if l, _ := lenovoPasswordLine([]byte("abcd2345efgh"), nil); l != "pap,,abcd2345efgh,ascii,us;" {
		t.Error(l)
	}
	if l, _ := lenovoSettingLine("WakeOnLAN", "ACOnly", nil); l != "WakeOnLAN,ACOnly;" {
		t.Error(l)
	}
	if l, _ := lenovoSettingLine("WakeOnLAN", "ACOnly", []byte("abcd2345efgh")); l != "WakeOnLAN,ACOnly,abcd2345efgh,ascii,us;" {
		t.Error(l)
	}
	if l, _ := lenovoSaveLine(nil); l != "" {
		t.Error(l)
	}
	if l, _ := lenovoSaveLine([]byte("abcd2345efgh")); l != "abcd2345efgh,ascii,us;" {
		t.Error(l)
	}
	// O separador na senha da frota (digitada pelo admin) ou no valor: recusa, sem chamada.
	for _, err := range []error{
		func() error { _, err := lenovoPasswordLine([]byte("abcd2345efgh"), []byte("a,b")); return err }(),
		func() error { _, err := lenovoSettingLine("WakeOnLAN", "AC;Only", nil); return err }(),
		func() error { _, err := lenovoSaveLine([]byte("x;y")); return err }(),
	} {
		if err == nil || strings.Contains(err.Error(), "a,b") {
			t.Errorf("separador: %v", err)
		}
	}
}

func TestSegurancaDaDell(t *testing.T) {
	if st, n, h := dellSecurity(nil); st != 0 || n != 0 || len(h) != 1 {
		t.Errorf("sem senha: %d %d %v", st, n, h)
	}
	pw := []byte("abcd2345efgh")
	st, n, h := dellSecurity(pw)
	if st != 1 || n != 12 || string(h) != "abcd2345efgh" {
		t.Errorf("com senha: %d %d", st, n)
	}
	clear(h)
	if string(pw) != "abcd2345efgh" {
		t.Error("o handle tem de ser uma cópia (é limpo depois da chamada)")
	}
}
