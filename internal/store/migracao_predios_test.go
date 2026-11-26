package store

import (
	"context"
	"io/fs"
	"path"
	"strings"
	"testing"
	"testing/fstest"
)

// A migração 0085 num banco com campanhas antigas: a legada continua legível e igual (os
// grupos, os jobs, sem escopo), e os prédios ganham ID a partir do patrimônio e dos vínculos
// antigos, sem apagar nada.
func TestMigracaoDosPrediosComCampanhasAntigas(t *testing.T) {
	pool := bancoDeTeste(t)
	ctx := context.Background()
	antes := fstest.MapFS{}
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() >= "0085" {
			continue
		}
		b, _ := fs.ReadFile(migrationFiles, path.Join("migrations", e.Name()))
		antes["m/"+e.Name()] = &fstest.MapFile{Data: b}
	}
	if err := migrate(ctx, pool, silencioso(), antes, "m"); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO campaigns (name, type, sources, rings, use_window, observe_sec, ttl_sec, state, created_by) VALUES ('antiga', 'rescan', '{}', '{0,1}', false, 3600, 3600, 'em_andamento', 't')`,
		`INSERT INTO asset_locations (tombo, building) VALUES ('1001', 'PREDIO A'), ('1002', 'PREDIO A'), ('2001', 'Predio a')`,
		`INSERT INTO building_rings (building, ring, updated_by) VALUES ('PREDIO VELHO', 3, 't')`,
		`INSERT INTO floor_locations (building, spec, name, updated_by) VALUES ('PREDIO A', 'regex:^10\.1\.', '1º andar', 't')`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := Migrate(ctx, pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	pg := NewPostgres(pool)
	c, ok, err := pg.Campaign(ctx, 1)
	if err != nil || !ok || c.Scope != nil || c.Located() || len(c.Rings) != 2 || c.Rings[1] != 1 || c.Name != "antiga" {
		t.Fatalf("campanha antiga: %+v %v", c, err)
	}
	bs, err := pg.Buildings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, b := range bs {
		s := b.Name
		if !b.Present {
			s += " (ausente)"
		}
		got = append(got, s)
	}
	if strings.Join(got, "; ") != "PREDIO A; PREDIO VELHO (ausente); Predio a" {
		t.Fatalf("prédios: %v", got)
	}
	// A campanha legada segue no motor: o grupo atual, os jobs dela (nenhum alvo por localização).
	if ts, err := pg.CampaignTargets(ctx, 1, -1); err != nil || len(ts) != 0 {
		t.Fatalf("alvos da legada: %v %v", ts, err)
	}
	c.LaunchedIdx, c.Detail = 0, "grupo de implantação 0: 0 de 0"
	if ok, err := pg.SaveCampaign(ctx, c, "em_andamento"); err != nil || !ok {
		t.Fatalf("gravar a legada: %v %v", ok, err)
	}
	// A constraint nova não deixa nascer campanha sem grupos e sem escopo.
	if _, err := pool.Exec(ctx, `INSERT INTO campaigns (name, type, sources, rings, use_window, observe_sec, ttl_sec, state, created_by)
		VALUES ('sem nada', 'rescan', '{}', '{}', false, 3600, 3600, 'em_andamento', 't')`); err == nil {
		t.Error("campanha sem grupos nem escopo aceita")
	}
}
