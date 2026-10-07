package main

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/catalog/kev"
	"github.com/tasantiago/patchd/internal/compliance"
	"github.com/tasantiago/patchd/internal/protocol"
)

type bancoComplianceFalso struct {
	inv      map[string]protocol.InventoryReport
	ubuntu   []compliance.Advisory
	fedora   []compliance.Advisory
	kevs     []kev.Vuln
	pedidoEc string
	produtos []compliance.MSRCProduct
	windows  []compliance.WindowsFix
	pedidoW  []string
}

func (b *bancoComplianceFalso) ResolveMachine(_ context.Context, prefix string) (string, bool, error) {
	var achou []string
	for id := range b.inv {
		if strings.HasPrefix(id, prefix) {
			achou = append(achou, id)
		}
	}
	switch len(achou) {
	case 0:
		return "", false, nil
	case 1:
		return achou[0], true, nil
	}
	return "", false, errors.New("ambíguo")
}

func (b *bancoComplianceFalso) LatestInventory(_ context.Context, id string) (protocol.InventoryReport, bool, error) {
	r, ok := b.inv[id]
	return r, ok && r.SchemaVersion > 0, nil
}

func (b *bancoComplianceFalso) UbuntuEcosystemsKnown(context.Context) ([]string, error) {
	return []string{"Ubuntu:24.04:LTS", "Ubuntu:26.04:LTS"}, nil
}

func (b *bancoComplianceFalso) UbuntuAdvisories(_ context.Context, eco string, _ []string) ([]compliance.Advisory, error) {
	b.pedidoEc = eco
	return b.ubuntu, nil
}

func (b *bancoComplianceFalso) FedoraReleasesKnown(context.Context) ([]string, error) {
	return []string{"F43", "F44"}, nil
}

func (b *bancoComplianceFalso) FedoraAdvisories(context.Context, string, []string) ([]compliance.Advisory, error) {
	return b.fedora, nil
}

func (b *bancoComplianceFalso) KEVEntries(context.Context) ([]kev.Vuln, error) { return b.kevs, nil }

func (b *bancoComplianceFalso) WindowsProducts(context.Context) ([]compliance.MSRCProduct, error) {
	return b.produtos, nil
}

func (b *bancoComplianceFalso) WindowsFixes(_ context.Context, ids []string) ([]compliance.WindowsFix, error) {
	b.pedidoW = ids
	return b.windows, nil
}

func TestEvaluateMachine(t *testing.T) {
	coleta := time.Date(2026, 10, 4, 22, 45, 0, 0, time.UTC)
	sw := func(name, ver, src string) protocol.Software {
		return protocol.Software{Name: name, Version: ver, SourcePackage: src, SourcePackageVersion: ver}
	}
	banco := &bancoComplianceFalso{
		inv: map[string]protocol.InventoryReport{
			"7aa604f7-185d": {SchemaVersion: 1, CollectedAt: coleta,
				OS: protocol.OSInfo{ID: "ubuntu", Name: "Ubuntu", Version: "26.04", Kernel: "7.0.0-34-generic"},
				Software: []protocol.Software{
					sw("linux-modules-7.0.0-34-generic", "7.0.0-34.34", "linux"),
					sw("linux-modules-7.0.0-36-generic", "7.0.0-36.36", "linux"),
					sw("curl", "8.18.0-1ubuntu2.7", "curl"),
				}},
			"aaaa-fedora": {SchemaVersion: 1, CollectedAt: coleta,
				OS: protocol.OSInfo{ID: "fedora", Name: "Fedora Linux", Version: "44", Kernel: "7.2.5-200.fc44.x86_64"},
				Software: []protocol.Software{
					{Name: "kernel-core", Version: "7.2.5-200.fc44", SourcePackage: "kernel", SourcePackageVersion: "7.2.5-200.fc44"},
				}},
			"bbbb-velho":   {SchemaVersion: 1, CollectedAt: coleta, OS: protocol.OSInfo{ID: "fedora", Name: "Fedora Linux", Version: "41"}},
			"287d1717-win": {SchemaVersion: 1, CollectedAt: coleta, OS: protocol.OSInfo{ID: "windows", Name: "Windows 11 Pro", Version: "26H2", Build: "26300.9457", Arch: "amd64", Edition: "Professional"}},
			"cccc-vazia":   {},
		},
		ubuntu: []compliance.Advisory{
			{ID: "USN-9002-1", Source: "curl", Fixed: "8.18.0-1ubuntu2.10", Published: coleta, KEV: []string{"CVE-2026-9999"}, CVEs: []string{"CVE-2026-9999"}, Severity: "high"},
		},
		fedora: []compliance.Advisory{
			{ID: "FEDORA-K2", Source: "kernel", Fixed: "0:7.2.7-200.fc44", Published: time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)},
		},
		kevs: []kev.Vuln{{CVE: "CVE-2025-39964", Vendor: "Linux", Product: "Kernel", Added: time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)}},
		produtos: []compliance.MSRCProduct{
			{ID: "12390", Name: "Windows 11 Version 24H2 for x64-based Systems"},
			{ID: "20438", Name: "Windows 11 Version 25H2 for x64-based Systems"},
		},
	}
	ctx := context.Background()
	agora := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

	ev, err := evaluateMachine(ctx, banco, "7aa6", evalOptions{})
	if err != nil || ev.Catalog != "Ubuntu:26.04:LTS" || banco.pedidoEc != "Ubuntu:26.04:LTS" {
		t.Fatalf("Ubuntu: %+v %v", ev, err)
	}
	var out bytes.Buffer
	printEval(ev, true, agora, &out)
	for _, trecho := range []string{
		"Máquina 7aa604f7-185d (Ubuntu 26.04)",
		"ATENÇÃO: há 2 dias",
		"Kernel em execução: 7.0.0-34-generic (fonte linux 7.0.0-34.34)",
		"REBOOT PENDENTE: kernel mais novo instalado e não carregado: 7.0.0-36.36",
		"2 pacotes fonte avaliados; 1 com correção pendente (1 com CVE explorada no KEV, 0 com exploração inferida)",
		"KEV        curl",
		"USN-9002-1",
		"KEV: CVE-2026-9999",
	} {
		if !strings.Contains(out.String(), trecho) {
			t.Errorf("faltou %q em:\n%s", trecho, out.String())
		}
	}

	// Fedora: o kernel em execução (7.2.5) fica pendente do update que a inferência liga ao KEV.
	ev, err = evaluateMachine(ctx, banco, "aaaa", evalOptions{})
	out = bytes.Buffer{}
	printEval(ev, false, agora, &out)
	if err != nil || ev.Catalog != "F44" || !strings.Contains(out.String(), "1 com exploração inferida") || !strings.Contains(out.String(), "1 (FEDORA-K2)") {
		t.Errorf("Fedora: %v\n%s", err, out.String())
	}

	for prefixo, nota := range map[string]string{
		"bbbb": "Fedora 41 não está no catálogo do Bodhi",
		"287d": "Windows 11 26H2 (amd64) não está no catálogo do MSRC; versões no catálogo: 24H2, 25H2. Se for uma versão nova da mesma linha de atualizações, avalie com -como VERSÃO",
		"cccc": "ainda não enviou inventário",
	} {
		ev, err := evaluateMachine(ctx, banco, prefixo, evalOptions{})
		out = bytes.Buffer{}
		printEval(ev, false, agora, &out)
		if err != nil || !strings.Contains(out.String(), nota) {
			t.Errorf("%s: %v\n%s", prefixo, err, out.String())
		}
	}
	if _, err := evaluateMachine(ctx, banco, "ffff", evalOptions{}); !errors.Is(err, errNoMachine) {
		t.Errorf("máquina inexistente: %v", err)
	}
	if _, err := evaluateMachine(ctx, banco, "7aa6", evalOptions{Assume: "25H2"}); err == nil || !strings.Contains(err.Error(), "só valem para máquinas Windows") {
		t.Errorf("-como no Ubuntu: %v", err)
	}
}

func TestEvaluateMachineWindows(t *testing.T) {
	coleta := time.Date(2026, 10, 4, 22, 45, 0, 0, time.UTC)
	agora := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	win := func(version, build, edition string) protocol.InventoryReport {
		return protocol.InventoryReport{SchemaVersion: 1, CollectedAt: coleta,
			OS: protocol.OSInfo{ID: "windows", Name: "Windows 11 Pro", Version: version, Build: build, Arch: "amd64", Edition: edition}}
	}
	set := time.Date(2026, 9, 8, 7, 0, 0, 0, time.UTC)
	ago := time.Date(2026, 8, 11, 7, 0, 0, 0, time.UTC)
	banco := &bancoComplianceFalso{
		inv: map[string]protocol.InventoryReport{
			"287d1717-win": win("26H2", "26300.9457", "Professional"),
			"aaaa-25h2":    win("25H2", "26200.9350", "Professional"),
			"bbbb-insider": win("25H2", "26220.9000", "Professional"),
			"cccc-server":  win("24H2", "26100.9445", "ServerDatacenter"),
		},
		produtos: []compliance.MSRCProduct{
			{ID: "12390", Name: "Windows 11 Version 24H2 for x64-based Systems"},
			{ID: "20438", Name: "Windows 11 Version 25H2 for x64-based Systems"},
		},
		windows: []compliance.WindowsFix{
			{Document: "2026-Sep", Released: set, CVE: "CVE-2026-50500", Title: "Win32k", Severity: "Important", Score: 7.8, KEV: true, KB: "5124008", SubType: "Security Update", Build: "10.0.26200.9445"},
			{Document: "2026-Sep", Released: set, CVE: "CVE-2026-50349", Title: "AFD", Severity: "Important", Score: 7.8, KB: "5124008", SubType: "Security Update", Build: "10.0.26200.9445"},
			{Document: "2026-Aug", Released: ago, CVE: "CVE-2026-40001", Title: "Kernel", Severity: "Important", Score: 7.0, KB: "5121003", SubType: "Security Update", Build: "10.0.26200.9350"},
			{Document: "2026-Aug", Released: ago, CVE: "CVE-2026-40001", Title: "Kernel", Severity: "Important", Score: 7.0, KB: "5129195", SubType: "Security Update", Build: "10.0.26200.9457"},
			{Document: "2026-Aug", Released: ago, CVE: "CVE-2026-40003", Title: "Hotpatch", Severity: "Moderate", KB: "5129241", SubType: "SecurityHotpatchUpdate", Build: "10.0.26200.9448"},
		},
	}
	ctx := context.Background()
	roda := func(prefixo string, opts evalOptions, verbose bool) string {
		t.Helper()
		ev, err := evaluateMachine(ctx, banco, prefixo, opts)
		if err != nil {
			t.Fatalf("%s: %v", prefixo, err)
		}
		var out bytes.Buffer
		printEval(ev, verbose, agora, &out)
		return out.String()
	}
	confere := func(nome, saida string, trechos ...string) {
		t.Helper()
		for _, tr := range trechos {
			if !strings.Contains(saida, tr) {
				t.Errorf("%s: faltou %q em:\n%s", nome, tr, saida)
			}
		}
	}

	// 25H2 em agosto (.9350): as duas de setembro pendentes, KEV primeiro; a relançada é informação.
	saida := roda("aaaa", evalOptions{}, true)
	confere("25H2", saida,
		"Catálogo: Windows 11 Version 25H2 for x64-based Systems",
		"Build: 26200.9350 (UBR 9350)",
		"4 CVEs do produto no catálogo; 2 pendentes (1 exploradas, pelo MSRC ou pelo KEV)",
		"Alvo: UBR 9445 (KB5124008, build 26200.9445)",
		"1 CVE(s) corrigida(s), mas com correção relançada acima deste build",
		"1 CVE(s) sem atualização cumulativa no catálogo",
		"Correções desconsideradas: 1 de hotpatch",
		"KEV        CVE-2026-50500",
		".9350 (KB5121003), relançada em .9457 (KB5129195)",
	)
	if !slices.Equal(banco.pedidoW, []string{"20438"}) {
		t.Errorf("produto pedido: %v", banco.pedidoW)
	}
	if strings.Index(saida, "CVE-2026-50500") > strings.Index(saida, "CVE-2026-50349") {
		t.Errorf("KEV deveria vir primeiro:\n%s", saida)
	}

	// O 26H2 do laboratório com -como 25H2: aceito pela evidência do UBR.
	saida = roda("287d", evalOptions{Assume: "25H2"}, false)
	confere("26H2 -como 25H2", saida,
		"PRESUMIDO (-como 25H2): o inventário diz 26H2. O build 26300 não é do produto (builds: 26100, 26200); evidência: o UBR 9457 é o do KB5129195 neste produto.",
		"4 CVEs do produto no catálogo; 0 pendentes",
	)
	// Simulação no mesmo produto, abaixo de agosto: tudo com correção pendente, alvo no relançamento.
	saida = roda("287d", evalOptions{Assume: "25H2", Build: "26200.9300"}, false)
	confere("-build", saida,
		"SIMULAÇÃO (-build): avaliado no build 26200.9300; o inventário diz 26300.9457",
		"3 pendentes (1 exploradas",
		"Alvo: UBR 9457 (KB5129195, build 26200.9457)",
	)
	if !strings.Contains(saida, "PRESUMIDO (-como 25H2): o inventário diz 26H2.\n") || strings.Contains(saida, "evidência") {
		t.Errorf("com -build num build do produto, só a versão é presumida:\n%s", saida)
	}

	// Recusas: UBR sem evidência, build de outra linha sem -como, Server.
	banco.inv["dddd-sem-evidencia"] = win("26H2", "26300.9460", "Professional")
	for nome, caso := range map[string]struct {
		prefixo string
		opts    evalOptions
		trecho  string
	}{
		"sem evidência": {"dddd", evalOptions{Assume: "25H2"}, "-como 25H2 recusado: o build 26300 não é do produto (builds: 26100, 26200) e o UBR 9460 não aparece"},
		"insider":       {"bbbb", evalOptions{}, "o build 26220 da máquina não é de Windows 11 Version 25H2 for x64-based Systems (builds do produto: 26100, 26200)"},
		"server":        {"cccc", evalOptions{}, "Windows Server (ServerDatacenter) ainda não é avaliado"},
	} {
		confere(nome, roda(caso.prefixo, caso.opts, false), "Sem avaliação: "+caso.trecho)
	}
}
