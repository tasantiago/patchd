package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/catalog/kev"
	"github.com/tasantiago/patchd/internal/store"
)

type kevFalso struct {
	cat  kev.Catalog
	erro error
}

func (k *kevFalso) Fetch(context.Context) (kev.Catalog, error) { return k.cat, k.erro }

type bancoKEVFalso struct {
	released  string
	gravacoes int
}

func (b *bancoKEVFalso) FeedHash(_ context.Context, source string) (string, error) {
	if source != store.FeedKEV {
		return "", errors.New("fonte errada")
	}
	return b.released, nil
}

func (b *bancoKEVFalso) SaveKEV(_ context.Context, c kev.Catalog) error {
	b.gravacoes++
	b.released = c.Released
	return nil
}

func TestSyncKEV(t *testing.T) {
	ctx := context.Background()
	fonte := &kevFalso{cat: kev.Catalog{Version: "2026.10.04", Released: "2026-10-04T18:52:56.0635Z",
		Vulns: []kev.Vuln{{CVE: "CVE-2026-1111", Ransomware: true}, {CVE: "CVE-2026-2222"}}}}
	banco := &bancoKEVFalso{}
	var out bytes.Buffer

	mudou, falhou, err := syncKEV(ctx, fonte, banco, &out)
	if err != nil || !mudou || falhou || banco.gravacoes != 1 ||
		!strings.Contains(out.String(), "catálogo 2026.10.04 (2 CVEs, 1 com uso em ransomware), atualizado") {
		t.Fatalf("primeira: %t %t %v\n%s", mudou, falhou, err, out.String())
	}
	out = bytes.Buffer{}
	mudou, _, _ = syncKEV(ctx, fonte, banco, &out)
	if mudou || banco.gravacoes != 1 || !strings.Contains(out.String(), "em dia") {
		t.Errorf("em dia: %d gravações\n%s", banco.gravacoes, out.String())
	}
	// Feed recusado pela conferência de integridade: falha, e o catálogo atual fica.
	fonte.erro = errors.New("feed do KEV incompleto: count 1734, itens 1700")
	out = bytes.Buffer{}
	_, falhou, err = syncKEV(ctx, fonte, banco, &out)
	if err != nil || !falhou || banco.gravacoes != 1 || !strings.Contains(out.String(), "o catálogo atual fica") {
		t.Errorf("recusa: %t %v\n%s", falhou, err, out.String())
	}
}

func TestOndeAparece(t *testing.T) {
	r := store.KEVRecentVuln{Vuln: kev.Vuln{CVE: "CVE-2026-87491", Added: time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)},
		USNs: 2, Inferred: []store.FedoraInferred{{Release: "F44", NVR: "chromium-154.0.8037.92-1.fc44"}}, Apple: []string{"26.7.1"}}
	if got := where(r); got != "Ubuntu 2 USN(s); Fedora F44 chromium-154.0.8037.92-1.fc44 (inferido); macOS 26.7.1" {
		t.Errorf("onde: %q", got)
	}
	if got := where(store.KEVRecentVuln{}); got != "fora do catálogo" {
		t.Errorf("vazio: %q", got)
	}
}
