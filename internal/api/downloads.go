package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Os downloads em andamento (docs/saude-servidor.md): quantas máquinas estão puxando arquivos
// do servidor agora, por tipo de arquivo. A tabela de rotas da aba Saúde só conta o download
// quando ele termina; com arquivos de 1 GB, o que pesa é o que está no meio do caminho.
// Fica só na memória: o número de agora e o pico desde a partida.
//
// O limite (parâmetro DownloadMaxKey): com ele, o download que passaria do número recebe 503
// "busy" com Retry-After, antes de transferir um byte. O agente trata como falha passageira:
// tenta de novo algumas vezes com espera crescente e, se não der, o job fica adiado (cache do
// Windows, instaladores) ou o arquivo vem no próximo check-in (versão do agente, catálogo
// offline). Os pacotes de scripts ficam fora do limite: são pequenos, e o agente marcaria o
// job como falho.

// DownloadMaxKey: quantos downloads o servidor entrega ao mesmo tempo (0: sem limite).
const DownloadMaxKey = "PATCHD_DOWNLOAD_MAX"

// DefaultDownloadMax: o padrão. Cem downloads de 1 GB ao mesmo tempo num servidor de 2
// núcleos, com o banco na mesma máquina, deixavam o painel lento.
const DefaultDownloadMax = 40

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
	refused  int64 // recusados pelo limite desde a partida
}

// tryBegin: como begin, mas só se o total em andamento (sem contar os tipos fora do limite)
// estiver abaixo de max; 0: sem limite.
func (t *downloadTracker) tryBegin(kind downloadKind, machine string, now time.Time, max int) (func(), bool) {
	if max > 0 && kind != dlBundle { // os scripts ficam fora do limite
		t.mu.Lock()
		limited := t.total() - t.n[dlBundle]
		if limited >= max {
			t.refused++
			t.mu.Unlock()
			return nil, false
		}
		t.mu.Unlock()
	}
	return t.begin(kind, machine, now), true
}

// beginDownload conta o download (e confere o limite do parâmetro). ok=false: já respondeu
// "ocupado" (503 com Retry-After).
func (a *api) beginDownload(w http.ResponseWriter, kind downloadKind, machine string) (func(), bool) {
	done, ok := a.downloads.tryBegin(kind, machine, a.now(), a.downloadMax())
	if !ok {
		busy(w)
		return nil, false
	}
	return done, true
}

// downloadMax: o limite de agora (o parâmetro vale na hora). Sem parâmetros: sem limite.
func (a *api) downloadMax() int {
	if a.params == nil {
		return 0
	}
	n, err := strconv.Atoi(a.params.Value(DownloadMaxKey))
	if err != nil || n < 0 {
		return DefaultDownloadMax
	}
	return n
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
	Refused         int64
	Max             int // o limite (0: sem limite)
}

func (t *downloadTracker) stats() downloadStats {
	t.mu.Lock()
	defer t.mu.Unlock()
	return downloadStats{Total: t.total(), Machines: len(t.machines), ByKind: t.n, Peak: t.peak, PeakAt: t.peakAt, Refused: t.refused}
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
	if s.Max > 0 {
		v += fmt.Sprintf(" · limite de %d ao mesmo tempo", s.Max)
	} else {
		v += " · sem limite"
	}
	if s.Refused > 0 {
		v += fmt.Sprintf(" · %d recusado(s) pelo limite desde a partida (a máquina tenta de novo depois)", s.Refused)
	}
	return settingRow{Label: "Downloads agora", Value: v}
}
