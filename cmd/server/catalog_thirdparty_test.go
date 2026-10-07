package main

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/thirdparty"
)

type fontesTerceirosFalsas struct {
	versoes map[string]string
	pedidos map[string]int
}

func (f *fontesTerceirosFalsas) Latest(_ context.Context, s thirdparty.Source) (string, error) {
	f.pedidos[s.String()]++
	if v, ok := f.versoes[s.String()]; ok {
		return v, nil
	}
	return "", errors.New("fora do ar")
}

type bancoTerceirosFalso struct {
	gravadas map[string]string
	mantidas []string
}

func (b *bancoTerceirosFalso) SaveThirdPartyVersion(_ context.Context, app, osID, version, source string) error {
	b.gravadas[app+"/"+osID] = version + " " + source
	return nil
}

func (b *bancoTerceirosFalso) PruneThirdPartyVersions(_ context.Context, keep []string) (int64, error) {
	b.mantidas = keep
	return 2, nil
}

func TestSyncThirdParty(t *testing.T) {
	m, err := thirdparty.Default()
	if err != nil {
		t.Fatal(err)
	}
	fontes := &fontesTerceirosFalsas{pedidos: map[string]int{}, versoes: map[string]string{
		"chrome:win64":                   "155.0.8059.40",
		"chrome:mac":                     "155.0.8059.40",
		"firefox:LATEST_FIREFOX_VERSION": "157.0.1",
		"firefox:FIREFOX_ESR":            "140.17.0",
		"winget:7zip.7zip":               "26.04",
	}}
	banco := &bancoTerceirosFalso{gravadas: map[string]string{}}
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	salvas, falhas, err := syncThirdParty(ctx, m, fontes, banco, &out)
	if err != nil {
		t.Fatal(err)
	}
	// O Firefox serve ao Windows e ao macOS com um pedido só.
	if fontes.pedidos["firefox:LATEST_FIREFOX_VERSION"] != 1 {
		t.Errorf("pedidos ao Firefox: %d", fontes.pedidos["firefox:LATEST_FIREFOX_VERSION"])
	}
	if banco.gravadas["mozilla-firefox/macos"] != "157.0.1 firefox:LATEST_FIREFOX_VERSION" || banco.gravadas["7zip/windows"] != "26.04 winget:7zip.7zip" {
		t.Errorf("gravadas: %v", banco.gravadas)
	}
	// As fontes sem resposta falham sem gravar nada; a limpeza mantém todas as regras com fonte.
	if salvas != 6 || falhas == 0 || salvas+falhas != len(banco.mantidas) {
		t.Errorf("salvas %d, falhas %d, mantidas %d", salvas, falhas, len(banco.mantidas))
	}
	if !slices.Contains(banco.mantidas, "python-3.14/windows") || slices.Contains(banco.mantidas, "microsoft-edge/windows") {
		t.Errorf("mantidas: %v", banco.mantidas)
	}
	for _, trecho := range []string{
		"google-chrome/windows: 155.0.8059.40 (chrome:win64)",
		"notepad-plus-plus/windows FALHOU (fica a versão guardada): fora do ar",
		"2 apagada(s) por sair do manifesto",
	} {
		if !strings.Contains(out.String(), trecho) {
			t.Errorf("faltou %q em:\n%s", trecho, out.String())
		}
	}
}
