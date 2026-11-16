package srvhealth

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

type bancoAtivo struct {
	fakeKeeper
	lista []Activity
}

func (b *bancoAtivo) DBActivity(context.Context) ([]Activity, error) { return b.lista, nil }

// O que o banco executa: as consultas vistas nas amostras, somadas por texto (sem os espaços e
// as quebras de linha), com onde cada uma mais ficou; a mais vista primeiro.
func TestConsultasEmExecucao(t *testing.T) {
	m := &Monitor{}
	l := &m.activity
	pesada := "SELECT payload\n\t\tFROM   inventory_reports WHERE id = $1"
	for range 3 {
		l.add([]Activity{{Query: pesada}, {Query: pesada, Wait: "IO"}, {Query: "UPDATE machines SET last_seen_at = $1", Wait: "Lock"}})
	}
	l.add(nil) // uma amostra com o banco parado também conta
	l.add([]Activity{{Query: strings.Repeat("x", 500), Wait: "Client"}, {Query: "   "}})
	top, ticks, active := m.Activity(2)
	if ticks != 5 || active != 10 || len(top) != 2 {
		t.Fatalf("ticks %d, ativas %d, linhas %d", ticks, active, len(top))
	}
	if top[0].Query != "SELECT payload FROM inventory_reports WHERE id = $1" || top[0].Samples != 6 || top[0].CPU != 3 || top[0].IO != 3 {
		t.Errorf("a mais vista: %+v", top[0])
	}
	if top[1].Samples != 3 || top[1].Lock != 3 {
		t.Errorf("a segunda: %+v", top[1])
	}
	all, _, _ := m.Activity(10)
	if n := len([]rune(all[2].Query)); n != maxQueryText+1 || all[2].Client != 1 {
		t.Errorf("texto longo: %d caracteres, %+v", n, all[2])
	}
	// O número de consultas guardadas tem teto: o que passar vai para "(outras)".
	var cheio activityLog
	for i := range maxQueries + 50 {
		cheio.add([]Activity{{Query: fmt.Sprintf("SELECT %d", i)}})
	}
	if len(cheio.queries) != maxQueries+1 || cheio.queries["(outras)"].Samples != 50 {
		t.Errorf("teto: %d consultas", len(cheio.queries))
	}
}

// O monitor amostra sem travar o laço: uma leitura por vez.
func TestAmostraDoBanco(t *testing.T) {
	b := &bancoAtivo{lista: []Activity{{Query: "SELECT 1"}}}
	m := New(b, nil, nil)
	m.sampleActivity(context.Background())
	for i := 0; i < 200; i++ {
		if _, ticks, _ := m.Activity(1); ticks == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if top, ticks, _ := m.Activity(1); ticks != 1 || len(top) != 1 || top[0].Query != "SELECT 1" {
		t.Errorf("amostra: %d %+v", ticks, top)
	}
	// Um banco que não sabe (o de memória): nada acontece.
	New(&fakeKeeper{}, nil, nil).sampleActivity(context.Background())
}
