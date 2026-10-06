package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/catalog/bodhi"
)

type pedidoBodhi struct {
	release string
	since   time.Time
	page    int
}

type bodhiFalso struct {
	versoes  []bodhi.Release
	updates  map[string][]bodhi.Update // por versão, todos
	porPag   int
	falhaPag map[string]int // versão → página que falha
	outra    bool           // devolve um update de outra versão
	pedidos  []pedidoBodhi
}

func (b *bodhiFalso) Releases(context.Context) ([]bodhi.Release, error) { return b.versoes, nil }

func (b *bodhiFalso) Updates(_ context.Context, release string, since time.Time, page int) (bodhi.UpdatesPage, error) {
	b.pedidos = append(b.pedidos, pedidoBodhi{release, since, page})
	if b.falhaPag[release] == page {
		return bodhi.UpdatesPage{}, errors.New("tempo esgotado")
	}
	var sel []bodhi.Update
	for _, u := range b.updates[release] {
		if since.IsZero() || !u.Pushed.Before(since) {
			sel = append(sel, u)
		}
	}
	pages := max((len(sel)+b.porPag-1)/b.porPag, 1)
	ini := min((page-1)*b.porPag, len(sel))
	fim := min(ini+b.porPag, len(sel))
	p := bodhi.UpdatesPage{Updates: sel[ini:fim], Page: page, Pages: pages, Total: len(sel)}
	if b.outra && len(p.Updates) > 0 {
		p.Updates[0].Release = "F99"
	}
	return p, nil
}

type bancoFedoraFalso struct {
	gravados map[string]bodhi.Update
	lotes    int
}

func (b *bancoFedoraFalso) FedoraLastPushed(_ context.Context, release string) (time.Time, error) {
	var last time.Time
	for _, u := range b.gravados {
		if u.Release == release && u.Pushed.After(last) {
			last = u.Pushed
		}
	}
	return last, nil
}

func (b *bancoFedoraFalso) SaveFedoraUpdates(_ context.Context, us []bodhi.Update) error {
	b.lotes++
	for _, u := range us {
		b.gravados[u.Alias] = u
	}
	return nil
}

func updatesDe(release string, n int, primeiro time.Time) []bodhi.Update {
	var us []bodhi.Update
	for i := range n {
		us = append(us, bodhi.Update{Alias: fmt.Sprintf("FEDORA-%s-%03d", release, i), Release: release, Pushed: primeiro.Add(time.Duration(i) * time.Hour)})
	}
	return us
}

func TestSyncFedora(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	fonte := &bodhiFalso{
		versoes: []bodhi.Release{{Name: "F43", IDPrefix: "FEDORA"}, {Name: "F44", IDPrefix: "FEDORA"}},
		updates: map[string][]bodhi.Update{"F43": updatesDe("F43", 5, t0), "F44": updatesDe("F44", 250, t0)},
		porPag:  100,
	}
	banco := &bancoFedoraFalso{gravados: map[string]bodhi.Update{}}
	var out bytes.Buffer

	// Primeira vez: tudo, em páginas; um lote (transação) por versão.
	n, falhas, err := syncFedora(ctx, fonte, banco, false, &out)
	if err != nil || n != 255 || falhas != 0 || banco.lotes != 2 || len(fonte.pedidos) != 1+3 {
		t.Fatalf("primeira: %d %d %v lotes=%d pedidos=%v\n%s", n, falhas, err, banco.lotes, fonte.pedidos, out.String())
	}
	if !strings.Contains(out.String(), "F44  completo: 250 update(s) em 3 página(s)") {
		t.Errorf("resumo: %s", out.String())
	}

	// Segunda vez: a partir da maior date_pushed guardada, menos a margem.
	fonte.pedidos, out = nil, bytes.Buffer{}
	novo := bodhi.Update{Alias: "FEDORA-F44-novo", Release: "F44", Pushed: t0.Add(300 * time.Hour)}
	fonte.updates["F44"] = append(fonte.updates["F44"], novo)
	n, _, _ = syncFedora(ctx, fonte, banco, false, &out)
	ultimoF44 := t0.Add(249 * time.Hour)
	var pedidoF44 pedidoBodhi
	for _, p := range fonte.pedidos {
		if p.release == "F44" {
			pedidoF44 = p
		}
	}
	if !pedidoF44.since.Equal(ultimoF44.Add(-fedoraOverlap)) {
		t.Errorf("ponto de parada: %v, esperado %v", pedidoF44.since, ultimoF44.Add(-fedoraOverlap))
	}
	if _, ok := banco.gravados["FEDORA-F44-novo"]; !ok || n >= 255 {
		t.Errorf("incremental: %d gravados, novo presente=%t\n%s", n, ok, out.String())
	}

	// -full ignora o ponto de parada.
	fonte.pedidos = nil
	_, _, _ = syncFedora(ctx, fonte, banco, true, &bytes.Buffer{})
	for _, p := range fonte.pedidos {
		if !p.since.IsZero() {
			t.Errorf("-full pediu com pushed_since: %+v", p)
		}
	}

	// Página 2 do F44 falha: nada do F44 é gravado nessa execução (o ponto de parada não
	// avança por cima do que faltou); o F43 segue.
	banco2 := &bancoFedoraFalso{gravados: map[string]bodhi.Update{}}
	fonte.falhaPag = map[string]int{"F44": 2}
	out = bytes.Buffer{}
	n, falhas, err = syncFedora(ctx, fonte, banco2, false, &out)
	if err != nil || falhas != 1 || n != 5 || banco2.lotes != 1 || !strings.Contains(out.String(), "F44  FALHOU (nada gravado desta versão): página 2: tempo esgotado") {
		t.Errorf("falha no meio: %d %d %v\n%s", n, falhas, err, out.String())
	}
	if last, _ := banco2.FedoraLastPushed(ctx, "F44"); !last.IsZero() {
		t.Errorf("o F44 não podia ter nada gravado: %v", last)
	}

	// Update de outra versão na resposta: a versão inteira é recusada.
	fonte.falhaPag, fonte.outra = nil, true
	out = bytes.Buffer{}
	_, falhas, _ = syncFedora(ctx, fonte, &bancoFedoraFalso{gravados: map[string]bodhi.Update{}}, false, &out)
	if falhas != 2 || !strings.Contains(out.String(), "veio o update") {
		t.Errorf("versão trocada: %d\n%s", falhas, out.String())
	}

	// Nenhuma versão listada é erro (o Bodhi mudou ou a resposta veio vazia).
	if _, _, err := syncFedora(ctx, &bodhiFalso{}, banco, false, &bytes.Buffer{}); err == nil {
		t.Error("sem versões deveria ser erro")
	}
}
