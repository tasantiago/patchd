package main

import (
	"log/slog"
	"os"
	"runtime/debug"

	"github.com/tasantiago/patchd/internal/srvhealth"
)

// O limite brando de memória do Go (docs/saude-servidor.md). Sem ele, o coletor deixa o heap
// crescer até o dobro do que está vivo antes de limpar, e o processo segura essa memória: numa
// máquina pequena, que divide a memória com o PostgreSQL, isso tira do banco o cache de disco.
// Com o limite, o coletor trabalha mais perto dele (gasta um pouco mais de CPU) e devolve a
// sobra ao sistema. GOMEMLIMIT no ambiente (inclusive "off") vale no lugar deste cálculo.
const (
	// memShareHost: a fração da memória da máquina quando o processo não tem limite próprio
	// (o PostgreSQL e o sistema ficam com o resto).
	memShareHost = 0.40
	// memShareCgroup: a fração do limite do container (só o patchd está nele).
	memShareCgroup = 0.75
	minSoftLimit   = 512 << 20
)

// softMemoryLimit calcula o limite. ok = false: o ambiente já define, ou a memória não se sabe.
func softMemoryLimit(env string, envSet bool, budget uint64, fromCgroup bool) (int64, bool) {
	if envSet || env != "" || budget == 0 {
		return 0, false
	}
	share := memShareHost
	if fromCgroup {
		share = memShareCgroup
	}
	l := int64(float64(budget) * share)
	if l < minSoftLimit {
		l = minSoftLimit
	}
	return l, true
}

func applySoftMemoryLimit(logger *slog.Logger) {
	env, set := os.LookupEnv("GOMEMLIMIT")
	budget, cg := srvhealth.MemoryBudget()
	l, ok := softMemoryLimit(env, set, budget, cg)
	if !ok {
		return
	}
	debug.SetMemoryLimit(l)
	logger.Info("limite brando de memória do Go definido (GOMEMLIMIT no ambiente troca)", "limite", srvhealth.Bytes(uint64(l)), // #nosec G115 -- positivo
		"memoria", srvhealth.Bytes(budget), "do_container", cg)
}
