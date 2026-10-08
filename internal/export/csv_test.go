package export

import (
	"bytes"
	"encoding/csv"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

func le(t *testing.T, b []byte) [][]string {
	t.Helper()
	if !bytes.HasPrefix(b, []byte(BOM)) {
		t.Fatal("sem BOM")
	}
	r := csv.NewReader(bytes.NewReader(b[len(BOM):]))
	r.Comma = ';'
	rows, err := r.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestFrotaEmCSV(t *testing.T) {
	visto := time.Date(2026, 10, 8, 22, 51, 3, 0, time.UTC)
	col := visto.Add(-time.Hour)
	var b bytes.Buffer
	err := Fleet(&b, protocol.ComplianceFleet{EvaluatedAt: visto, Machines: []protocol.ComplianceStatus{
		{MachineID: "60ef97e4-1", Hostname: "vm; com \"aspas\"", OS: "Ubuntu 26.04", State: "faltando", Pending: 1, PendingUnit: "pacotes",
			Exploited: 1, LastSeenAt: visto, InventoryCollectedAt: &col, Catalog: "Ubuntu:26.04:LTS",
			Reasons: []string{"1 pacote com correção pendente", "kernel mais novo instalado"}, EvaluatedAt: visto},
		{MachineID: "evil-2", Hostname: "=HYPERLINK(\"http://x\",\"clique\")", OS: "+cmd", State: "desconhecido", LastSeenAt: visto, EvaluatedAt: visto,
			Reasons: []string{"-1"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "\r\n") {
		t.Error("CRLF")
	}
	rows := le(t, b.Bytes())
	if len(rows) != 3 || rows[0][0] != "maquina" || len(rows[0]) != 12 {
		t.Fatalf("%v", rows)
	}
	u := rows[1]
	if u[1] != `vm; com "aspas"` || u[3] != "faltando" || u[4] != "1" || u[6] != "1" || u[7] != "2026-10-08 22:51:03" ||
		u[8] != "2026-10-08 21:51:03" || u[10] != "1 pacote com correção pendente; kernel mais novo instalado" {
		t.Errorf("linha do Ubuntu: %q", u)
	}
	e := rows[2]
	if e[1] != `'=HYPERLINK("http://x","clique")` || e[2] != "'+cmd" || e[3] != "desconhecido" || e[8] != "" || e[10] != "'-1" {
		t.Errorf("injeção de fórmula: %q", e)
	}
}

func TestPendenciasEmCSV(t *testing.T) {
	var b bytes.Buffer
	d := protocol.ComplianceDetail{ComplianceStatus: protocol.ComplianceStatus{MachineID: "60ef97e4-1", Hostname: "vm"},
		Items: []protocol.PendingItem{
			{ID: "curl", Severity: "high", Exploited: true, Installed: "8.18.0-1ubuntu2.7", FixedIn: "8.18.0-1ubuntu2.10", Note: "USN-9002-1"},
			{ID: "CVE-2026-1", Note: "@SUM(A1)"},
		}}
	if err := Items(&b, d); err != nil {
		t.Fatal(err)
	}
	rows := le(t, b.Bytes())
	if len(rows) != 3 || rows[1][2] != "curl" || rows[1][3] != "sim" || rows[2][3] != "não" || rows[2][7] != "'@SUM(A1)" {
		t.Errorf("%q", rows)
	}
}
