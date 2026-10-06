package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/catalog/apple"
	"github.com/tasantiago/patchd/internal/store"
)

type appleFalso struct {
	versoes  []apple.GDMFVersion
	hashGDMF string
	feed     apple.SOFAFeed
	erroGDMF error
}

func (a *appleFalso) FetchGDMF(context.Context) ([]apple.GDMFVersion, string, error) {
	return a.versoes, a.hashGDMF, a.erroGDMF
}

func (a *appleFalso) FetchSOFA(context.Context) (apple.SOFAFeed, error) { return a.feed, nil }

type bancoAppleFalso struct {
	hashes    map[string]string
	gravacoes int
}

func (b *bancoAppleFalso) FeedHash(_ context.Context, source string) (string, error) {
	return b.hashes[source], nil
}

func (b *bancoAppleFalso) SaveAppleVersions(_ context.Context, _ []apple.GDMFVersion, hash string) error {
	b.gravacoes++
	b.hashes[store.FeedAppleGDMF] = hash
	return nil
}

func (b *bancoAppleFalso) SaveSOFA(_ context.Context, f apple.SOFAFeed) error {
	b.gravacoes++
	b.hashes[store.FeedAppleSOFA] = f.UpdateHash
	return nil
}

func TestSyncApple(t *testing.T) {
	ctx := context.Background()
	fonte := &appleFalso{
		versoes:  []apple.GDMFVersion{{ProductVersion: "26.7.1", Build: "25G241"}, {ProductVersion: "26.3.1", Build: "25D771280a", Extra: "(a)"}},
		hashGDMF: "g1",
		feed: apple.SOFAFeed{UpdateHash: "s1", Releases: []apple.Release{
			{MajorNumber: "26", ProductVersion: "26.7.1", CVEs: []apple.CVE{{ID: "CVE-2026-86950", Exploited: true}}},
			{MajorNumber: "15", ProductVersion: "15.8.1", CVEs: []apple.CVE{{ID: "CVE-2026-86950", Exploited: true}, {ID: "CVE-2026-1"}}},
		}},
	}
	banco := &bancoAppleFalso{hashes: map[string]string{}}
	var out bytes.Buffer

	mudou, falhas, err := syncApple(ctx, fonte, banco, &out)
	if err != nil || mudou != 2 || falhas != 0 || banco.gravacoes != 2 {
		t.Fatalf("primeira: %d %d %v\n%s", mudou, falhas, err, out.String())
	}
	for _, trecho := range []string{"gdmf: 1 versões publicadas do macOS (1 melhorias em segundo plano), atualizado",
		"sofa: 2 majors, 2 versões de segurança, 3 CVEs (1 exploradas distintas), atualizado"} {
		if !strings.Contains(out.String(), trecho) {
			t.Errorf("faltou %q em:\n%s", trecho, out.String())
		}
	}

	// Nada mudou: nada é gravado.
	out = bytes.Buffer{}
	mudou, _, _ = syncApple(ctx, fonte, banco, &out)
	if mudou != 0 || banco.gravacoes != 2 || strings.Count(out.String(), "em dia") != 2 {
		t.Errorf("em dia: %d gravações\n%s", banco.gravacoes, out.String())
	}

	// gdmf fora do ar e SOFA vazio: os dois falham, nada é apagado, nada é gravado.
	fonte.erroGDMF = errors.New("x509: certificate signed by unknown authority")
	fonte.feed = apple.SOFAFeed{UpdateHash: "s2"}
	out = bytes.Buffer{}
	mudou, falhas, err = syncApple(ctx, fonte, banco, &out)
	if err != nil || mudou != 0 || falhas != 2 || banco.gravacoes != 2 || banco.hashes[store.FeedAppleSOFA] != "s1" {
		t.Errorf("falhas: %d %d %v\n%s", mudou, falhas, err, out.String())
	}
	if !strings.Contains(out.String(), "o catálogo atual fica") || !strings.Contains(out.String(), "unknown authority") {
		t.Errorf("mensagens: %s", out.String())
	}

	// gdmf vazio também é recusado.
	fonte.erroGDMF, fonte.versoes = nil, nil
	_, falhas, _ = syncApple(ctx, fonte, banco, &bytes.Buffer{})
	if falhas != 2 {
		t.Errorf("gdmf vazio: %d falhas", falhas)
	}
}
