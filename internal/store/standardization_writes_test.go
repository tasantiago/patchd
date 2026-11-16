package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

// O plano e o relato iguais aos guardados não regravam a linha (a padronização roda a cada
// check-in e a cada 5 minutos por usuário); a hora é renovada de hora em hora. E o sistema da
// máquina sai da coluna à parte, sem ler o relatório.
func TestPadronizacaoGravaSoQuandoMudaPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	st := NewPostgres(pool)
	if _, err := st.SaveInventory(ctx, "pc", protocol.InventoryReport{SchemaVersion: 1, Hash: "sha256:aa",
		OS: protocol.OSInfo{Hostname: "pc", ID: "windows", Family: "windows"}}); err != nil {
		t.Fatal(err)
	}
	if f, ok, err := st.MachinePlatform(ctx, "pc"); err != nil || !ok || f != "windows" {
		t.Errorf("sistema: %q %v %v", f, ok, err)
	}
	if _, ok, err := st.MachinePlatform(ctx, "nao-existe"); err != nil || ok {
		t.Errorf("máquina sem inventário: %v %v", ok, err)
	}
	versao := func() (xmin string, evaluated, shortcuts *time.Time) {
		t.Helper()
		var e time.Time
		if err := pool.QueryRow(ctx, `SELECT xmin::text, evaluated_at, shortcuts_at FROM standardization_states WHERE machine_id='pc' AND account='ana'`).Scan(&xmin, &e, &shortcuts); err != nil {
			t.Fatal(err)
		}
		return xmin, &e, shortcuts
	}
	t0 := time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC)
	r1, r0, r2 := strings.Repeat("1", 64), strings.Repeat("0", 64), strings.Repeat("2", 64)
	plano := protocol.StandardizationPlan{Revision: r1, Policies: []string{"Atalhos"}, Shortcuts: []protocol.StandardShortcut{},
		Apps: []protocol.StandardAppState{}, GeneratedAt: t0}
	salva := func(p protocol.StandardizationPlan, at time.Time) {
		t.Helper()
		p.GeneratedAt = at
		if err := st.SaveStandardizationState(ctx, StandardizationState{MachineID: "pc", Account: "Ana", Plan: p, EvaluatedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	salva(plano, t0)
	x1, e1, _ := versao()
	// O mesmo plano 10 minutos depois: a linha não muda.
	salva(plano, t0.Add(10*time.Minute))
	if x2, e2, _ := versao(); x2 != x1 || !e2.Equal(*e1) {
		t.Errorf("plano igual regravou: %s→%s, %v→%v", x1, x2, e1, e2)
	}
	// Uma hora depois, a hora da avaliação é renovada.
	salva(plano, t0.Add(61*time.Minute))
	if _, e3, _ := versao(); !e3.Equal(t0.Add(61 * time.Minute)) {
		t.Errorf("hora não renovada: %v", e3)
	}

	// O relato: o primeiro grava; o igual não; o diferente grava; o de outra revisão é recusado.
	rel := protocol.StandardizationResult{Revision: r1, Items: []protocol.StandardShortcutResult{{Name: "Portal", State: "aplicado"}}}
	if err := st.ReportStandardization(ctx, "pc", "ana", rel, t0.Add(62*time.Minute)); err != nil {
		t.Fatal(err)
	}
	x4, _, s4 := versao()
	if err := st.ReportStandardization(ctx, "pc", "ANA", rel, t0.Add(70*time.Minute)); err != nil {
		t.Errorf("relato igual: %v", err)
	}
	if x5, _, s5 := versao(); x5 != x4 || !s5.Equal(*s4) {
		t.Errorf("relato igual regravou: %s→%s", x4, x5)
	}
	rel.Items[0].State = "em_dia"
	if err := st.ReportStandardization(ctx, "pc", "ana", rel, t0.Add(71*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if x6, _, s6 := versao(); x6 == x4 || !s6.Equal(t0.Add(71*time.Minute)) {
		t.Errorf("relato diferente não gravou: %s %v", x6, s6)
	}
	rel.Revision = r0
	if err := st.ReportStandardization(ctx, "pc", "ana", rel, t0.Add(72*time.Minute)); !errors.Is(err, ErrStandardizationStale) {
		t.Errorf("revisão velha: %v", err)
	}
	// O plano novo (outra revisão) grava e limpa o relato da revisão anterior.
	plano.Revision = r2
	salva(plano, t0.Add(73*time.Minute))
	if _, _, s7 := versao(); s7 != nil {
		t.Errorf("relato da revisão anterior ficou: %v", s7)
	}
}
