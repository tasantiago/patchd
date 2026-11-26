package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/store"
)

func TestComandosDePredio(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	ids := prediosCLI(t, mem, map[string]string{"m1": "EQ1001", "m2": "EQ2001", "m3": "SEMTOMBO"},
		[]store.AssetLocation{{Tombo: "1001", Building: "PREDIO SEDE"}, {Tombo: "2001", Building: "FORUM GERAL"}})
	roda := func(args ...string) (int, string, string) {
		var o, e bytes.Buffer
		code := buildingCommand(ctx, mem, args, "America/Porto_Velho", &o, &e)
		return code, o.String(), e.String()
	}
	sede, forum := fmt.Sprint(ids["PREDIO SEDE"]), fmt.Sprint(ids["FORUM GERAL"])
	if code, _, e := roda("window", "-id", sede, "-days", "seg-sex", "-start", "22:00"); code != exitConfig || !strings.Contains(e, "-end") {
		t.Errorf("sem término: %d %s", code, e)
	}
	if code, _, e := roda("window", "-id", "x", "-days", "seg-sex", "-start", "22:00", "-end", "04:00"); code != exitConfig {
		t.Errorf("ID ruim: %d %s", code, e)
	}
	if code, _, e := roda("window", "-id", sede+","+forum, "-days", "seg-sex", "-start", "22:00", "-end", "04:00"); code != exitOK || !strings.Contains(e, "em 2 prédio(s)") {
		t.Fatalf("lote: %d %s", code, e)
	}
	ws, _ := mem.BuildingWindows(ctx)
	if ws[ids["PREDIO SEDE"]].Window.String() != "seg-sex 22:00-04:00 do dia seguinte (America/Porto_Velho)" {
		t.Errorf("janela: %+v", ws)
	}
	if code, _, _ := roda("window", "-id", forum, "-disable"); code != exitOK {
		t.Error("desativar")
	}
	code, o, _ := roda("list")
	if code != exitOK || !strings.Contains(o, sede+"   Predio Sede") || !strings.Contains(o, "(desativada)") || !strings.Contains(o, "1 equipamento(s) sem localização identificada") {
		t.Errorf("list:\n%s", o)
	}
	if code, _, e := roda("window", "-id", sede, "-clear"); code != exitOK || !strings.Contains(e, "1 janela(s) removida(s)") {
		t.Errorf("remover: %d %s", code, e)
	}
}
