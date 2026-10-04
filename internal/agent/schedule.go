package agent

import "time"

// Tempos da agenda. RNF-03: check-in de hora em hora com sorteio, busca uma vez por dia.
const (
	initialSplayMax = 5 * time.Minute  // espera aleatória na partida: a frota não liga toda junta
	checkinJitter   = 0.10             // ±10% no intervalo de check-in
	backoffMin      = 30 * time.Second // primeira nova tentativa depois de falha transitória
	scanJitter      = 2 * time.Hour    // ±2 h na próxima busca: as buscas da frota se espalham no dia
)

// randFn devolve um inteiro em [0, n). Injetável nos testes.
type randFn func(n int64) int64

// between devolve uma duração aleatória em [lo, hi].
func between(r randFn, lo, hi time.Duration) time.Duration {
	if hi <= lo {
		return lo
	}
	return lo + time.Duration(r(int64(hi-lo)+1))
}

// initialDelay é a espera antes do primeiro check-in: até 5 min, nunca mais que o intervalo.
func initialDelay(r randFn, interval time.Duration) time.Duration {
	return between(r, 0, min(initialSplayMax, interval))
}

// nextCheckin é a espera normal: o intervalo com ±10%.
func nextCheckin(r randFn, interval time.Duration) time.Duration {
	d := time.Duration(float64(interval) * checkinJitter)
	return between(r, interval-d, interval+d)
}

// backoff é a espera depois da n-ésima falha transitória seguida (n ≥ 1): dobra a cada
// falha, a partir de 30 s, até o intervalo de check-in; o valor final é sorteado entre a
// metade e o total. Sem o sorteio, mil agentes que perderam o servidor juntos voltariam
// juntos, todos no mesmo segundo, e o derrubariam de novo.
func backoff(r randFn, n int, interval time.Duration) time.Duration {
	d := backoffMin
	for i := 1; i < n && d < interval; i++ {
		d *= 2
	}
	d = min(d, interval)
	return between(r, d/2, d)
}

// nextScanAt é quando a próxima busca fica devida: o período, ±2 h.
func nextScanAt(r randFn, last time.Time, every time.Duration) time.Time {
	j := min(scanJitter, every/4)
	return last.Add(between(r, every-j, every+j))
}
