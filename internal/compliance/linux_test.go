package compliance

import (
	"slices"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/catalog/kev"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/version"
)

func dia(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func deb(name, ver, src, srcVer string) protocol.Software {
	return protocol.Software{Name: name, Version: ver, SourcePackage: src, SourcePackageVersion: srcVer, Scope: "machine"}
}

// O inventário da VM do laboratório (Ubuntu 26.04, 04/10/2026), reduzido: os binários do
// kernel das duas versões instaladas, mais openssl, libxpm e curl.
func inventarioVM() []protocol.Software {
	return []protocol.Software{
		deb("linux-headers-7.0.0-14-generic", "7.0.0-14.14", "linux", "7.0.0-14.14"),
		deb("linux-modules-7.0.0-14-generic", "7.0.0-14.14", "linux", "7.0.0-14.14"),
		deb("linux-headers-7.0.0-34-generic", "7.0.0-34.34", "linux", "7.0.0-34.34"),
		deb("linux-modules-7.0.0-34-generic", "7.0.0-34.34", "linux", "7.0.0-34.34"),
		deb("linux-libc-dev", "7.0.0-34.34", "linux", "7.0.0-34.34"),
		deb("linux-image-7.0.0-14-generic", "7.0.0-14.14", "linux-signed", "7.0.0-14.14"),
		deb("linux-image-7.0.0-34-generic", "7.0.0-34.34", "linux-signed", "7.0.0-34.34"),
		deb("linux-image-generic-hwe-26.04", "7.0.0-34.34", "linux-meta", "7.0.0-34.34"),
		deb("libssl3t64", "3.5.5-1ubuntu3.7", "openssl", "3.5.5-1ubuntu3.7"),
		deb("openssl", "3.5.5-1ubuntu3.7", "openssl", "3.5.5-1ubuntu3.7"),
		deb("libxpm4", "1:3.5.17-1ubuntu0.26.04.2", "libxpm", "1:3.5.17-1ubuntu0.26.04.2"),
		deb("curl", "8.18.0-1ubuntu2.7", "curl", "8.18.0-1ubuntu2.7"),
		deb("libcurl4t64", "8.18.0-1ubuntu2.7", "curl", "8.18.0-1ubuntu2.7"),
		{Name: "sem-fonte", Version: "1.0", Scope: "machine"},
	}
}

func TestEvaluateUbuntu(t *testing.T) {
	avisos := []Advisory{
		{ID: "USN-8847-1", Source: "openssl", Fixed: "3.5.5-1ubuntu3.6", Published: dia(2026, 9, 29)},
		{ID: "USN-8861-1", Source: "openssl", Fixed: "3.5.5-1ubuntu3.7", Published: dia(2026, 10, 1)},
		// A época vem na versão da correção e na instalada: igual, nada pendente.
		{ID: "USN-8862-1", Source: "libxpm", Fixed: "1:3.5.17-1ubuntu0.26.04.2", Published: dia(2026, 10, 1)},
		// Corrigido na versão do kernel em execução: o 7.0.0-14 instalado não pode contar.
		{ID: "USN-8816-1", Source: "linux", Fixed: "7.0.0-34.34", Published: dia(2026, 9, 24), KEV: []string{"CVE-2025-39964"}},
		// Dois avisos novos do curl, um com CVE no KEV.
		{ID: "USN-9001-1", Source: "curl", Fixed: "8.18.0-1ubuntu2.8", Published: dia(2026, 10, 5)},
		{ID: "USN-9002-1", Source: "curl", Fixed: "8.18.0-1ubuntu2.10", Published: dia(2026, 10, 6), KEV: []string{"CVE-2026-9999"}},
	}
	r := EvaluateLinux(version.Dpkg, inventarioVM(), "7.0.0-34-generic", avisos)

	if r.Kernel.Source != "linux" || r.Kernel.Version != "7.0.0-34.34" || !slices.Equal(r.Kernel.Older, []string{"7.0.0-14.14"}) || len(r.Kernel.Newer) != 0 {
		t.Errorf("kernel: %+v", r.Kernel)
	}
	if len(r.Findings) != 1 {
		t.Fatalf("só o curl está pendente: %+v", r.Findings)
	}
	f := r.Findings[0]
	// O alvo é a maior correção pela regra do dpkg: ubuntu2.10 > ubuntu2.8 (em texto, seria o contrário).
	if f.Source != "curl" || f.Target != "8.18.0-1ubuntu2.10" || len(f.Advisories) != 2 || f.Advisories[0].ID != "USN-9002-1" ||
		!f.Exploited() || !slices.Equal(f.Binaries, []string{"curl", "libcurl4t64"}) {
		t.Errorf("curl: %+v", f)
	}
	// linux (só a versão em execução), linux-signed, linux-meta, openssl, libxpm e curl.
	if r.Sources != 6 || len(r.Invalid) != 0 {
		t.Errorf("fontes avaliadas: %d, inválidas %v", r.Sources, r.Invalid)
	}

	// Rodando o kernel velho: o novo vira reboot pendente, e o velho fica pendente da USN.
	r = EvaluateLinux(version.Dpkg, inventarioVM(), "7.0.0-14-generic", avisos)
	if r.Kernel.Version != "7.0.0-14.14" || !slices.Equal(r.Kernel.Newer, []string{"7.0.0-34.34"}) {
		t.Errorf("kernel velho em execução: %+v", r.Kernel)
	}
	var kernel *Finding
	for i := range r.Findings {
		if r.Findings[i].Source == "linux" {
			kernel = &r.Findings[i]
		}
	}
	if kernel == nil || kernel.Installed != "7.0.0-14.14" || !kernel.Exploited() || len(r.Findings) != 2 {
		t.Errorf("o kernel em execução fica pendente da USN com CVE explorada: %+v", r.Findings)
	}

	// Kernel não identificado (sem o linux-modules do kernel informado): todas as versões contam.
	r = EvaluateLinux(version.Dpkg, inventarioVM(), "6.8.0-1-generic", avisos)
	if r.Kernel.Source != "" {
		t.Errorf("kernel desconhecido: %+v", r.Kernel)
	}
	var linux []string
	for _, f := range r.Findings {
		if f.Source == "linux" {
			linux = append(linux, f.Installed)
		}
	}
	if !slices.Equal(linux, []string{"7.0.0-14.14"}) {
		t.Errorf("sem kernel identificado, o 7.0.0-14 aparece: %v", linux)
	}
}

func TestEvaluateFedora(t *testing.T) {
	rpm := func(name, ver, srcVer, src string) protocol.Software {
		return protocol.Software{Name: name, Version: ver, SourcePackage: src, SourcePackageVersion: srcVer, Scope: "machine"}
	}
	sw := []protocol.Software{
		rpm("kernel-core", "7.2.5-200.fc44", "7.2.5-200.fc44", "kernel"),
		rpm("kernel-core", "7.2.7-200.fc44", "7.2.7-200.fc44", "kernel"),
		// A época está no binário, não no SOURCERPM.
		rpm("openssl-libs", "1:3.5.8-1.fc44", "3.5.8-1.fc44", "openssl"),
		rpm("chromium", "152.0.7977.75-1.fc44", "152.0.7977.75-1.fc44", "chromium"),
	}
	avisos := []Advisory{
		{ID: "FEDORA-K", Source: "kernel", Fixed: "0:7.2.7-200.fc44", Published: dia(2026, 9, 23)},
		{ID: "FEDORA-O1", Source: "openssl", Fixed: "1:3.5.8-1.fc44", Published: dia(2026, 9, 2)},
		{ID: "FEDORA-O2", Source: "openssl", Fixed: "1:3.5.9-1.fc44", Published: dia(2026, 10, 3)},
		{ID: "FEDORA-C1", Source: "chromium", Fixed: "0:153.0.8010.36-1.fc44", Published: dia(2026, 9, 10)},
	}
	r := EvaluateLinux(version.RPM, sw, "7.2.5-200.fc44.x86_64", avisos)
	if r.Kernel.Source != "kernel" || r.Kernel.Version != "0:7.2.5-200.fc44" || !slices.Equal(r.Kernel.Newer, []string{"0:7.2.7-200.fc44"}) {
		t.Errorf("kernel: %+v", r.Kernel)
	}
	got := map[string]Finding{}
	for _, f := range r.Findings {
		got[f.Source] = f
	}
	if f := got["openssl"]; len(f.Advisories) != 1 || f.Advisories[0].ID != "FEDORA-O2" || f.Installed != "1:3.5.8-1.fc44" {
		t.Errorf("openssl (sem a época, o FEDORA-O1 pareceria pendente): %+v", f)
	}
	if f := got["kernel"]; f.Installed != "0:7.2.5-200.fc44" || f.Target != "0:7.2.7-200.fc44" {
		t.Errorf("kernel em execução pendente: %+v", f)
	}
	if _, ok := got["chromium"]; !ok {
		t.Error("chromium pendente")
	}
}

func TestInferFedoraKEV(t *testing.T) {
	avisos := []Advisory{
		{ID: "K1", Source: "kernel", Published: dia(2026, 9, 13)},
		{ID: "K2", Source: "kernel", Published: dia(2026, 9, 23)},
		{ID: "K3", Source: "kernel", Published: dia(2026, 10, 1)},
		{ID: "C1", Source: "chromium", Published: dia(2026, 9, 10), KEV: []string{"CVE-2026-85046"}},
		{ID: "C2", Source: "chromium", Published: dia(2026, 9, 20)},
	}
	vulns := []kev.Vuln{
		{CVE: "CVE-2025-39964", Vendor: "Linux", Product: "Kernel", Added: dia(2026, 9, 18)},      // → K2
		{CVE: "CVE-2022-0995", Vendor: "Linux", Product: "Kernel", Added: dia(2026, 8, 26)},       // antiga: nada
		{CVE: "CVE-2026-85046", Vendor: "Google", Product: "Chromium V8", Added: dia(2026, 9, 4)}, // citada no C1: nada
		{CVE: "CVE-2026-87491", Vendor: "Google", Product: "Chromium V8", Added: dia(2026, 9, 9)}, // → C1
		{CVE: "CVE-2026-88779", Vendor: "Citrix", Product: "NetScaler", Added: dia(2026, 10, 4)},  // sem pacote
	}
	InferFedoraKEV(avisos, vulns)
	quer := map[string][]string{"K2": {"CVE-2025-39964"}, "C1": {"CVE-2026-87491"}}
	for _, a := range avisos {
		if !slices.Equal(a.Inferred, quer[a.ID]) {
			t.Errorf("%s: inferidas %v, esperado %v", a.ID, a.Inferred, quer[a.ID])
		}
	}
	if !OldForKEV("CVE-2022-0995", dia(2026, 8, 26)) || OldForKEV("CVE-2025-39964", dia(2026, 9, 18)) || OldForKEV("X", dia(2026, 1, 1)) {
		t.Error("regra da CVE antiga")
	}
}

func TestEcossistemas(t *testing.T) {
	conhecidos := []string{"Ubuntu:24.04:LTS", "Ubuntu:26.04:LTS", "Ubuntu:25.10", "Ubuntu:Pro:20.04:LTS"}
	for v, quer := range map[string]string{"26.04": "Ubuntu:26.04:LTS", "25.10": "Ubuntu:25.10", "20.04": "", "22.04": ""} {
		got, ok := UbuntuEcosystem(v, conhecidos)
		if got != quer || ok != (quer != "") {
			t.Errorf("%s: %q %t", v, got, ok)
		}
	}
	if r, ok := FedoraRelease("44", []string{"F43", "F44"}); r != "F44" || !ok {
		t.Errorf("F44: %s %t", r, ok)
	}
	if _, ok := FedoraRelease("42", []string{"F43", "F44"}); ok {
		t.Error("F42 fora do catálogo")
	}
}
