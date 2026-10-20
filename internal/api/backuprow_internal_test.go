package api

import (
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/store"
)

// TestLinhaDasCopias (Aula 20.8, F4): cada situação das cópias e dos ensaios, com o nível.
func TestLinhaDasCopias(t *testing.T) {
	agora := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	copia := func(h int, cif bool) store.BackupRun {
		return store.BackupRun{FinishedAt: agora.Add(-time.Duration(h) * time.Hour), File: "patchd-20261020T023000Z.dump.age", Bytes: 1, Encrypted: cif}
	}
	ensaio := func(dias int, ok bool) store.RestoreTest {
		return store.RestoreTest{FinishedAt: agora.Add(-time.Duration(dias) * 24 * time.Hour), File: "patchd-20261001T023000Z.dump.age", OK: ok}
	}
	casos := []struct {
		nome  string
		b     BackupStatus
		nivel string
		quer  []string
	}{
		{"nenhuma", BackupStatus{}, "erro", []string{"nenhuma cópia registrada"}},
		{"tudo em dia", BackupStatus{Last: copia(6, true), HasLast: true, Test: ensaio(12, true), HasTest: true}, "",
			[]string{"última há 6 h, cifrada", "ensaio de restauração há 12 dias, conferiu"}},
		{"sem cifra", BackupStatus{Last: copia(6, false), HasLast: true, Test: ensaio(12, true), HasTest: true}, "atencao", []string{"sem cifra"}},
		{"timer parado", BackupStatus{Last: copia(40, true), HasLast: true, Test: ensaio(12, true), HasTest: true}, "atencao", []string{"o timer parou?"}},
		{"sem ensaio", BackupStatus{Last: copia(6, true), HasLast: true}, "atencao", []string{"nenhum ensaio"}},
		{"ensaio velho", BackupStatus{Last: copia(6, true), HasLast: true, Test: ensaio(40, true), HasTest: true}, "atencao", []string{"o ensaio é mensal"}},
		// Erro vence atenção, em qualquer ordem.
		{"ensaio falhou", BackupStatus{Last: copia(40, false), HasLast: true, Test: ensaio(2, false), HasTest: true}, "erro",
			[]string{"NÃO conferiu", "patchd-20261001T023000Z.dump.age"}},
	}
	for _, c := range casos {
		v, l := backupRow(c.b, agora)
		if l != c.nivel {
			t.Errorf("%s: nível %q, quer %q (%s)", c.nome, l, c.nivel, v)
		}
		for _, q := range c.quer {
			if !strings.Contains(v, q) {
				t.Errorf("%s: faltou %q em %q", c.nome, q, v)
			}
		}
	}
}
