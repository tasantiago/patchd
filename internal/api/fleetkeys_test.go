package api_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/release"
)

// TestChavesNaFrota (Aula 20.7, S8): a linha que diz se a frota já aceita a chave nova,
// antes de cada passo da troca.
func TestChavesNaFrota(t *testing.T) {
	pubJob, privJob, _ := jobs.GenerateKey()
	nextJob, _, _ := jobs.GenerateKey()
	relAtual, _, _ := ed25519.GenerateKey(rand.Reader)
	relNext, _, _ := ed25519.GenerateKey(rand.Reader)
	velhoA, velhoN := release.PublicKey, release.NextPublicKey
	release.PublicKey, release.NextPublicKey = release.EncodePublicKey(relAtual), release.EncodePublicKey(relNext)
	t.Cleanup(func() { release.PublicKey, release.NextPublicKey = velhoA, velhoN })

	h, st, _ := ambientePaginas(t, api.WithJobKey(privJob))
	ctx := context.Background()
	linha := func() string {
		b := pagina(h, "GET", "/painel/configuracao", sessaoAdmin, nil).Body.String()
		i := strings.Index(b, "<dt>Chaves na frota</dt>")
		if i < 0 {
			t.Fatalf("sem a linha:\n%s", b)
		}
		return b[i : i+strings.Index(b[i:], "</dd>")]
	}
	if l := linha(); !strings.Contains(l, "nenhuma máquina com estado") {
		t.Errorf("sem estado: %s", l)
	}

	jid, jnext := jobs.KeyID(pubJob), jobs.KeyID(nextJob)
	ra, rn := release.KeyID(relAtual), release.KeyID(relNext)
	// Uma máquina já com o agente novo (aceita as duas), outra com o da 20.7 sem a seguinte.
	_ = st.SaveHostState(ctx, "60ef97e4-0001", &protocol.MachineState{CollectedAt: time.Now().UTC(),
		JobKeys: []string{jid, jnext}, ReleaseKeys: []string{ra, rn}}, "192.0.2.1")
	_ = st.SaveHostState(ctx, "7aa604f7-0002", &protocol.MachineState{CollectedAt: time.Now().UTC(),
		JobKeys: []string{jid}, ReleaseKeys: []string{ra}}, "192.0.2.2")
	l := linha()
	for _, quer := range []string{"<dd>jobs " + jid + " (a do servidor): 2 de 2", "jobs " + jnext + ": 1",
		"release " + ra + ": 2 de 2 (atual)", "release " + rn + ": 1 de 2 (seguinte)"} {
		if !strings.Contains(l, quer) {
			t.Errorf("faltou %q em %s", quer, l)
		}
	}

	// Uma máquina que não aceita a chave do servidor (o servidor trocou cedo demais): atenção.
	_ = st.SaveHostState(ctx, "7aa604f7-0002", &protocol.MachineState{CollectedAt: time.Now().UTC(),
		JobKeys: []string{jnext}, ReleaseKeys: []string{ra}}, "192.0.2.2")
	if l := linha(); !strings.Contains(l, `<dd class="nivel-atencao">jobs `+jid+" (a do servidor): 1 de 2") {
		t.Errorf("atenção: %s", l)
	}

	// Agente anterior à 20.7 (sem as listas): contado à parte.
	_ = st.SaveHostState(ctx, "7aa604f7-0002", &protocol.MachineState{CollectedAt: time.Now().UTC()}, "192.0.2.2")
	if l := linha(); !strings.Contains(l, "1 de 1") || !strings.Contains(l, "1 máquina(s) com agente anterior à 20.7") {
		t.Errorf("agente antigo: %s", l)
	}
	// Sem as chaves de release no build (o container do servidor): as impressões da frota,
	// sem rótulo.
	release.PublicKey, release.NextPublicKey = "", ""
	if l := linha(); !strings.Contains(l, "release "+ra+": 1 de 1 · release "+rn+": 1 de 1 ·") && !strings.Contains(l, "release "+rn+": 1 de 1 · release "+ra+": 1 de 1 ·") {
		t.Errorf("sem rótulos: %s", l)
	}
}
