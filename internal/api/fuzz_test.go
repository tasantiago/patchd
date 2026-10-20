package api

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/tasantiago/patchd/internal/protocol"
)

// Aula 20.3: fuzzing do que chega de fora no servidor.

// FuzzDecodeJSON: o corpo que o agente manda (T3 manda o que quiser), puro ou em gzip.
// Nunca um 5xx nem pânico; aceito, o valor não leva texto que o banco recusa (NUL).
func FuzzDecodeJSON(f *testing.F) {
	f.Add([]byte(`{"schema_version":1,"hash":"sha256:00","os":{"family":"windows","hostname":"pc-01"},"software":[{"name":"7-Zip","version":"26.01","scope":"machine"}]}`), false)
	f.Add([]byte(`{"os":{"hostname":"pc\u0000"}}`), false)
	f.Add([]byte(`{"software":[{"name":"a\ud800b"}]} {}`), false)
	f.Add([]byte(`{"hardware":{"model":"x"},"errors":["a"]}`), true)
	f.Fuzz(func(t *testing.T, body []byte, comprimir bool) {
		enc := ""
		if comprimir {
			var b bytes.Buffer
			zw := gzip.NewWriter(&b)
			_, _ = zw.Write(body)
			_ = zw.Close()
			body, enc = b.Bytes(), "gzip"
		}
		r := httptest.NewRequest(http.MethodPost, "/api/v1/agent/machines/x/inventory", bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Content-Encoding", enc)
		w := httptest.NewRecorder()
		var rep protocol.InventoryReport
		if !decodeJSON(w, r, &rep) {
			if w.Code < 400 || w.Code >= 500 {
				t.Fatalf("recusa com status %d", w.Code)
			}
			return
		}
		if texto := textoDe(rep); strings.ContainsRune(texto, 0) || !utf8.ValidString(texto) {
			t.Fatalf("texto que o banco não guarda passou: %q", texto)
		}
	})
}

// FuzzConsultas: os filtros das listas (busca do Patch, de Equipamentos, do software da
// máquina e o usuário da Auditoria) vêm da URL. Saem com no máximo o limite de bytes, em
// UTF-8 válido e sem NUL: vão para a tela, para links e (a Auditoria) para o banco.
func FuzzConsultas(f *testing.F) {
	f.Add("q=7-zip&usuario=fulano")
	f.Add("q=" + strings.Repeat("ç", 60) + "&usuario=" + strings.Repeat("é", 40))
	f.Add("q=%FF%FE&usuario=%00")
	f.Fuzz(func(t *testing.T, raw string) {
		v, err := url.ParseQuery(raw)
		if err != nil {
			return
		}
		confere := func(onde, s string, limite int) {
			if len(s) > limite || !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
				t.Fatalf("%s: %q (%d bytes)", onde, s, len(s))
			}
		}
		confere("patch", parsePatchQuery(v).Q, maxSearchLen)
		confere("equipamentos", parseEquipQuery(v).Q, maxSearchLen)
		confere("software", softwareTab("m", protocol.InventoryReport{}, v, func(time.Time) string { return "" }).Q, maxSearchLen)
		confere("auditoria", parseAuditQuery(v).User, 64)
	})
}

// textoDe junta todo o texto do inventário que vai para o banco.
func textoDe(rep protocol.InventoryReport) string {
	var b strings.Builder
	add := func(ss ...string) {
		for _, s := range ss {
			b.WriteString(s)
		}
	}
	add(rep.AgentVersion, rep.Hash, rep.OS.Family, rep.OS.Name, rep.OS.Version, rep.OS.Hostname)
	add(rep.Errors...)
	for _, s := range rep.Software {
		add(s.Name, s.Version, s.Publisher, s.InstallDate, s.Scope, s.User, s.Arch, s.ProductCode)
	}
	if h := rep.Hardware; h != nil {
		add(h.Manufacturer, h.Model, h.SerialNumber, h.ChassisType)
	}
	return b.String()
}
