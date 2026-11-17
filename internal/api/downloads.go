package api

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// Os downloads em andamento (docs/saude-servidor.md): quantas máquinas estão puxando arquivos
// do servidor agora, por tipo de arquivo. A tabela de rotas da aba Saúde só conta o download
// quando ele termina; com arquivos de 1 GB, o que pesa é o que está no meio do caminho.
// Fica só na memória: o número de agora e o pico desde a partida.

type downloadKind int

const (
	dlWinCache downloadKind = iota // cache do Windows Update
	dlRelease                      // versões do agente
	dlPackage                      // instaladores de terceiros
	dlCatalog                      // catálogo offline (wsusscn2.cab)
	dlBundle                       // pacotes de scripts
	dlKinds
)

var downloadLabels = [dlKinds]string{"do cache do Windows", "de versões do agente", "de instaladores", "do catálogo offline", "de scripts"}

type downloadTracker struct {
	mu       sync.Mutex
	n        [dlKinds]int
	machines map[string]int // máquina → downloads dela em andamento
	peak     int
	peakAt   time.Time
}

// begin conta um download que começa; a função devolvida conta o fim (uma vez só).
func (t *downloadTracker) begin(kind downloadKind, machine string, now time.Time) func() {
	t.mu.Lock()
	if t.machines == nil {
		t.machines = map[string]int{}
	}
	t.n[kind]++
	t.machines[machine]++
	if total := t.total(); total > t.peak {
		t.peak, t.peakAt = total, now
	}
	t.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			t.n[kind]--
			if t.machines[machine]--; t.machines[machine] <= 0 {
				delete(t.machines, machine)
			}
			t.mu.Unlock()
		})
	}
}

func (t *downloadTracker) total() int {
	sum := 0
	for _, n := range t.n {
		sum += n
	}
	return sum
}

// downloadStats é o retrato para a aba Saúde.
type downloadStats struct {
	Total, Machines int
	ByKind          [dlKinds]int
	Peak            int
	PeakAt          time.Time
}

func (t *downloadTracker) stats() downloadStats {
	t.mu.Lock()
	defer t.mu.Unlock()
	return downloadStats{Total: t.total(), Machines: len(t.machines), ByKind: t.n, Peak: t.peak, PeakAt: t.peakAt}
}

// downloadsRow: a linha "Downloads agora" do quadro "Processo e disco".
func downloadsRow(s downloadStats, stamp func(time.Time) string) settingRow {
	v := "nenhum em andamento"
	if s.Total > 0 {
		var parts []string
		for k, n := range s.ByKind {
			if n > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", n, downloadLabels[k]))
			}
		}
		v = fmt.Sprintf("%d em andamento, de %d máquina(s): %s", s.Total, s.Machines, strings.Join(parts, ", "))
	}
	if s.Peak > 0 {
		v += fmt.Sprintf(" · pico desde a partida: %d (%s)", s.Peak, stamp(s.PeakAt))
	}
	return settingRow{Label: "Downloads agora", Value: v}
}
