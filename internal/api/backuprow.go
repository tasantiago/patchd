package api

import (
	"fmt"
	"time"

	"github.com/tasantiago/patchd/internal/store"
)

// BackupStatus (Aula 20.8, F4): o que o banco sabe das cópias. O backup.sh registra cada
// cópia conferida (Aula 10.3) e o restaurar.sh --testar, cada ensaio.
type BackupStatus struct {
	Last    store.BackupRun
	HasLast bool
	Test    store.RestoreTest
	HasTest bool
}

// Prazos da linha: o timer roda todo dia (36 h dão folga para um atraso), e o ensaio é mensal.
const (
	backupMaxAge      = 36 * time.Hour
	restoreTestMaxAge = 35 * 24 * time.Hour
)

// backupRow: a linha "Cópias do banco". O nível é o pior dos problemas: sem cópia ou ensaio
// que falhou é erro; cópia velha, sem cifra, sem ensaio ou ensaio velho é atenção. Fora do
// host o servidor não enxerga: isso continua no roteiro (docs/seguranca.md, F4).
func backupRow(b BackupStatus, now time.Time) (string, string) {
	if !b.HasLast {
		return "nenhuma cópia registrada: ligue o deploy/backup.sh e o timer (docs/operacao.md)", "erro"
	}
	level := ""
	worse := func(l string) {
		if l == "erro" || level == "" {
			level = l
		}
	}
	v := "última " + ago(now.Sub(b.Last.FinishedAt))
	if b.Last.Encrypted {
		v += ", cifrada"
	} else {
		v += ", sem cifra (backup_recipients)"
		worse("atencao")
	}
	if now.Sub(b.Last.FinishedAt) > backupMaxAge {
		v += " (o timer parou?)"
		worse("atencao")
	}
	switch {
	case !b.HasTest:
		v += " · nenhum ensaio de restauração (restaurar.sh --testar)"
		worse("atencao")
	case !b.Test.OK:
		v += fmt.Sprintf(" · o ensaio %s NÃO conferiu (%s)", ago(now.Sub(b.Test.FinishedAt)), b.Test.File)
		worse("erro")
	default:
		v += fmt.Sprintf(" · ensaio de restauração %s, conferiu", ago(now.Sub(b.Test.FinishedAt)))
		if now.Sub(b.Test.FinishedAt) > restoreTestMaxAge {
			v += " (o ensaio é mensal)"
			worse("atencao")
		}
	}
	return v, level
}
