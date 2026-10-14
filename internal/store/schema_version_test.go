package store

import "testing"

// A Configuração mostra a migração mais nova do binário (Aula 15.2).
func TestSchemaVersion(t *testing.T) {
	list, err := loadMigrations(migrationFiles, "migrations")
	if err != nil || len(list) == 0 {
		t.Fatal(err)
	}
	if v := SchemaVersion(); v != list[len(list)-1].version || v < 30 {
		t.Errorf("SchemaVersion: %d", v)
	}
}
