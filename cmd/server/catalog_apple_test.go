package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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
	majors    []store.AppleMajor
}

func (b *bancoAppleFalso) AppleMajors(context.Context) ([]store.AppleMajor, error) {
	return b.majors, nil
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
		}, Models: []apple.Model{{ID: "Mac14,14", Majors: []int{27, 26}}}},
	}
	banco := &bancoAppleFalso{hashes: map[string]string{}}
	var out bytes.Buffer

	mudou, falhas, err := syncApple(ctx, fonte, banco, &out)
	if err != nil || mudou != 2 || falhas != 0 || banco.gravacoes != 2 {
		t.Fatalf("primeira: %d %d %v\n%s", mudou, falhas, err, out.String())
	}
	for _, trecho := range []string{"gdmf: 1 versões publicadas do macOS (1 melhorias em segundo plano), atualizado",
		"sofa: 2 majors, 2 versões de segurança, 3 CVEs (1 exploradas distintas), 1 modelos de Mac, atualizado"} {
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

func TestSyncAppleMajorQueSome(t *testing.T) {
	ctx := context.Background()
	agora := time.Now()
	fonte := &appleFalso{
		versoes:  []apple.GDMFVersion{{ProductVersion: "26.7.1", Build: "25G241"}},
		hashGDMF: "g1",
		feed: apple.SOFAFeed{UpdateHash: "s2", Releases: []apple.Release{
			{MajorNumber: "27", ProductVersion: "27.0.1"}, {MajorNumber: "26", ProductVersion: "26.7.1"}, {MajorNumber: "15", ProductVersion: "15.8.1"},
		}},
	}
	// O Sonoma (última versão há dois meses) sumiu do feed: recusado, nada é gravado.
	banco := &bancoAppleFalso{hashes: map[string]string{store.FeedAppleSOFA: "s1"}, majors: []store.AppleMajor{
		{Major: "Tahoe 26", Number: "26", Date: agora.AddDate(0, 0, -9)},
		{Major: "Sonoma 14", Number: "14", Date: agora.AddDate(0, -2, 0)},
	}}
	var out bytes.Buffer
	_, falhas, err := syncApple(ctx, fonte, banco, &out)
	if err != nil || falhas != 1 || banco.hashes[store.FeedAppleSOFA] != "s1" ||
		!strings.Contains(out.String(), "sofa RECUSADO: a major 14 (Sonoma 14, última versão em") {
		t.Errorf("major recente que some: %d %v\n%s", falhas, err, out.String())
	}

	// O Monterey (última versão há dois anos) sai com aviso, e o feed é gravado.
	banco.majors = []store.AppleMajor{{Major: "Monterey 12", Number: "12", Date: agora.AddDate(-2, 0, 0)}}
	out = bytes.Buffer{}
	_, falhas, err = syncApple(ctx, fonte, banco, &out)
	if err != nil || falhas != 0 || banco.hashes[store.FeedAppleSOFA] != "s2" ||
		!strings.Contains(out.String(), "a major 12 (Monterey 12, última versão em") || !strings.Contains(out.String(), "sai do catálogo") {
		t.Errorf("major antiga que some: %d %v\n%s", falhas, err, out.String())
	}
}
