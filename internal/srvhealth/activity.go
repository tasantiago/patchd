package srvhealth

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

// O que o banco está executando (docs/saude-servidor.md, quarta rodada). O pg_stat_statements
// pede mudar a configuração e reiniciar o PostgreSQL; isto não pede nada: a cada poolTick o
// monitor lê o pg_stat_activity e soma, por consulta, quantas vezes ela foi vista em execução.
// Uma consulta que aparece em metade das amostras ocupa meia conexão o tempo todo: é a lista
// de onde o banco gasta o tempo, sem números exatos, mas com o culpado no topo.
//
// O texto é o do comando preparado, com $1, $2 no lugar dos valores: nenhum dado de máquina
// ou de usuário entra aqui.

// Activity é uma consulta em execução numa amostra.
type Activity struct {
	Query string
	Wait  string // wait_event_type do PostgreSQL; "" = usando CPU
}

// ActivityReader: quem sabe ler as consultas em execução (o PostgreSQL).
type ActivityReader interface {
	DBActivity(ctx context.Context) ([]Activity, error)
}

// QueryLoad é a soma de uma consulta nas amostras.
type QueryLoad struct {
	Query   string
	Samples int // vezes vista em execução
	CPU     int // dessas, usando CPU (sem espera)
	IO      int // esperando disco
	Lock    int // esperando trava
	Client  int // esperando o servidor do patchd ler ou mandar
}

const (
	maxQueryText = 220 // o começo basta para achar a consulta no código
	maxQueries   = 300 // as consultas são as do código: um número pequeno e fixo
	activityWait = 3 * time.Second
)

type activityLog struct {
	mu      sync.Mutex
	busy    bool
	ticks   int // amostras feitas
	active  int // consultas vistas, somadas
	queries map[string]*QueryLoad
}

// normQuery: uma linha, sem espaços repetidos, cortada.
func normQuery(q string) string {
	q = strings.Join(strings.Fields(q), " ")
	if r := []rune(q); len(r) > maxQueryText {
		q = string(r[:maxQueryText]) + "…"
	}
	return q
}

func (l *activityLog) add(list []Activity) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.queries == nil {
		l.queries = map[string]*QueryLoad{}
	}
	l.ticks++
	for _, a := range list {
		q := normQuery(a.Query)
		if q == "" {
			continue
		}
		ql := l.queries[q]
		if ql == nil {
			if len(l.queries) >= maxQueries {
				q = "(outras)"
				if ql = l.queries[q]; ql == nil {
					ql = &QueryLoad{Query: q}
					l.queries[q] = ql
				}
			} else {
				ql = &QueryLoad{Query: q}
				l.queries[q] = ql
			}
		}
		l.active++
		ql.Samples++
		switch a.Wait {
		case "":
			ql.CPU++
		case "IO":
			ql.IO++
		case "Lock", "LWLock", "BufferPin":
			ql.Lock++
		case "Client":
			ql.Client++
		}
	}
}

// sampleActivity lê as consultas em execução, sem segurar o laço do monitor: uma leitura por
// vez, com prazo curto (com o pool cheio, a amostra é perdida, não enfileirada).
func (m *Monitor) sampleActivity(ctx context.Context) {
	r, ok := m.keeper.(ActivityReader)
	if !ok {
		return
	}
	l := &m.activity
	l.mu.Lock()
	if l.busy {
		l.mu.Unlock()
		return
	}
	l.busy = true
	l.mu.Unlock()
	go func() {
		defer func() {
			l.mu.Lock()
			l.busy = false
			l.mu.Unlock()
		}()
		sctx, cancel := context.WithTimeout(ctx, activityWait)
		defer cancel()
		list, err := r.DBActivity(sctx)
		if err != nil {
			return
		}
		l.add(list)
	}()
}

// Activity devolve as consultas mais vistas desde a partida (no máximo n), o número de
// amostras e a soma das consultas vistas.
func (m *Monitor) Activity(n int) (top []QueryLoad, ticks, active int) {
	l := &m.activity
	l.mu.Lock()
	for _, q := range l.queries {
		top = append(top, *q)
	}
	ticks, active = l.ticks, l.active
	l.mu.Unlock()
	sort.Slice(top, func(i, j int) bool {
		if top[i].Samples != top[j].Samples {
			return top[i].Samples > top[j].Samples
		}
		return top[i].Query < top[j].Query
	})
	if len(top) > n {
		top = top[:n]
	}
	return top, ticks, active
}

// AddActivity soma uma amostra pronta (os testes do painel; a medição usa Run).
func (m *Monitor) AddActivity(list []Activity) { m.activity.add(list) }
