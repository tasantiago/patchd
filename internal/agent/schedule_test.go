package agent

import (
	"testing"
	"time"
)

// Sorteios nos extremos: o menor e o maior valor possível.
func menor(n int64) int64 { return 0 }
func maior(n int64) int64 { return n - 1 }

func TestBackoffDobraAteOIntervalo(t *testing.T) {
	h := time.Hour
	casos := []struct {
		falhas   int
		min, max time.Duration
	}{
		{1, 15 * time.Second, 30 * time.Second},
		{2, 30 * time.Second, time.Minute},
		{3, time.Minute, 2 * time.Minute},
		{8, 30 * time.Minute, h}, // 30 s × 2^7 = 64 min, limitado a 1 h
		{50, 30 * time.Minute, h},
	}
	for _, c := range casos {
		if lo, hi := backoff(menor, c.falhas, h), backoff(maior, c.falhas, h); lo != c.min || hi != c.max {
			t.Errorf("falhas=%d: [%v, %v], esperado [%v, %v]", c.falhas, lo, hi, c.min, c.max)
		}
	}
}

func TestNextCheckinEInitialDelay(t *testing.T) {
	if lo, hi := nextCheckin(menor, time.Hour), nextCheckin(maior, time.Hour); lo != 54*time.Minute || hi != 66*time.Minute {
		t.Errorf("check-in: [%v, %v], esperado [54m, 66m]", lo, hi)
	}
	if hi := initialDelay(maior, time.Hour); hi != 5*time.Minute {
		t.Errorf("espera inicial máxima: %v", hi)
	}
	if hi := initialDelay(maior, 2*time.Minute); hi != 2*time.Minute {
		t.Errorf("espera inicial nunca passa do intervalo: %v", hi)
	}
}

func TestNextScanAt(t *testing.T) {
	last := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
	if lo, hi := nextScanAt(menor, last, 24*time.Hour), nextScanAt(maior, last, 24*time.Hour); !lo.Equal(last.Add(22*time.Hour)) || !hi.Equal(last.Add(26*time.Hour)) {
		t.Errorf("próxima busca: [%v, %v]", lo, hi)
	}
	// Período curto (testes de laboratório): a variação é no máximo um quarto do período.
	if hi := nextScanAt(maior, last, time.Hour); !hi.Equal(last.Add(75 * time.Minute)) {
		t.Errorf("período curto: %v", hi)
	}
}
