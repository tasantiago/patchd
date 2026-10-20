package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/texto"
)

// Aula 20.3: o inventário que um agente manda chega ao PostgreSQL. Para qualquer texto nos
// campos, depois da limpeza que a API faz na entrada (texto.CleanAll, no decodeJSON),
// gravar não pode falhar: uma falha aqui é um 500 a cada check-in, e a máquina fica sem
// inventário. Sem a limpeza, este alvo achou em menos de um segundo o byte 0xff e o NUL.
// Precisa do PATCHD_TEST_DATABASE_URL (sem ele, o alvo é pulado).
func FuzzSaveInventory(f *testing.F) {
	pool := bancoDeTeste(f)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		f.Fatal(err)
	}
	st := NewPostgres(pool)
	f.Add("pc-lab-01", "7-Zip 26.01 (x64)", "26.01", "Igor Pavlov", "Dell Inc.", int64(1791163517))
	f.Add("PC-ÇÃO", "Microsoft Edge", "141.0.3537.71", "Microsoft Corporation", "", int64(0))
	n := 0
	f.Fuzz(func(t *testing.T, host, nome, versao, editor, fabricante string, quando int64) {
		n++
		rep := inv(fmt.Sprintf("%032x%032x", time.Now().UnixNano(), n), versao) // hash novo: grava sempre
		rep.OS.Hostname = host
		rep.Software[0].Name, rep.Software[0].Publisher = nome, editor
		rep.Hardware = &protocol.Hardware{Manufacturer: fabricante}
		rep.CollectedAt = time.Unix(quando%253402300800, 0).UTC() // anos 1 a 9999, como o JSON permite
		texto.CleanAll(&rep)
		if _, err := st.SaveInventory(context.Background(), "11111111-1111-1111-1111-111111111111", rep); err != nil {
			t.Fatalf("o banco recusou: %v", err)
		}
	})
}
