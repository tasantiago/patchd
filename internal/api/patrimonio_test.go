package api_test

import (
	"bytes"
	"context"
	"encoding/csv"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

func envioDePatrimonio(h http.Handler, cookie, nome string, arquivo []byte) *httptest.ResponseRecorder {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if arquivo != nil {
		fw, _ := mw.CreateFormFile("arquivo", nome)
		_, _ = fw.Write(arquivo)
	}
	_ = mw.Close()
	req := httptest.NewRequest("POST", "/painel/configuracao/patrimonio", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(&http.Cookie{Name: "patchd_sessao", Value: cookie})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// Aula 23.6: o CSV do patrimônio em Configuração › Patrimônio, a ligação pelo tombo no nome e
// a localização no Inventário (coluna, filtro de prédio, busca e CSV). Dados fictícios.
func TestPatrimonioNoInventario(t *testing.T) {
	h, st, _ := ambientePaginas(t)
	ctx := context.Background()
	for id, host := range map[string]string{"p1-0000": "FJ102874", "p2-0000": "FJ088594", "p3-0000": "NB116583X", "p4-0000": "PC112345"} {
		if _, err := st.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: id, CollectedAt: time.Now().UTC(),
			OS: protocol.OSInfo{Family: "windows", Hostname: host}}); err != nil {
			t.Fatal(err)
		}
	}
	csvArq := "DEPREDIO,DESALA,UO,TOMBO,DEBEM,DESUBGRUPOBEM\n" +
		"FORUM EXEMPLO,SALA 101 - CARTORIO,UOX,102874,DESKTOP EXEMPLO,COMPUTADOR\n" +
		"FORUM EXEMPLO,SALA 102,UOX,88594,DESKTOP EXEMPLO,COMPUTADOR\n" +
		"ANEXO EXEMPLO,SALA 201,UOY,116583,NOTEBOOK EXEMPLO,COMPUTADOR\n" +
		"ANEXO EXEMPLO,SALA 202,UOY,1234,MONITOR EXEMPLO,MONITOR\n" +
		"ANEXO EXEMPLO,SALA 203,UOY,,SEM TOMBO,MONITOR\n"

	// Só admin; sem arquivo, com arquivo sem a coluna, e só com linhas sem tombo: nada muda.
	if r := envioDePatrimonio(h, sessaoTeste, "p.csv", []byte(csvArq)); r.Code != http.StatusForbidden {
		t.Errorf("leitura: %d", r.Code)
	}
	for nome, arq := range map[string][]byte{"sem arquivo": nil, "sem coluna": []byte("PREDIO,SALA\nA,B\n"), "sem tombo": []byte("TOMBO,DESALA\n,B\n")} {
		r := envioDePatrimonio(h, sessaoAdmin, "p.csv", arq)
		if l := local(r); !strings.HasPrefix(l, "/painel/configuracao?aba=patrimonio&erro=patrimonio&motivo=") {
			t.Errorf("%s: %s", nome, l)
		}
	}
	if l, _ := st.AssetLocations(ctx); len(l) != 0 {
		t.Fatalf("gravou com erro: %+v", l)
	}

	r := envioDePatrimonio(h, sessaoAdmin, `C:\Users\exemplo\patrimonio.csv`, []byte(csvArq))
	l := local(r)
	if !strings.Contains(l, "importado=4") || !strings.Contains(l, "ligadas=3") || !strings.Contains(l, "ignoradas=1") || !strings.Contains(l, "linhas=6") {
		t.Fatalf("importar: %s", l)
	}
	b := pagina(h, "GET", l, sessaoAdmin, nil).Body.String()
	for _, tr := range []string{"Patrimônio importado: 4 bens, 3 máquina(s) do inventário ligadas pelo tombo.", "(linhas 6)",
		"<td>patrimonio.csv</td>", "(3 com máquina no inventário)", "(3 com tombo ligado)", ">PC112345</a>"} {
		if !strings.Contains(b, tr) {
			t.Errorf("aba: faltou %q", tr)
		}
	}

	// O Inventário: a coluna, o prédio na lista e o filtro; a busca pela sala.
	b = pagina(h, "GET", "/painel/inventario?q=FJ0&campo=nome", sessaoTeste, nil).Body.String()
	for _, tr := range []string{"<code>88594</code>", `title="UOX · DESKTOP EXEMPLO · COMPUTADOR">FORUM EXEMPLO<div class="muted nota">SALA 102</div>`,
		`<option value="ANEXO EXEMPLO" >ANEXO EXEMPLO</option>`} {
		if !strings.Contains(b, tr) {
			t.Errorf("inventário: faltou %q", tr)
		}
	}
	b = pagina(h, "GET", "/painel/inventario?predio=ANEXO+EXEMPLO", sessaoTeste, nil).Body.String()
	if !strings.Contains(b, "1–1 de 1") || !strings.Contains(b, "NB116583X") || !strings.Contains(b, `<option value="ANEXO EXEMPLO" selected>`) {
		t.Errorf("filtro de prédio:\n%s", b)
	}
	if b := pagina(h, "GET", "/painel/inventario?campo=local&q=sala+101", sessaoTeste, nil).Body.String(); !strings.Contains(b, "1–1 de 1") || !strings.Contains(b, "FJ102874") {
		t.Error("busca pela sala")
	}

	// O CSV leva as colunas do patrimônio.
	res := pagina(h, "GET", "/painel/inventario.csv?campo=nome&q=FJ102874", sessaoTeste, nil)
	cr := csv.NewReader(strings.NewReader(strings.TrimPrefix(res.Body.String(), "\ufeff")))
	cr.Comma = ';'
	recs, err := cr.ReadAll()
	if err != nil || len(recs) != 2 {
		t.Fatalf("csv: %v %v", recs, err)
	}
	cab, lin := recs[0], recs[1]
	if strings.Join(cab[len(cab)-6:], ",") != "tombo,predio,sala,uo,bem,subgrupo_bem" ||
		strings.Join(lin[len(lin)-6:], ",") != "102874,FORUM EXEMPLO,SALA 101 - CARTORIO,UOX,DESKTOP EXEMPLO,COMPUTADOR" {
		t.Errorf("csv: %v / %v", cab, lin)
	}

	// Uma importação nova substitui a anterior inteira.
	envioDePatrimonio(h, sessaoAdmin, "novo.csv", []byte("TOMBO;DEPREDIO;DESALA\n102874;ANEXO EXEMPLO;SALA 299\n"))
	if l, _ := st.AssetLocations(ctx); len(l) != 1 || l[0].Room != "SALA 299" {
		t.Fatalf("substituição: %+v", l)
	}
	b = pagina(h, "GET", "/painel/inventario?q=FJ088594&campo=nome", sessaoTeste, nil).Body.String()
	if strings.Contains(b, "<code>88594</code>") {
		t.Error("o tombo que saiu do arquivo continua na tabela")
	}
	ev, _ := st.AuditEvents(ctx, store.AuditFilter{Action: "patrimonio."})
	if len(ev) != 2 || ev[0].Target != "novo.csv" || !strings.Contains(ev[1].Detail, "4 bens · 1 linhas sem tombo · 0 repetidos · 3 máquinas ligadas") {
		t.Errorf("auditoria: %+v", ev)
	}
}
