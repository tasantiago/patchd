package api_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/settings"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/texto"
)

// As siglas da instituição nos nomes de lugar vêm do parâmetro do painel (nada de nome de
// instituição no código): sem ele, "XYZ" é palavra comum; com ele, fica em maiúsculas.
func TestSiglasNosNomesDeLugar(t *testing.T) {
	defer texto.SetPlaceAcronyms("")
	ctx := context.Background()
	params := settings.New([]settings.Def{{Key: api.PlaceAcronymsKey, Group: "Aparência", Label: "Siglas",
		Check: func(v string) (string, error) {
			l, err := texto.ParsePlaceAcronyms(v)
			return strings.Join(l, ", "), err
		}}}, func(string) (string, bool) { return "", false }, store.NewMemory(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	h, st, _ := ambientePaginas(t, api.WithSettings(params))
	if _, err := st.SaveInventory(ctx, "s1-0000", protocol.InventoryReport{SchemaVersion: 1, Hash: "s1", CollectedAt: time.Now().UTC(),
		OS: protocol.OSInfo{Family: "windows", Hostname: "EQ5001"}}); err != nil {
		t.Fatal(err)
	}
	if r := envioDePatrimonio(h, sessaoAdmin, "p.csv", []byte("DEPREDIO,DESALA,TOMBO\nNUCLEO XYZ,BLOCO A,5001\n")); !strings.Contains(local(r), "importado=1") {
		t.Fatalf("importar: %s", local(r))
	}
	inv := func() string { return pagina(h, "GET", "/painel/inventario", sessaoTeste, nil).Body.String() }
	if b := inv(); !strings.Contains(b, ">Nucleo Xyz<") || !strings.Contains(b, ">Bloco A<") {
		t.Fatalf("sem o parâmetro: %s", b[strings.Index(b, "Nucleo"):][:60])
	}
	if _, err := params.Apply(ctx, map[string]string{api.PlaceAcronymsKey: "xyz"}, "admin"); err != nil {
		t.Fatal(err)
	}
	if b := inv(); !strings.Contains(b, ">Nucleo XYZ<") {
		t.Fatal("com o parâmetro, a sigla não ficou em maiúsculas")
	}
	if _, err := params.Apply(ctx, map[string]string{api.PlaceAcronymsKey: "<b>"}, "admin"); err == nil {
		t.Fatal("sigla inválida aceita")
	}
}
