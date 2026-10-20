package store

import (
	"context"
	"testing"
	"time"
)

func TestCopiasDoBanco(t *testing.T) {
	pool := bancoDeTeste(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	p := NewPostgres(pool)
	if _, found, err := p.LastBackup(ctx); found || err != nil {
		t.Fatalf("sem cópias: %v %v", found, err)
	}
	ontem := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Microsecond)
	hoje := ontem.Add(24 * time.Hour)
	for _, b := range []BackupRun{
		{FinishedAt: hoje, File: "patchd-20261012T023000Z.dump.age", Bytes: 2048, Encrypted: true},
		{FinishedAt: ontem, File: "patchd-20261011T023000Z.dump", Bytes: 1024},
	} {
		if err := p.RecordBackup(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	b, found, err := p.LastBackup(ctx)
	if err != nil || !found || !b.FinishedAt.Equal(hoje) || !b.Encrypted || b.Bytes != 2048 {
		t.Errorf("mais recente: %+v %v %v", b, found, err)
	}
	// O banco recusa o que não é nome de cópia (uma pasta, por exemplo) e tamanho zero.
	for _, ruim := range []BackupRun{
		{FinishedAt: hoje, File: "/srv/copias/patchd-20261012T023000Z.dump", Bytes: 1},
		{FinishedAt: hoje, File: "patchd-20261012T023000Z.dump", Bytes: 0},
	} {
		if err := p.RecordBackup(ctx, ruim); err == nil {
			t.Errorf("aceito: %+v", ruim)
		}
	}

	// Ensaios de restauração (Aula 20.8, F4).
	if _, found, err := p.LastRestoreTest(ctx); found || err != nil {
		t.Fatalf("sem ensaios: %v %v", found, err)
	}
	for _, r := range []RestoreTest{
		{FinishedAt: ontem, File: "patchd-20261011T023000Z.dump", OK: true},
		{FinishedAt: hoje, File: "patchd-20261012T023000Z.dump.age", OK: false},
	} {
		if err := p.RecordRestoreTest(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	r, found, err := p.LastRestoreTest(ctx)
	if err != nil || !found || !r.FinishedAt.Equal(hoje) || r.OK || r.File != "patchd-20261012T023000Z.dump.age" {
		t.Errorf("ensaio mais recente: %+v %v %v", r, found, err)
	}
	if err := p.RecordRestoreTest(ctx, RestoreTest{FinishedAt: hoje, File: "../patchd.dump", OK: true}); err == nil {
		t.Error("ensaio com nome que não é de cópia aceito")
	}
}
