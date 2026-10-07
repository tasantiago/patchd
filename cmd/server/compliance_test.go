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
	"github.com/tasantiago/patchd/internal/thirdparty"
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
	majors   []compliance.MacOSMajor
	macFixes map[int][]compliance.MacOSFix
	semCorr  []compliance.MacOSCVE
	desde    time.Time
	modelos  map[string]compliance.MacModel
	extras   map[string]compliance.MacExtra
	refs     []thirdparty.Ref
}

func (b *bancoComplianceFalso) ThirdPartyVersions(context.Context) ([]thirdparty.Ref, error) {
	return b.refs, nil
}

func (b *bancoComplianceFalso) MacOSMajors(context.Context) ([]compliance.MacOSMajor, error) {
	return b.majors, nil
}

func (b *bancoComplianceFalso) MacOSFixes(_ context.Context, major int) ([]compliance.MacOSFix, error) {
	return b.macFixes[major], nil
}

func (b *bancoComplianceFalso) MacOSUnfixed(_ context.Context, _ int, since time.Time) ([]compliance.MacOSCVE, error) {
	b.desde = since
	return slices.Clone(b.semCorr), nil
}

func (b *bancoComplianceFalso) AppleModel(_ context.Context, id string) (compliance.MacModel, bool, error) {
	m, ok := b.modelos[id]
	return m, ok, nil
}

func (b *bancoComplianceFalso) AppleModelsCount(context.Context) (int, error) {
	return len(b.modelos), nil
}

func (b *bancoComplianceFalso) AppleExtraBuild(_ context.Context, build string) (compliance.MacExtra, bool, error) {
	x, ok := b.extras[build]
	return x, ok, nil
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

func TestEvaluateMachineMacOS(t *testing.T) {
	d := func(s string) time.Time { v, _ := time.Parse("2006-01-02", s); return v }
	hoje := d("2026-10-07")
	mac := func(version, build, model string) protocol.InventoryReport {
		return protocol.InventoryReport{SchemaVersion: 1, CollectedAt: hoje.Add(-2 * time.Hour),
			OS:       protocol.OSInfo{ID: "macos", Name: "macOS", Version: version, Build: build, Arch: "arm64"},
			Hardware: &protocol.Hardware{Manufacturer: "Apple", Model: model}}
	}
	banco := &bancoComplianceFalso{
		inv: map[string]protocol.InventoryReport{
			"aaaa-studio": mac("26.6.2", "25G83", "Mac14,14"),
			"bbbb-air":    mac("14.8.9", "23J631", "MacBookAir8,1"),
			"cccc-bsi":    mac("26.3.1", "25D771280a", "Mac99,9"),
		},
		majors: []compliance.MacOSMajor{
			{Number: 27, Name: "Golden Gate 27", Launched: d("2026-09-14"), Latest: "27.0.1", LatestDate: d("2026-09-28")},
			{Number: 26, Name: "Tahoe 26", Launched: d("2025-09-15"), Latest: "26.7.1", LatestDate: d("2026-09-28")},
			{Number: 15, Name: "Sequoia 15", Launched: d("2024-09-16"), Latest: "15.8.1", LatestDate: d("2026-09-28")},
			{Number: 14, Name: "Sonoma 14", Launched: d("2023-09-26"), Latest: "14.8.9", LatestDate: d("2026-08-06")},
		},
		macFixes: map[int][]compliance.MacOSFix{
			26: {
				{Version: "26.6.1", Released: d("2026-08-06"), CVE: "CVE-2026-65400", Exploited: true, KEV: true},
				{Version: "26.7", Released: d("2026-09-14"), CVE: "CVE-2026-65400", Exploited: true, KEV: true},
				{Version: "26.7", Released: d("2026-09-14"), CVE: "CVE-2026-60001", Severity: "Critical"},
				{Version: "26.7.1", Released: d("2026-09-28"), CVE: "CVE-2026-86950", Exploited: true, KEV: true},
			},
			14: {{Version: "14.8.9", Released: d("2026-08-06"), CVE: "CVE-2026-65400", Exploited: true, KEV: true}},
		},
		semCorr: []compliance.MacOSCVE{{CVE: "CVE-2026-86950", Exploited: true, KEV: true, Released: d("2026-09-28"), Elsewhere: []string{"26.7.1", "15.8.1"}}},
		modelos: map[string]compliance.MacModel{
			"Mac14,14":      {ID: "Mac14,14", Name: "Mac Studio (M2 Ultra, 2023)", Majors: []int{27, 26, 15, 14, 13}},
			"MacBookAir8,1": {ID: "MacBookAir8,1", Name: "MacBook Air (Retina, 13-inch, 2018)", Majors: []int{14, 13, 12}},
		},
		extras: map[string]compliance.MacExtra{"25D771280a": {Build: "25D771280a", Version: "26.3.1", Extra: "(a)", Prerequisite: "25D2128"}},
	}
	ctx := context.Background()
	roda := func(prefixo string, opts evalOptions, verbose bool) string {
		t.Helper()
		opts.Now = hoje
		ev, err := evaluateMachine(ctx, banco, prefixo, opts)
		if err != nil {
			t.Fatalf("%s %+v: %v", prefixo, opts, err)
		}
		var out bytes.Buffer
		printEval(ev, verbose, hoje, &out)
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

	confere("Tahoe", roda("aaaa", evalOptions{}, true),
		"Máquina aaaa-studio (macOS 26.6.2)",
		"Catálogo: macOS Tahoe 26 (SOFA): última versão de segurança 26.7.1, de 2026-09-28",
		"Suporte: com suporte (recebeu a 26.7.1 em 2026-09-28, no ciclo do macOS 27",
		"Modelo: Mac14,14 (Mac Studio (M2 Ultra, 2023)): o modelo aceita até o macOS 27 (atualizar a major é opcional",
		"3 CVEs corrigidas na major, no catálogo; 2 pendentes (1 exploradas, 1 no KEV)",
		"Alvo: 26.7.1",
		"KEV        CVE-2026-86950  -           26.7.1",
		"CVE-2026-60001  Critical    26.7",
	)

	saida := roda("bbbb", evalOptions{}, false)
	confere("Sonoma", saida,
		"Suporte: FIM DE SUPORTE: a última versão de segurança é a 14.8.9, de 2026-08-06, anterior ao lançamento do macOS 27 (2026-09-14)",
		"HARDWARE FORA DE SUPORTE: a major mais nova que o modelo aceita (macOS 14)",
		"1 CVEs corrigidas na major, no catálogo; 0 pendentes",
		"1 CVE(s) explorada(s) corrigida(s) depois de 2026-08-06 só em outras majors",
		"Exploradas sem correção nesta major:",
		"KEV        CVE-2026-86950  -           26.7.1, 15.8.1",
	)
	if !banco.desde.Equal(d("2026-08-06")) {
		t.Errorf("as sem correção devem ser buscadas depois da última do Sonoma: %v", banco.desde)
	}

	// A melhoria (a) é reconhecida pelo build; o modelo fora do SOFA é dito.
	confere("(a)", roda("cccc", evalOptions{}, false),
		"Build 25D771280a: melhoria de segurança em segundo plano 26.3.1 (a), sobre o 25D2128",
		"Modelo: o modelo Mac99,9 não está na lista do SOFA",
		"3 pendentes",
	)

	// Simulação sem máquina: o mesmo Mac Studio parado no Sonoma deve atualizar a major.
	confere("simulação", roda("", evalOptions{MacOS: "14.8.9", Model: "Mac14,14"}, false),
		"Simulação, sem máquina: macOS 14.8.9, modelo Mac14,14",
		"ATUALIZAR A MAJOR: o modelo aceita até o macOS 27, que tem suporte",
	)
	confere("simulação sem modelo", roda("", evalOptions{MacOS: "26.7.1"}, false),
		"Modelo: o inventário não informa o modelo do Mac",
		"0 pendentes",
	)
	confere("major mais nova que o catálogo", roda("", evalOptions{MacOS: "28.0"}, false),
		"Sem avaliação: o macOS 28 é mais novo que o catálogo do SOFA",
	)

	// -como e -build não valem numa máquina macOS real.
	if _, err := evaluateMachine(ctx, banco, "aaaa", evalOptions{Build: "25G83", Now: hoje}); err == nil {
		t.Error("-build numa máquina macOS deveria ser recusado")
	}
}

func TestEvaluateMachineTerceiros(t *testing.T) {
	agora := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	manifesto, err := thirdparty.Default()
	if err != nil {
		t.Fatal(err)
	}
	local, err := thirdparty.Parse([]byte(`{"version": 1, "apps": [
	  {"id": "java-8", "os": "windows", "name": "Java 8", "category": "minimum", "publisher": "^Oracle", "match": "^Java 8 Update", "min_version": "8.0.5040"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	manifesto = manifesto.Merge(local)
	sw := []protocol.Software{
		{Name: "Google Chrome", Version: "154.0.8037.93", Publisher: "Google LLC", Scope: "machine"},
		{Name: "PowerShell 7.6.6.0-x64", Version: "7.6.6.0", Publisher: "Microsoft Corporation", Scope: "machine"},
		{Name: "Python 3.14.3", Version: "3.14-64", Publisher: "Python Software Foundation", Scope: "user"},
		{Name: "Java 8 Update 503 (64-bit)", Version: "8.0.5030.1", Publisher: "Oracle Corporation", Scope: "machine"},
		{Name: "Microsoft Visual C++ 2008 Redistributable - x86 9.0.30729.6161", Version: "9.0.30729.6161", Publisher: "Microsoft Corporation", Scope: "machine"},
		{Name: "Microsoft Edge", Version: "154.0.4258.53", Publisher: "Microsoft Corporation", Scope: "machine"},
		{Name: "Mozilla Maintenance Service", Version: "156.0", Publisher: "Mozilla", Scope: "machine"},
		{Name: "Spotify", Version: "1.2.99.317.g9bd8c54d", Publisher: "Spotify AB", Scope: "user"},
	}
	banco := &bancoComplianceFalso{
		inv: map[string]protocol.InventoryReport{
			"287d1717-win": {SchemaVersion: 1, CollectedAt: agora.Add(-time.Hour), Software: sw,
				OS: protocol.OSInfo{ID: "windows", Name: "Windows 11 Pro", Version: "26H2", Build: "26300.9457", Arch: "amd64", Edition: "Professional"}},
			"7aa604f7-vm": {SchemaVersion: 1, CollectedAt: agora, Software: sw,
				OS: protocol.OSInfo{ID: "ubuntu", Name: "Ubuntu", Version: "26.04"}},
		},
		produtos: []compliance.MSRCProduct{{ID: "20438", Name: "Windows 11 Version 25H2 for x64-based Systems"}},
		refs: []thirdparty.Ref{
			{App: "google-chrome", OS: "windows", Version: "155.0.8059.40", Source: "chrome:win64", Fetched: agora.Add(-2 * time.Hour)},
			{App: "powershell-7", OS: "windows", Version: "7.6.6.0", Source: "winget:Microsoft.PowerShell", Fetched: agora.AddDate(0, 0, -10)},
		},
	}
	ev, err := evaluateMachine(context.Background(), banco, "287d", evalOptions{Now: agora, Manifest: &manifesto})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	printEval(ev, true, agora, &out)
	saida := out.String()
	for _, trecho := range []string{
		// O Windows ficou sem avaliação (26H2), mas os terceiros saem assim mesmo, depois.
		"Sem avaliação: Windows 11 26H2 (amd64) não está no catálogo do MSRC",
		"Programas de terceiros (",
		"8 no inventário, 6 identificados, 1 auxiliares, 1 sem regra",
		"1 desatualizado, 1 abaixo do mínimo, 1 fora de suporte, 1 sem referência, 1 no catálogo do SO, 1 em dia",
		"sem referência: rode",
		"DESATUALIZADO     Google Chrome",
		"155.0.8059.40",
		"chrome:win64, 2026-10-07",
		"ABAIXO DO MÍNIMO  Java 8 Update 503 (64-bit)",
		"mínimo do manifesto",
		"FORA DE SUPORTE   Microsoft Visual C++ 2008",
		"fim do suporte estendido em 2018-04-10",
		"CATÁLOGO DO SO    Microsoft Edge",
		"winget:Microsoft.PowerShell, 2026-09-27 (antiga)",
		"Sem regra no manifesto:",
		"Spotify",
	} {
		if !strings.Contains(saida, trecho) {
			t.Errorf("faltou %q em:\n%s", trecho, saida)
		}
	}
	if strings.Index(saida, "Sem avaliação") > strings.Index(saida, "Programas de terceiros") {
		t.Errorf("os terceiros deveriam vir depois da avaliação do SO:\n%s", saida)
	}

	// Ubuntu: os terceiros não se aplicam (os pacotes já vêm pela distribuição).
	ev, err = evaluateMachine(context.Background(), banco, "7aa6", evalOptions{Now: agora, Manifest: &manifesto})
	if err != nil || ev.Third != nil {
		t.Errorf("Ubuntu: %+v %v", ev.Third, err)
	}
}
