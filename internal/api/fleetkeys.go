package api

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/release"
)

// fleetKeysWindow: só contam as máquinas com estado recente; uma máquina parada há meses
// não deve travar a troca da chave (ela recebe o agente novo quando voltar, antes de jobs).
const fleetKeysWindow = 30 * 24 * time.Hour

// fleetKeysRow (Aula 20.7, S8) responde, antes de cada passo da troca de chave, "a frota
// inteira já aceita a chave nova?". Nível "atencao" quando alguma máquina que informa as
// chaves não aceita a chave de jobs do servidor: os jobs para ela seriam recusados.
func (a *api) fleetKeysRow(ctx context.Context, now time.Time) (string, string, error) {
	ks, err := a.store.KeyStats(ctx, now.Add(-fleetKeysWindow))
	if err != nil {
		return "", "", err
	}
	if ks.Machines == 0 {
		return "nenhuma máquina com estado nos últimos 30 dias", "", nil
	}
	var parts []string
	level := ""
	if a.jobKey != nil {
		id := jobs.KeyID(a.jobKey.Public().(ed25519.PublicKey))
		parts = append(parts, fmt.Sprintf("jobs %s (a do servidor): %d de %d", id, ks.Job[id], ks.Reporting))
		if ks.Job[id] < ks.Reporting {
			level = "atencao"
		}
		for _, other := range slices.Sorted(maps.Keys(ks.Job)) {
			if other != id {
				parts = append(parts, fmt.Sprintf("jobs %s: %d", other, ks.Job[other]))
			}
		}
	}
	// Release: as impressões informadas pela frota. O servidor não precisa das chaves de
	// release (o container não as tem); quando o build as embutiu, elas ganham o rótulo e
	// aparecem mesmo com zero máquinas.
	notes := releaseKeyNotes()
	ids := slices.Sorted(maps.Keys(ks.Release))
	for id := range notes {
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	for _, id := range ids {
		parts = append(parts, fmt.Sprintf("release %s: %d de %d", id, ks.Release[id], ks.Reporting)+notes[id])
	}
	if old := ks.Machines - ks.Reporting; old > 0 {
		parts = append(parts, fmt.Sprintf("%d máquina(s) com agente anterior à 20.7 não informam", old))
	}
	return strings.Join(parts, " · "), level, nil
}

// releaseKeyNotes: as chaves de release embutidas neste build (scripts/build.sh), com o rótulo.
func releaseKeyNotes() map[string]string {
	out := map[string]string{}
	for _, k := range []struct{ b64, note string }{{release.PublicKey, " (atual)"}, {release.NextPublicKey, " (seguinte)"}} {
		if pub, err := release.ParsePublicKey(k.b64); err == nil {
			out[release.KeyID(pub)] = k.note
		}
	}
	return out
}
