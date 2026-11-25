package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/shield"
)

func TestShieldRankingsPorLugar(t *testing.T) {
	st, pool := shieldPG(t)
	ctx := context.Background()
	agora := time.Date(2026, 11, 24, 15, 0, 0, 0, time.UTC)
	maquinas := []struct {
		id, host, ip string
		reboot       bool
	}{
		{"m1", "EQ1001", "10.1.1.10", false}, {"m2", "EQ1002", "10.1.1.11", false},
		{"m3", "EQ1003", "10.1.2.10", true}, {"m4", "EQ1004", "10.1.2.11", false},
		{"m5", "EQ2001", "10.2.0.5", false}, {"m6", "EQ2002", "10.2.0.6", false},
		{"m7", "SEMTOMBO", "10.9.9.9", false},
	}
	inv := agora.Add(-time.Hour)
	cs := &complianceShield{}
	for _, m := range maquinas {
		users := `{}`
		if m.id == "m3" {
			users = `{"users":[{"account":"EXEMPLO\\ana.souza","session":"console","active":true}]}`
		}
		var invID, scanID int64
		if _, err := pool.Exec(ctx, `INSERT INTO machines (id, ring, last_seen_at, last_checkin_addr, state) VALUES ($1, 1, $2, $3::inet, $4::jsonb)`, m.id, agora, m.ip, users); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `INSERT INTO inventory_reports (machine_id, hash, schema_version, collected_at, hostname, report) VALUES ($1, 'h', 1, $2, $3, '{}') RETURNING id`,
			m.id, inv, m.host).Scan(&invID); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `INSERT INTO scan_reports (machine_id, schema_version, scanned_at, reboot_pending, report) VALUES ($1, 1, $2, $3, '{}') RETURNING id`,
			m.id, agora, m.reboot).Scan(&scanID); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE machines SET current_inventory_id = $2, current_scan_id = $3 WHERE id = $1`, m.id, invID, scanID); err != nil {
			t.Fatal(err)
		}
		cs.status = append(cs.status, protocol.ComplianceStatus{MachineID: m.id, State: "em_dia", InventoryCollectedAt: &inv, Catalog: "x"})
	}
	if _, err := pool.Exec(ctx, `INSERT INTO asset_locations (tombo, building, room) VALUES
		('1001','Edifício Sede','101'), ('1002','Edifício Sede','101'), ('1003','Edifício Sede','201'), ('1004','Edifício Sede','201'),
		('2001','Anexo','1'), ('2002','Anexo','1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ip_rings (spec, ring, updated_by) VALUES ('10.1.1.0/24', 1, 't'), ('10.1.2.0/24', 1, 't'), ('10.0.0.0/8', 1, 't')`); err != nil {
		t.Fatal(err)
	}
	pol := shield.Default()
	pol.Enabled = true
	pol.Ranking.MinGroup, pol.Ranking.MinRoom = 2, 2
	pol.Ranking.FloorNames = map[string]string{"10.1.2.0/24": "2º andar"}
	if _, err := st.SaveShieldPolicy(ctx, pol, "", "admin"); err != nil {
		t.Fatal(err)
	}
	a := &api{store: st, compliance: cs, logger: slog.New(slog.DiscardHandler), now: func() time.Time { return agora }}
	if err := a.shieldRun(ctx, st, agora); err != nil {
		t.Fatal(err)
	}
	sc, _, _ := st.ShieldScoreOf(ctx, "m3")
	if sc.Place != (shield.Place{Building: "Edifício Sede", Room: "201", Floor: "10.1.2.0/24"}) {
		t.Fatalf("lugar de m3 (a faixa mais específica): %+v", sc.Place)
	}
	b, _ := st.ShieldRankings(ctx, shield.RankBuilding, "", 10)
	if len(b) != 2 || b[0].Label != "Anexo" || b[1].Label != "Edifício Sede" || b[1].Sealed != 3 || b[1].Machines != 4 {
		t.Fatalf("prédios (o Anexo, 100%%, à frente): %+v", b)
	}
	f, _ := st.ShieldRankings(ctx, shield.RankFloor, "", 10)
	if len(f) != 2 || f[0].Label != "Faixa 1" || f[0].Building != "Edifício Sede" || f[1].Label != "2º andar" {
		t.Fatalf("andares só na Sede (duas faixas), com o nome da política: %+v", f)
	}
	r, _ := st.ShieldRankings(ctx, shield.RankRoom, "Edifício Sede", 10)
	if len(r) != 2 || r[0].Label != "101" || r[1].Label != "201" || r[1].Pos != 2 {
		t.Fatalf("salas da Sede: %+v", r)
	}
	// A Central da pessoa em m3: o prédio, o andar e a sala dela, sem IP nenhum.
	v, err := a.shieldView(ctx, st, "m3", protocol.SelfServiceUser{Account: `EXEMPLO\ana.souza`, Groups: []string{}}, agora)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Places) != 3 || v.Places[0].Mine != "Edifício Sede" || v.Places[0].Pos != 2 || v.Places[1].Mine != "2º andar" || v.Places[1].Pos != 2 ||
		v.Places[2].Mine != "201" || v.Places[2].Of != 2 {
		t.Fatalf("lugares na Central: %+v", v.Places)
	}
	j, _ := json.Marshal(v)
	if strings.Contains(string(j), "10.1.") || strings.Contains(string(j), "/24") {
		t.Fatalf("a Central recebeu a faixa de IP: %s", j)
	}
	if err := v.Check(); err != nil {
		t.Fatal(err)
	}
	// Desligar as salas some com elas na próxima passada.
	pol.Ranking.Rooms = false
	if _, err := st.SaveShieldPolicy(ctx, pol, "", "admin"); err != nil {
		t.Fatal(err)
	}
	a.shieldPolicyStale()
	if err := a.shieldRun(ctx, st, agora.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if r, _ := st.ShieldRankings(ctx, shield.RankRoom, "", 10); len(r) != 0 {
		t.Fatalf("salas desligadas continuam: %+v", r)
	}
}
