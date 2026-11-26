package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/shield"
	"github.com/tasantiago/patchd/internal/store"
)

// shieldPG: um schema novo no banco de teste, com as migrações (pulado sem banco).
func shieldPG(t *testing.T) (*store.Postgres, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("PATCHD_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("PATCHD_TEST_DATABASE_URL não definido: teste com PostgreSQL pulado")
	}
	ctx := context.Background()
	admin, err := store.Connect(ctx, dsn, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	schema := "teste_" + hex.EncodeToString(b)
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := store.Migrate(ctx, pool, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	return store.NewPostgres(pool), pool
}

// complianceShield: a compliance do teste (todas em dia, com o inventário de agora).
type complianceShield struct {
	status []protocol.ComplianceStatus
	items  []protocol.MachineItems
}

func (c *complianceShield) Fleet(context.Context) (protocol.ComplianceFleet, error) {
	return protocol.ComplianceFleet{Machines: c.status}, nil
}
func (c *complianceShield) Detail(context.Context, string) (protocol.ComplianceDetail, bool, error) {
	return protocol.ComplianceDetail{}, false, nil
}
func (c *complianceShield) Items(context.Context) ([]protocol.MachineItems, error) {
	return c.items, nil
}

func TestShieldAvaliadorDePontaAPonta(t *testing.T) {
	st, pool := shieldPG(t)
	ctx := context.Background()
	// Terça, 24/11/2026, 15h UTC.
	agora := time.Date(2026, 11, 24, 15, 0, 0, 0, time.UTC)
	users := `{"users":[{"account":"EXEMPLO\\ana.souza","display_name":"Ana Souza","session":"console","active":true},
		{"account":"EXEMPLO\\bruno","session":"rdp","active":false}]}`
	for _, m := range []struct {
		id    string
		ring  int
		state string
	}{{"m-a", 1, users}, {"m-b", 2, `{}`}} {
		if _, err := pool.Exec(ctx, `INSERT INTO machines (id, ring, state, last_seen_at) VALUES ($1, $2, $3::jsonb, $4)`, m.id, m.ring, m.state, agora.Add(-time.Minute)); err != nil {
			t.Fatal(err)
		}
		var scan int64
		if err := pool.QueryRow(ctx, `INSERT INTO scan_reports (machine_id, schema_version, scanned_at, reboot_pending, report) VALUES ($1, 1, $2, false, '{}') RETURNING id`,
			m.id, agora).Scan(&scan); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE machines SET current_scan_id = $2 WHERE id = $1`, m.id, scan); err != nil {
			t.Fatal(err)
		}
	}
	inv := agora.Add(-time.Hour)
	cs := &complianceShield{status: []protocol.ComplianceStatus{
		{MachineID: "m-a", State: "em_dia", InventoryCollectedAt: &inv, Catalog: "x"},
		{MachineID: "m-b", State: "em_dia", InventoryCollectedAt: &inv, Catalog: "x"},
	}}
	pol := shield.Default()
	pol.Enabled, pol.PilotRings = true, []int{1}
	if _, err := st.SaveShieldPolicy(ctx, pol, "piloto", "admin"); err != nil {
		t.Fatal(err)
	}
	relogio := agora
	novo := func() *api {
		return &api{store: st, compliance: cs, logger: slog.New(slog.DiscardHandler), now: func() time.Time { return relogio }}
	}
	a := novo()
	if err := a.shieldRun(ctx, st, agora); err != nil {
		t.Fatal(err)
	}
	s, ok, err := st.ShieldScoreOf(ctx, "m-a")
	if err != nil || !ok || !s.Result.Sealed || s.Result.Display != 100 {
		t.Fatalf("m-a no selo: %+v %v", s, err)
	}
	if _, ok, _ := st.ShieldScoreOf(ctx, "m-b"); ok {
		t.Fatal("m-b fora do piloto ganhou pontuação")
	}
	pl, ok, _ := st.ShieldPlayerByAccount(ctx, `exemplo\ana.souza`)
	if !ok || pl.DisplayName != "Ana Souza" {
		t.Fatalf("a pessoa da sessão ativa: %+v", pl)
	}
	if _, ok, _ := st.ShieldPlayerByAccount(ctx, `exemplo\bruno`); ok {
		t.Fatal("sessão desconectada não conta como presença")
	}

	// Quarta: o dia 24 fecha (protegido): 10 do dia + 10 da conquista "primeiro escudo".
	relogio = agora.Add(20 * time.Hour)
	if err := a.shieldRun(ctx, st, relogio); err != nil {
		t.Fatal(err)
	}
	pls, _ := st.ShieldPlayersByID(ctx, []int64{pl.ID})
	got := pls[pl.ID]
	if got.Streak.Current != 1 || got.Streak.Protected != 1 || got.XP != 20 {
		t.Fatalf("depois do fechamento: %+v", got)
	}
	// Outro servidor (sem a memória deste) fecha de novo: nada muda.
	if err := novo().shieldRun(ctx, st, relogio); err != nil {
		t.Fatal(err)
	}
	pls, _ = st.ShieldPlayersByID(ctx, []int64{pl.ID})
	if pls[pl.ID].XP != 20 || pls[pl.ID].Streak.Current != 1 {
		t.Fatalf("fechou duas vezes: %+v", pls[pl.ID])
	}

	// Reinício pendente aparece e some: quem estava na máquina ganha o XP do reinício.
	if _, err := pool.Exec(ctx, `UPDATE scan_reports SET reboot_pending = true WHERE machine_id = 'm-a'`); err != nil {
		t.Fatal(err)
	}
	if err := a.shieldRun(ctx, st, relogio.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if s, _, _ := st.ShieldScoreOf(ctx, "m-a"); s.Result.Sealed {
		t.Fatal("reinício pendente com selo")
	}
	if _, err := pool.Exec(ctx, `UPDATE scan_reports SET reboot_pending = false WHERE machine_id = 'm-a'`); err != nil {
		t.Fatal(err)
	}
	if err := a.shieldRun(ctx, st, relogio.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	pls, _ = st.ShieldPlayersByID(ctx, []int64{pl.ID})
	if pls[pl.ID].XP != 35 {
		t.Fatalf("XP do reinício: %d", pls[pl.ID].XP)
	}

	// Desligado: não avalia mais nada.
	pol.Enabled = false
	if _, err := st.SaveShieldPolicy(ctx, pol, "", "admin"); err != nil {
		t.Fatal(err)
	}
	a.shieldPolicyStale()
	if _, err := pool.Exec(ctx, `UPDATE scan_reports SET reboot_pending = true WHERE machine_id = 'm-a'`); err != nil {
		t.Fatal(err)
	}
	if err := a.shieldRun(ctx, st, relogio.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if s, _, _ := st.ShieldScoreOf(ctx, "m-a"); !s.Result.Sealed {
		t.Fatal("desligado e reavaliou")
	}
}

// TestShieldRotaDoAgente: a "Minha Proteção" pela rota do agente, com a credencial da máquina;
// a máquina vem da credencial e a pessoa do corpo (a identidade que o agente leu do token).
func TestShieldRotaDoAgente(t *testing.T) {
	st, pool := shieldPG(t)
	ctx := context.Background()
	agora := time.Now().UTC()
	cred, hash := identity.NewSecret(identity.CredentialPrefix)
	if _, err := pool.Exec(ctx, `INSERT INTO machines (id, ring, credential_hash, last_seen_at, agent_version) VALUES ('m-a', 1, $1, $2, 'v1.0.0')`, hash, agora); err != nil {
		t.Fatal(err)
	}
	var scan int64
	if err := pool.QueryRow(ctx, `INSERT INTO scan_reports (machine_id, schema_version, scanned_at, reboot_pending, report) VALUES ('m-a', 1, $1, true, '{}') RETURNING id`, agora).Scan(&scan); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE machines SET current_scan_id = $1`, scan); err != nil {
		t.Fatal(err)
	}
	inv := agora.Add(-time.Hour)
	cs := &complianceShield{status: []protocol.ComplianceStatus{{MachineID: "m-a", State: "reboot_pendente", InventoryCollectedAt: &inv, Catalog: "x"}}}
	h := New(st, slog.New(slog.DiscardHandler), WithCompliance(cs))
	pede := func(cred, corpo string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "http://patchd.exemplo"+protocol.ShieldPath, strings.NewReader(corpo))
		req.Header.Set("Content-Type", "application/json")
		if cred != "" {
			req.Header.Set("Authorization", "Bearer "+cred)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	ana := `{"account":"EXEMPLO\\ana.souza","groups":[],"sid":"S-1-5-21-1-2-3-1001"}`
	if w := pede("", ana); w.Code != http.StatusUnauthorized {
		t.Fatalf("sem credencial: %d", w.Code)
	}
	var v protocol.ShieldView
	w := pede(cred, ana)
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil || w.Code != 200 || v.Enabled || v.Reason == "" {
		t.Fatalf("desligado: %d %s", w.Code, w.Body.String())
	}
	pol := shield.Default()
	pol.Enabled = true
	if _, err := st.SaveShieldPolicy(ctx, pol, "", "admin"); err != nil {
		t.Fatal(err)
	}
	a := &api{store: st, compliance: cs, logger: slog.New(slog.DiscardHandler), now: func() time.Time { return agora }}
	if err := a.shieldRun(ctx, st, agora); err != nil {
		t.Fatal(err)
	}
	h = New(st, slog.New(slog.DiscardHandler), WithCompliance(cs)) // cache da política novo
	w = pede(cred, ana)
	v = protocol.ShieldView{}
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil || !v.Enabled || v.Machine == nil || v.Player == nil {
		t.Fatalf("ligado: %d %s", w.Code, w.Body.String())
	}
	if v.Machine.Sealed || v.Machine.Display >= 100 {
		t.Fatalf("reinício pendente com selo: %+v", v.Machine)
	}
	acao := false
	for _, c := range v.Machine.Criteria {
		acao = acao || (c.Criterion == "reinicio" && c.Action == "reiniciar")
	}
	if !acao || len(v.Notices) == 0 {
		t.Fatalf("a ação do reinício e o aviso: %+v", v)
	}
	// A visita conta como presença, e a pessoa ganha o SID.
	pl, ok, _ := st.ShieldPlayerByAccount(ctx, `exemplo\ana.souza`)
	if !ok || pl.SID != "S-1-5-21-1-2-3-1001" {
		t.Fatalf("%+v", pl)
	}
	on, _ := st.ShieldPresentOn(ctx, dateOf(agora, time.UTC))
	if len(on["m-a"]) != 1 {
		t.Fatalf("presença: %+v", on)
	}
	// Identidade fora do formato: recusada.
	if w := pede(cred, `{"account":"","groups":[]}`); w.Code != http.StatusBadRequest {
		t.Fatalf("conta vazia: %d", w.Code)
	}
}

// A missão coletiva cumprida antes de zerar não paga de novo nos dias seguintes (o XP dela
// ficou para trás com o zerar); a cumprida depois paga.
func TestShieldZerarNaoRepagaMissao(t *testing.T) {
	st, pool := shieldPG(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO machines (id, ring) VALUES ('m-a', 1)`); err != nil {
		t.Fatal(err)
	}
	ids, _ := st.ShieldTouchAccounts(ctx, map[string]string{`exemplo\ana`: ""})
	ana := ids[`exemplo\ana`]
	d := time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)
	if err := st.AddShieldPresence(ctx, []store.ShieldPresence{{PlayerID: ana, MachineID: "m-a", Day: d}}); err != nil {
		t.Fatal(err)
	}
	mid, err := st.SaveShieldMission(ctx, store.ShieldMission{Title: "Semana", Scope: "coletiva", Goal: store.MissionGoal{Percent: 90},
		StartsAt: d, EndsAt: d.AddDate(0, 0, 7), State: "publicada", XP: 50, CreatedBy: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := st.CompleteShieldMission(ctx, mid, d.Add(10*time.Hour)); err != nil || !ok {
		t.Fatalf("cumprir: %v %v", ok, err)
	}
	pol := shield.Default()
	a := &api{store: st, logger: slog.New(slog.DiscardHandler), now: time.Now}
	xp := func() int64 {
		pls, _ := st.ShieldPlayersByID(ctx, []int64{ana})
		return pls[ana].XP
	}
	if err := a.shieldCloseDay(ctx, st, &pol, d); err != nil || xp() < 50 {
		t.Fatalf("antes de zerar, a missão paga: %d %v", xp(), err)
	}
	if _, err := st.ResetShieldPoints(ctx, d, "início do programa", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := a.shieldCloseDay(ctx, st, &pol, d.AddDate(0, 0, 1)); err != nil || xp() != 0 {
		t.Fatalf("a missão cumprida antes de zerar pagou de novo: %d %v", xp(), err)
	}
}
