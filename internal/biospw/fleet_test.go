package biospw

import (
	"errors"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/machinebox"
)

func TestSenhaPadrao(t *testing.T) {
	priv, _ := machinebox.Generate()
	pub, _ := machinebox.Public(priv)
	env, err := SealFleet(pub, "lenovo", "Frota#2024")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(env), "Frota#2024") {
		t.Fatal("senha legível no envelope")
	}
	if pw, err := OpenFleet(priv, "lenovo", env); err != nil || pw != "Frota#2024" {
		t.Fatalf("%q %v", pw, err)
	}
	if _, err := OpenFleet(priv, "dell", env); !errors.Is(err, ErrOtherVendor) {
		t.Errorf("outro fabricante: %v", err)
	}
	// O envelope da guarda não abre como senha padrão, nem o contrário.
	g, _ := Seal(pub, "60ef97e4-0001", "abcd2345efgh")
	if _, err := OpenFleet(priv, "lenovo", g); err == nil {
		t.Error("envelope da guarda aberto como senha padrão")
	}
	if _, err := Open(priv, "60ef97e4-0001", env); err == nil {
		t.Error("senha padrão aberta como guarda")
	}
	for v, ruins := range map[string][]string{
		"lenovo": {"", "a,b", "a;b", "senhaçã", "com espaço", strings.Repeat("a", 65), "a\tb"},
		"hp":     {"", "a\nb", strings.Repeat("a", 65)},
	} {
		for _, pw := range ruins {
			if CheckFleet(v, pw) == nil {
				t.Errorf("%s: %q aceita", v, pw)
			}
		}
	}
	for _, ok := range [][2]string{{"lenovo", "Frota#2024!"}, {"hp", "Frota 2024, ç"}, {"dell", "x"}} {
		if err := CheckFleet(ok[0], ok[1]); err != nil {
			t.Errorf("%v: %v", ok, err)
		}
	}
}
