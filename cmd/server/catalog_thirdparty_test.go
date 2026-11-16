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
	ciclos  map[string][]thirdparty.Cycle
}

func (f *fontesTerceirosFalsas) Latest(_ context.Context, s thirdparty.Source) (string, error) {
	f.pedidos[s.String()]++
	if v, ok := f.versoes[s.String()]; ok {
		return v, nil
	}
	return "", errors.New("fora do ar")
}

func (f *fontesTerceirosFalsas) Lifecycle(_ context.Context, product string) ([]thirdparty.Cycle, error) {
	f.pedidos["ciclos:"+product]++
	if f.ciclos == nil || f.ciclos[product] == nil {
		return nil, errors.New("fora do ar")
	}
	return f.ciclos[product], nil
}

type bancoTerceirosFalso struct {
	gravadas  map[string]string
	conferida map[string]string
	mantidas  []string
	ciclos    map[string][]thirdparty.Cycle
	produtos  []string
}

func (b *bancoTerceirosFalso) SaveThirdPartyCheck(_ context.Context, app, osID, version, source string) error {
	if b.conferida == nil {
		b.conferida = map[string]string{}
	}
	b.conferida[app+"/"+osID] = version + " " + source
	return nil
}

func (b *bancoTerceirosFalso) ReplaceLifecycle(_ context.Context, product string, cycles []thirdparty.Cycle) error {
	if b.ciclos == nil {
		b.ciclos = map[string][]thirdparty.Cycle{}
	}
	b.ciclos[product] = cycles
	return nil
}

func (b *bancoTerceirosFalso) PruneLifecycles(_ context.Context, keep []string) (int64, error) {
	b.produtos = keep
	return 0, nil
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
		"evergreen:7zip":                 "26.05", // o fabricante à frente do winget-pkgs
	}, ciclos: map[string][]thirdparty.Cycle{
		"python": {{Product: "python", Name: "3.14", Latest: "3.14.10"}, {Product: "python", Name: "3.13", Latest: "3.13.9"}},
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
	// Falhas: 5 fontes principais, nenhuma conferência (a do 7-Zip respondeu; as outras nem são
	// pedidas sem a principal) e 6 dos 7 produtos do endoflife.date.
	if salvas != 6 || falhas != 11 || len(banco.mantidas) != 11 {
		t.Errorf("salvas %d, falhas %d, mantidas %d", salvas, falhas, len(banco.mantidas))
	}
	if banco.conferida["7zip/windows"] != "26.05 evergreen:7zip" || len(banco.conferida) != 1 {
		t.Errorf("conferência: %v", banco.conferida)
	}
	// Os ciclos: um pedido por produto, mesmo com duas regras do Python; a limpeza mantém os sete.
	if fontes.pedidos["ciclos:python"] != 1 || len(banco.ciclos["python"]) != 2 || len(banco.produtos) != 7 ||
		!slices.Contains(banco.produtos, "dotnet") {
		t.Errorf("ciclos: %d pedidos, guardados %v, mantidos %v", fontes.pedidos["ciclos:python"], banco.ciclos, banco.produtos)
	}
	if !slices.Contains(banco.mantidas, "python-3.14/windows") || slices.Contains(banco.mantidas, "microsoft-edge/windows") {
		t.Errorf("mantidas: %v", banco.mantidas)
	}
	for _, trecho := range []string{
		"google-chrome/windows: 155.0.8059.40 (chrome:win64)",
		"notepad-plus-plus/windows FALHOU (fica a versão guardada): fora do ar",
		"2 apagada(s) por sair do manifesto",
		"7zip/windows: 26.04 (winget:7zip.7zip); conferência 26.05 (evergreen:7zip) · a fonte principal ainda não acompanhou",
		"ciclos de python (endoflife.date): 2",
		"ciclos de nodejs (endoflife.date) FALHOU (ficam os guardados): fora do ar",
		"7 produto(s) com ciclos de suporte (2 ciclos)",
	} {
		if !strings.Contains(out.String(), trecho) {
			t.Errorf("faltou %q em:\n%s", trecho, out.String())
		}
	}
}

// O script dos Macs na sincronização dos terceiros: só quando alguma regra o usa e há pasta de
// conteúdo; a falha conta como falha dos terceiros.
func TestSyncInstallomator(t *testing.T) {
	m, err := thirdparty.Default()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	pastas := []string{}
	srcs := catalogSources{Manifest: m, ContentDir: "/srv/patchd", InstallomatorVer: "10.10",
		Installomator: func(_ context.Context, dir string) (int64, bool, error) {
			pastas = append(pastas, dir)
			return 677099, false, nil
		}}
	var out bytes.Buffer
	if !syncInstallomator(ctx, srcs, &out) || len(pastas) != 1 || pastas[0] != "/srv/patchd/pacotes" ||
		!strings.Contains(out.String(), "Installomator 10.10: baixado e conferido (677099 bytes)") {
		t.Errorf("baixar: %v %s", pastas, out.String())
	}
	out.Reset()
	srcs.Installomator = func(context.Context, string) (int64, bool, error) { return 0, false, errors.New("SHA-256 diferente") }
	if syncInstallomator(ctx, srcs, &out) || !strings.Contains(out.String(), "FALHOU (fica o guardado, se houver): SHA-256 diferente") {
		t.Errorf("falha: %s", out.String())
	}
	out.Reset()
	srcs.ContentDir = ""
	if !syncInstallomator(ctx, srcs, &out) || !strings.Contains(out.String(), "sem PATCHD_CONTENT_DIR") {
		t.Errorf("sem pasta: %s", out.String())
	}
	// Sem regra do Mac com Installomator, nada é pedido.
	local, _ := thirdparty.Parse([]byte(`{"version": 1, "apps": [{"id": "x", "os": "windows", "category": "eol", "match": "x"}]}`))
	srcs = catalogSources{Manifest: local, ContentDir: "/srv", Installomator: func(context.Context, string) (int64, bool, error) {
		t.Error("pediu o script sem regra que o use")
		return 0, false, nil
	}}
	if !syncInstallomator(ctx, srcs, &out) {
		t.Error("sem regra devia passar")
	}
}
