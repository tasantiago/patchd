package api

import (
	"net/netip"
	"testing"

	"github.com/tasantiago/patchd/internal/store"
)

func TestIndependentFloorRegex(t *testing.T) {
	rules := compileFloorRegex(map[store.FloorKey]string{
		{Building:"X",Spec:`regex:^10\.12\.1\.[0-9]+$`}: "1º andar",
		{Building:"X",Spec:`regex:^10\.12\.2\.[0-9]+$`}: "2º andar",
		{Building:"Y",Spec:`regex:^10\.12\.1\.[0-9]+$`}: "Térreo",
	})
	e := equip{ip:netip.MustParseAddr("10.12.1.45")}
	if got, conflict := regexFloorName(matchingFloorRegex(e,rules["X"])); got!="1º andar" || conflict { t.Fatalf("got %q, conflict %v",got,conflict) }
	if got, _ := regexFloorName(matchingFloorRegex(e,rules["Y"])); got!="Térreo" { t.Fatal(got) }
	e.ip=netip.MustParseAddr("10.12.2.45")
	if got, _ := regexFloorName(matchingFloorRegex(e,rules["X"])); got!="2º andar" { t.Fatal(got) }
	if _, conflict := regexFloorName([]floorRegexRule{{Name:"A"},{Name:"B"}}); !conflict { t.Fatal("expected conflict") }
}
