package main

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/catalog/osv"
)

type osvFalso struct {
	mu        sync.Mutex
	lista     []osv.Entry
	baixados  []string
	falha     string // ID que falha ao baixar
	retirado  string // ID que volta com withdrawn
	erroLista error
}

func (f *osvFalso) ModifiedIDs(_ context.Context, prefix string) ([]osv.Entry, error) {
	if prefix != "USN-" {
		return nil, errors.New("a sincronização do Ubuntu só quer as USNs")
	}
	return f.lista, f.erroLista
}

func (f *osvFalso) Record(_ context.Context, id string) (osv.Record, error) {
	f.mu.Lock()
	f.baixados = append(f.baixados, id)
	f.mu.Unlock()
	if id == f.falha {
		return osv.Record{}, errors.New("rede caiu")
	}
	r := osv.Record{ID: id, Fixes: []osv.Fix{{Ecosystem: "Ubuntu:26.04:LTS", Package: "openssl", Version: "1.0-1"}}}
	if id == f.retirado {
		r.Withdrawn = time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	}
	return r, nil
}

type bancoUbuntuFalso struct {
	mu       sync.Mutex
	versoes  map[string]string
	retirada map[string]bool
}

func (b *bancoUbuntuFalso) UbuntuVersions(context.Context) (map[string]string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := map[string]string{}
	for k, v := range b.versoes {
		out[k] = v
	}
	return out, nil
}

func (b *bancoUbuntuFalso) SaveUbuntuUSN(_ context.Context, r osv.Record, modified string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.versoes[r.ID] = modified
	if b.retirada == nil {
		b.retirada = map[string]bool{}
	}
	b.retirada[r.ID] = !r.Withdrawn.IsZero()
	if !b.retirada[r.ID] {
		delete(b.retirada, r.ID)
	}
	return nil
}

func (b *bancoUbuntuFalso) retiradas() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.retirada)
}

func TestSyncUbuntu(t *testing.T) {
	ctx := context.Background()
	fonte := &osvFalso{lista: []osv.Entry{
		{ID: "USN-3-1", Modified: "2026-10-05T07:30:02.615241916Z"},
		{ID: "USN-2-1", Modified: "2026-10-02T12:17:59.329313578Z"},
		{ID: "USN-1-1", Modified: "2026-09-01T00:00:00Z"},
	}}
	banco := &bancoUbuntuFalso{versoes: map[string]string{}}
	var out bytes.Buffer

	// Com teto de 2: as duas mais recentes da lista; a terceira fica para depois.
	res, err := syncUbuntu(ctx, fonte, banco, 2, 3, &out)
	slices.Sort(fonte.baixados)
	if err != nil || res.Listed != 3 || res.Pending != 3 || res.Saved != 2 || strings.Join(fonte.baixados, ",") != "USN-2-1,USN-3-1" {
		t.Fatalf("com teto: %+v %v %v\n%s", res, fonte.baixados, err, out.String())
	}

	// Sem teto: só a que faltou. As outras estão em dia (o texto com nanossegundos bate).
	fonte.baixados, out = nil, bytes.Buffer{}
	res, _ = syncUbuntu(ctx, fonte, banco, 0, 3, &out)
	if res.Pending != 1 || res.Saved != 1 || strings.Join(fonte.baixados, ",") != "USN-1-1" {
		t.Errorf("o que faltou: %+v %v\n%s", res, fonte.baixados, out.String())
	}

	// Nada mudou: nada é baixado.
	fonte.baixados, out = nil, bytes.Buffer{}
	res, _ = syncUbuntu(ctx, fonte, banco, 0, 3, &out)
	if res.Pending != 0 || len(fonte.baixados) != 0 || !strings.Contains(out.String(), "0 novas ou alteradas") {
		t.Errorf("em dia: %+v %v\n%s", res, fonte.baixados, out.String())
	}

	// Duas mudaram: uma foi retirada pelo Ubuntu (gravada como retirada), a outra falha
	// (conta, não para nada, e a data guardada não avança).
	fonte.lista[0].Modified = "2026-10-06T01:00:00Z"
	fonte.lista[1].Modified = "2026-10-06T02:00:00Z"
	fonte.retirado, fonte.falha = "USN-3-1", "USN-2-1"
	fonte.baixados, out = nil, bytes.Buffer{}
	res, err = syncUbuntu(ctx, fonte, banco, 0, 2, &out)
	if err != nil || res.Withdrawn != 1 || res.Failed != 1 || res.Saved != 0 || !strings.Contains(out.String(), "USN-2-1 FALHOU: rede caiu") {
		t.Errorf("retirada e falha: %+v %v\n%s", res, err, out.String())
	}
	if banco.versoes["USN-3-1"] != "2026-10-06T01:00:00Z" || banco.retiradas() != 1 {
		t.Errorf("a retirada precisa ficar guardada, com a data nova: %v", banco.versoes)
	}
	if banco.versoes["USN-2-1"] != "2026-10-02T12:17:59.329313578Z" {
		t.Errorf("a data só avança no que foi gravado: %v", banco.versoes)
	}

	// Na execução seguinte, a retirada não volta como nova (só a que falhou é tentada).
	fonte.falha, fonte.baixados, out = "", nil, bytes.Buffer{}
	res, _ = syncUbuntu(ctx, fonte, banco, 0, 2, &out)
	if res.Pending != 1 || strings.Join(fonte.baixados, ",") != "USN-2-1" {
		t.Errorf("a retirada foi baixada de novo: %+v %v\n%s", res, fonte.baixados, out.String())
	}

	// Falha na lista: erro, nada baixado.
	fonte.erroLista, fonte.baixados = errors.New("bucket fora do ar"), nil
	if _, err := syncUbuntu(ctx, fonte, banco, 0, 2, &bytes.Buffer{}); err == nil || len(fonte.baixados) != 0 {
		t.Errorf("lista com erro: %v %v", err, fonte.baixados)
	}
}

func TestSyncUbuntuMuitasFalhasResumidas(t *testing.T) {
	fonte := &osvFalso{}
	for i := range 25 {
		fonte.lista = append(fonte.lista, osv.Entry{ID: "USN-" + strings.Repeat("9", i+1) + "-1", Modified: "2026-10-06T00:00:00Z"})
	}
	falhaTudo := &osvTudoFalha{osvFalso: fonte}
	var out bytes.Buffer
	res, err := syncUbuntu(context.Background(), falhaTudo, &bancoUbuntuFalso{versoes: map[string]string{}}, 0, 4, &out)
	if err != nil || res.Failed != 25 || strings.Count(out.String(), "FALHOU") != maxShownFailures || !strings.Contains(out.String(), "e mais 15 falhas") {
		t.Errorf("falhas resumidas: %+v %v\n%s", res, err, out.String())
	}
}

type osvTudoFalha struct{ *osvFalso }

func (f *osvTudoFalha) Record(context.Context, string) (osv.Record, error) {
	return osv.Record{}, errors.New("tempo esgotado")
}
