package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/catalog/msrc"
)

type fonteFalsa struct {
	lista    []msrc.Update
	baixados []string
	falha    string // ID que falha ao baixar
}

func (f *fonteFalsa) Updates(context.Context) ([]msrc.Update, error) { return f.lista, nil }

func (f *fonteFalsa) Document(_ context.Context, id string) (msrc.Document, error) {
	f.baixados = append(f.baixados, id)
	if id == f.falha {
		return msrc.Document{}, errors.New("rede caiu")
	}
	return msrc.Document{ID: id, Vulns: []msrc.Vuln{{CVE: "CVE-1", Exploited: true}}, Fixes: []msrc.Fix{{CVE: "CVE-1", KB: "5000000"}}}, nil
}

type bancoFalso struct{ versoes map[string]time.Time }

func (b *bancoFalso) MSRCVersions(context.Context) (map[string]time.Time, error) {
	out := map[string]time.Time{}
	for k, v := range b.versoes {
		out[k] = v
	}
	return out, nil
}

func (b *bancoFalso) SaveMSRCDocument(_ context.Context, d msrc.Document, current time.Time) error {
	b.versoes[d.ID] = current
	return nil
}

func dia(m time.Month, d int) time.Time { return time.Date(2026, m, d, 7, 0, 0, 0, time.UTC) }

func TestSyncMSRCBaixaSoONovoOuRevisado(t *testing.T) {
	fonte := &fonteFalsa{lista: []msrc.Update{
		{ID: "2026-Sep", InitialReleaseDate: dia(9, 8), CurrentReleaseDate: dia(10, 4)},
		{ID: "2025-Jan", InitialReleaseDate: time.Date(2025, 1, 14, 8, 0, 0, 0, time.UTC), CurrentReleaseDate: dia(1, 1)},
		{ID: "2026-Aug", InitialReleaseDate: dia(8, 11), CurrentReleaseDate: dia(10, 2)},
	}}
	banco := &bancoFalso{versoes: map[string]time.Time{}}
	desde := dia(1, 1) // fora da janela: 2025-Jan
	var out bytes.Buffer

	n, falhas, err := syncMSRC(context.Background(), fonte, banco, desde, &out)
	if err != nil || n != 2 || falhas != 0 || strings.Join(fonte.baixados, ",") != "2026-Aug,2026-Sep" || fonte.lista[0].ID != "2026-Sep" {
		t.Fatalf("primeira vez (em ordem, só a janela): %d %d %v %v\n%s", n, falhas, fonte.baixados, err, out.String())
	}
	if !strings.Contains(out.String(), "1 exploradas") {
		t.Errorf("resumo: %s", out.String())
	}

	// Nada mudou na lista: nada é baixado.
	fonte.baixados, out = nil, bytes.Buffer{}
	n, _, _ = syncMSRC(context.Background(), fonte, banco, desde, &out)
	if n != 0 || len(fonte.baixados) != 0 || strings.Count(out.String(), "em dia") != 2 {
		t.Errorf("sem mudanças: %d %v\n%s", n, fonte.baixados, out.String())
	}

	// Setembro revisado: só ele é baixado de novo. Agosto também mudou, mas falha; o
	// resto segue, e a falha é contada.
	for i := range fonte.lista {
		if fonte.lista[i].ID != "2025-Jan" {
			fonte.lista[i].CurrentReleaseDate = dia(10, 5)
		}
	}
	fonte.falha = "2026-Aug"
	fonte.baixados, out = nil, bytes.Buffer{}
	n, falhas, _ = syncMSRC(context.Background(), fonte, banco, desde, &out)
	if n != 1 || falhas != 1 || !strings.Contains(out.String(), "revisado") || !strings.Contains(out.String(), "FALHOU: rede caiu") {
		t.Errorf("revisão e falha: %d %d\n%s", n, falhas, out.String())
	}
	if !banco.versoes["2026-Sep"].Equal(dia(10, 5)) || !banco.versoes["2026-Aug"].Equal(dia(10, 2)) {
		t.Errorf("a data só avança no que foi gravado: %v", banco.versoes)
	}
}
