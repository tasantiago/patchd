package main

import (
	"bytes"
	"context"
	"errors"
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
			"287d1717-win": {SchemaVersion: 1, CollectedAt: coleta, OS: protocol.OSInfo{ID: "windows", Name: "Windows 11 Pro", Version: "26H2"}},
			"cccc-vazia":   {},
		},
		ubuntu: []compliance.Advisory{
			{ID: "USN-9002-1", Source: "curl", Fixed: "8.18.0-1ubuntu2.10", Published: coleta, KEV: []string{"CVE-2026-9999"}, CVEs: []string{"CVE-2026-9999"}, Severity: "high"},
		},
		fedora: []compliance.Advisory{
			{ID: "FEDORA-K2", Source: "kernel", Fixed: "0:7.2.7-200.fc44", Published: time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)},
		},
		kevs: []kev.Vuln{{CVE: "CVE-2025-39964", Vendor: "Linux", Product: "Kernel", Added: time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)}},
	}
	ctx := context.Background()
	agora := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

	ev, err := evaluateMachine(ctx, banco, "7aa6")
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
	ev, err = evaluateMachine(ctx, banco, "aaaa")
	out = bytes.Buffer{}
	printEval(ev, false, agora, &out)
	if err != nil || ev.Catalog != "F44" || !strings.Contains(out.String(), "1 com exploração inferida") || !strings.Contains(out.String(), "1 (FEDORA-K2)") {
		t.Errorf("Fedora: %v\n%s", err, out.String())
	}

	for prefixo, nota := range map[string]string{
		"bbbb": "Fedora 41 não está no catálogo do Bodhi",
		"287d": "a avaliação de Windows 11 Pro 26H2 entra nas próximas partes",
		"cccc": "ainda não enviou inventário",
	} {
		ev, err := evaluateMachine(ctx, banco, prefixo)
		out = bytes.Buffer{}
		printEval(ev, false, agora, &out)
		if err != nil || !strings.Contains(out.String(), nota) {
			t.Errorf("%s: %v\n%s", prefixo, err, out.String())
		}
	}
	if _, err := evaluateMachine(ctx, banco, "ffff"); !errors.Is(err, errNoMachine) {
		t.Errorf("máquina inexistente: %v", err)
	}
}
