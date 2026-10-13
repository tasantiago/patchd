package api

import (
	"net/url"
	"testing"
)

func TestBatchSel(t *testing.T) {
	for _, c := range []struct{ id, fix string }{{"curl", "8.18.0-1ubuntu2.10"}, {"CVE-2026-1001", "26200.7019 (KB5070773)"}, {"x&y=z", ""}} {
		id, fix, ok := parseBatchSel(batchSel(c.id, c.fix))
		if !ok || id != c.id || fix != c.fix {
			t.Errorf("%q %q: %q %q %t", c.id, c.fix, id, fix, ok)
		}
	}
	for _, v := range []string{"", "%%%", "id=curl", "fix=1", "id=&fix=1", "id=a&id=b&fix=1"} {
		if _, _, ok := parseBatchSel(v); ok {
			t.Errorf("%q devia ser recusado", v)
		}
	}
}

func TestBatchFlash(t *testing.T) {
	for q, want := range map[string]string{
		"lote=3&ja=0&fora=0":  "3 itens aprovados em lote.",
		"lote=1&ja=1&fora=0":  "1 item aprovado em lote. 1 já estava aprovado por outra pessoa; a aprovação original ficou.",
		"lote=0&ja=2&fora=1":  "0 itens aprovados em lote. 2 já estavam aprovados por outra pessoa; a aprovação original ficou. 1 ficou de fora: não está mais aguardando.",
		"lote=-5&ja=x&fora=0": "0 itens aprovados em lote.",
		"status=aguardando":   "",
	} {
		v, _ := url.ParseQuery(q)
		if got, _ := batchFlash(v); got != want {
			t.Errorf("%s:\n got %q\nwant %q", q, got, want)
		}
	}
}
