package wucache

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Com horário para baixar da Microsoft: fora dele, o pedido fica na fila (em disco) e nada é
// baixado; o que já está no cache continua pronto; quando o horário abre, a fila é baixada, e
// a fila sobrevive a um reinício do servidor.
func TestFilaDoHorario(t *testing.T) {
	conteudo := strings.Repeat("pacote ", 2000)
	var pedidos atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pedidos.Add(1)
		_, _ = io.WriteString(w, conteudo)
	}))
	defer srv.Close()
	dir := t.TempDir()
	agora := time.Date(2026, 11, 16, 14, 0, 0, 0, time.UTC)
	abre := time.Date(2026, 11, 16, 22, 0, 0, 0, time.UTC)
	novo := func() *Cache {
		c, err := New(dir, "teste", slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			t.Fatal(err)
		}
		c.Origin = func(u *url.URL) []string { return []string{srv.URL + u.Path} }
		c.now = func() time.Time { return agora }
		c.Schedule = func(now time.Time) (bool, time.Time) { return !now.Before(abre), abre }
		return c
	}
	c := novo()
	a, b := "http://download.windowsupdate.com/c/a.cab", "http://download.windowsupdate.com/c/b.msu"
	st := c.Ensure(context.Background(), []string{a, b, "http://exemplo.com/x.cab"})
	if st[0].State != StateScheduled || !st[0].Opens.Equal(abre) || st[1].State != StateScheduled || st[2].State != StateRefused {
		t.Fatalf("fora do horário: %+v", st)
	}
	c.Ensure(context.Background(), []string{a}) // o pedido repetido não duplica
	if q := c.Queue(); len(q) != 2 || q[0].URL != a && q[1].URL != a {
		t.Fatalf("fila: %+v", q)
	}
	if n := c.Drain(); n != 0 || pedidos.Load() != 0 {
		t.Fatalf("baixou fora do horário: %d começados, %d pedidos à origem", n, pedidos.Load())
	}
	if s, _ := c.Stats(); len(s.Queued) != 2 || s.Files != 0 {
		t.Errorf("resumo: %+v", s)
	}

	// O servidor reinicia; o horário abre: a fila é baixada.
	c = novo()
	agora = abre.Add(time.Minute)
	if n := c.Drain(); n != 2 {
		t.Fatalf("horário aberto: %d começados", n)
	}
	st = esperaPronto(t, c, []string{a, b})
	if st[0].State != StateReady || st[1].State != StateReady || st[0].Size != int64(len(conteudo)) {
		t.Fatalf("depois do horário: %+v", st)
	}
	if q := c.Queue(); len(q) != 0 {
		t.Errorf("a fila não esvaziou: %+v", q)
	}
	if c.Drain() != 0 {
		t.Error("baixou de novo o que já tem")
	}

	// De volta ao expediente: o que está no cache segue pronto; um arquivo novo vai para a fila.
	agora = abre.Add(16 * time.Hour)
	abre = abre.Add(24 * time.Hour)
	novoArq := "http://download.windowsupdate.com/c/c.cab"
	st = c.Ensure(context.Background(), []string{a, novoArq})
	if st[0].State != StateReady || st[1].State != StateScheduled {
		t.Errorf("no dia seguinte: %+v", st)
	}
	// A fila não aceita endereço de fora nem chave trocada: sai sem baixar.
	_ = os.WriteFile(filepath.Join(dir, "0123456789abcdef-x.cab"+queueSuffix), []byte(`{"key":"0123456789abcdef-x.cab","url":"http://exemplo.com/x.cab"}`), 0o600)
	agora = abre.Add(time.Minute)
	antes := pedidos.Load()
	if n := c.Drain(); n != 1 {
		t.Errorf("fila com um endereço de fora: %d começados", n)
	}
	esperaPronto(t, c, []string{novoArq})
	if pedidos.Load() != antes+1 || len(c.Queue()) != 0 {
		t.Errorf("endereço de fora: %d pedidos à origem, fila %+v", pedidos.Load()-antes, c.Queue())
	}

	// A limpeza tira o pedido que ninguém repetiu em uma semana.
	agora = abre.Add(-time.Hour)
	abre = abre.Add(240 * time.Hour)
	c.Ensure(context.Background(), []string{"http://download.windowsupdate.com/c/velho.cab"})
	agora = agora.Add(8 * 24 * time.Hour)
	if _, _, err := c.Prune(agora.Add(-90 * 24 * time.Hour)); err != nil || len(c.Queue()) != 0 {
		t.Errorf("limpeza da fila: %v, %+v", err, c.Queue())
	}

	// Sem horário (o padrão), tudo como antes: baixa quando a primeira máquina pede.
	c.Schedule = nil
	if st := esperaPronto(t, c, []string{"http://download.windowsupdate.com/c/d.cab"}); st[0].State != StateReady {
		t.Errorf("sem horário: %+v", st)
	}
}
