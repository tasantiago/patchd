package compliance

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/catalog/kev"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/version"
)

// Advisory é um aviso de segurança da distribuição que corrige um pacote fonte: uma USN
// (Ubuntu) ou um update do Bodhi (Fedora).
type Advisory struct {
	ID        string    // USN-8861-1, FEDORA-2026-35b989661c
	Source    string    // pacote fonte
	Fixed     string    // versão da correção, no esquema da distribuição (rpm: época:versão-release)
	Published time.Time // Ubuntu: publicação da USN; Fedora: entrada em stable
	Severity  string    // Ubuntu: maior prioridade entre as CVEs; Fedora: severidade do update
	CVEs      []string
	KEV       []string // CVEs do aviso que estão no KEV (confirmado)
	Inferred  []string // Fedora: CVEs do KEV ligadas ao update por inferência (kev.FedoraRules)
	Partial   bool     // Fedora: a lista de CVEs veio cortada
}

// Finding é um pacote fonte instalado abaixo de pelo menos uma correção.
type Finding struct {
	Source     string
	Installed  string     // versão do pacote fonte instalada
	Binaries   []string   // binários instalados dessa fonte e versão
	Target     string     // a maior versão de correção entre os avisos pendentes
	Advisories []Advisory // pendentes, do mais novo para o mais antigo
}

// Exploited diz se algum aviso pendente corrige CVE do KEV citada no próprio aviso.
func (f Finding) Exploited() bool {
	return slices.ContainsFunc(f.Advisories, func(a Advisory) bool { return len(a.KEV) > 0 })
}

// InferredExploited diz se algum aviso pendente foi ligado ao KEV por inferência.
func (f Finding) InferredExploited() bool {
	return slices.ContainsFunc(f.Advisories, func(a Advisory) bool { return len(a.Inferred) > 0 })
}

// Kernel descreve o kernel da máquina. Só a versão em execução entra na conformidade: as
// outras versões instaladas viram reboot pendente (mais novas) ou informação (mais velhas).
type Kernel struct {
	Release string   // como o SO informa: 7.0.0-34-generic, 7.2.7-200.fc44.x86_64
	Source  string   // pacote fonte do kernel em execução; vazio se não identificado
	Version string   // versão do pacote fonte em execução
	Newer   []string // versões instaladas mais novas que a em execução: reboot pendente
	Older   []string // versões mais velhas ainda instaladas
}

// LinuxResult é a avaliação de uma máquina Linux contra o catálogo.
type LinuxResult struct {
	Scheme   version.Scheme
	Sources  int // pacotes fonte avaliados
	Kernel   Kernel
	Findings []Finding
	Invalid  []string // versões que não puderam ser comparadas (não entram na conta)
}

// group é uma versão instalada de um pacote fonte, com os binários dela.
type group struct {
	source, version string
	binaries        []string
}

// archSuffix: o sufixo de arquitetura no fim do kernel do Fedora (7.2.7-200.fc44.x86_64).
var archSuffix = regexp.MustCompile(`\.(x86_64|aarch64|i686|ppc64le|s390x|armv7hl)$`)

// EvaluateLinux compara os pacotes fonte instalados com os avisos da distribuição.
//
// Regras (Aula 6.4):
//   - a unidade é o pacote fonte; a versão instalada é a do pacote fonte de cada binário
//     (no rpm, com a época do binário);
//   - um aviso está pendente se a versão instalada é menor que a da correção, pelas regras
//     do esquema (dpkg ou rpm), nunca por ordem de texto;
//   - kernel: só a versão em execução é avaliada. No Ubuntu, a fonte do kernel em execução
//     é a do binário linux-modules-<kernel> (as USNs citam "linux", não "linux-signed");
//     no Fedora, é "kernel" na versão-release do kernel em execução.
func EvaluateLinux(scheme version.Scheme, sw []protocol.Software, kernelRelease string, advisories []Advisory) LinuxResult {
	res := LinuxResult{Scheme: scheme, Kernel: Kernel{Release: kernelRelease}}

	groups := map[[2]string]*group{}
	var order [][2]string
	for _, s := range sw {
		src, ver := installedSource(scheme, s)
		if src == "" || ver == "" {
			continue
		}
		k := [2]string{src, ver}
		g, ok := groups[k]
		if !ok {
			g = &group{source: src, version: ver}
			groups[k] = g
			order = append(order, k)
		}
		g.binaries = append(g.binaries, s.Name)
	}

	// Kernel em execução.
	res.Kernel.Source, res.Kernel.Version = runningKernel(scheme, sw, kernelRelease)
	if res.Kernel.Source != "" {
		for _, k := range order {
			if k[0] != res.Kernel.Source || k[1] == res.Kernel.Version {
				continue
			}
			c, err := version.Compare(scheme, k[1], res.Kernel.Version)
			switch {
			case err != nil:
				res.Invalid = append(res.Invalid, k[0]+" "+k[1])
			case c > 0:
				res.Kernel.Newer = append(res.Kernel.Newer, k[1])
			default:
				res.Kernel.Older = append(res.Kernel.Older, k[1])
			}
			delete(groups, k) // fora da conformidade
		}
	}

	bySource := map[string][]Advisory{}
	for _, a := range advisories {
		bySource[a.Source] = append(bySource[a.Source], a)
	}
	sources := map[string]bool{}
	for _, k := range order {
		g, ok := groups[k]
		if !ok {
			continue
		}
		sources[g.source] = true
		f := Finding{Source: g.source, Installed: g.version, Binaries: g.binaries}
		for _, a := range bySource[g.source] {
			c, err := version.Compare(scheme, g.version, a.Fixed)
			if err != nil {
				res.Invalid = append(res.Invalid, fmt.Sprintf("%s %s × %s %s", g.source, g.version, a.ID, a.Fixed))
				continue
			}
			if c >= 0 {
				continue
			}
			f.Advisories = append(f.Advisories, a)
			if f.Target == "" {
				f.Target = a.Fixed
			} else if t, err := version.Compare(scheme, a.Fixed, f.Target); err == nil && t > 0 {
				f.Target = a.Fixed
			}
		}
		if len(f.Advisories) == 0 {
			continue
		}
		slices.SortFunc(f.Advisories, func(a, b Advisory) int {
			return cmp.Or(b.Published.Compare(a.Published), strings.Compare(a.ID, b.ID))
		})
		res.Findings = append(res.Findings, f)
	}
	res.Sources = len(sources)

	// Explorada confirmada primeiro, depois inferida, depois por quantidade de avisos.
	rank := func(f Finding) int {
		switch {
		case f.Exploited():
			return 0
		case f.InferredExploited():
			return 1
		}
		return 2
	}
	slices.SortStableFunc(res.Findings, func(a, b Finding) int {
		return cmp.Or(cmp.Compare(rank(a), rank(b)), cmp.Compare(len(b.Advisories), len(a.Advisories)), strings.Compare(a.Source, b.Source))
	})
	return res
}

// installedSource devolve o pacote fonte e a versão dele para um binário instalado.
func installedSource(scheme version.Scheme, s protocol.Software) (string, string) {
	if s.SourcePackage == "" || s.SourcePackageVersion == "" {
		return "", ""
	}
	v := s.SourcePackageVersion
	if scheme == version.RPM && !strings.Contains(v, ":") {
		// O SOURCERPM não traz a época; ela vem da versão do binário ("1:3.5.7-2.fc44").
		epoch := "0"
		if e, _, ok := strings.Cut(s.Version, ":"); ok && e != "" {
			epoch = e
		}
		v = epoch + ":" + v
	}
	return s.SourcePackage, v
}

// runningKernel identifica a fonte e a versão do kernel em execução no inventário.
func runningKernel(scheme version.Scheme, sw []protocol.Software, release string) (string, string) {
	if release == "" {
		return "", ""
	}
	switch scheme {
	case version.Dpkg:
		for _, s := range sw {
			if s.Name == "linux-modules-"+release {
				return installedSource(scheme, s)
			}
		}
	case version.RPM:
		vr := archSuffix.ReplaceAllString(release, "")
		for _, s := range sw {
			if s.SourcePackage == "kernel" && s.SourcePackageVersion == vr {
				return installedSource(scheme, s)
			}
		}
	}
	return "", ""
}

// InferFedoraKEV liga CVEs do KEV aos updates do Fedora que não as citam, pela tabela
// kev.FedoraRules: para cada CVE do KEV de um produto mapeado para o pacote, o primeiro
// update de segurança estável do pacote a partir da inclusão no KEV recebe a CVE como
// inferida. Ficam de fora:
//   - a CVE citada diretamente por algum update da versão (é confirmada, não inferida);
//   - a CVE antiga recém-incluída no KEV (dois anos ou mais entre o ano da CVE e a
//     inclusão): a correção quase certamente já está em qualquer versão em suporte
//     (Aula 6.3: CVE-2022-0995 do kernel, incluída em 2026).
func InferFedoraKEV(advisories []Advisory, kevVulns []kev.Vuln) {
	cited := map[string]bool{}
	byPkg := map[string][]int{}
	for i, a := range advisories {
		for _, c := range a.KEV {
			cited[c] = true
		}
		byPkg[a.Source] = append(byPkg[a.Source], i)
	}
	for _, idx := range byPkg {
		slices.SortFunc(idx, func(x, y int) int {
			return cmp.Or(advisories[x].Published.Compare(advisories[y].Published), strings.Compare(advisories[x].ID, advisories[y].ID))
		})
	}
	for _, v := range kevVulns {
		if cited[v.CVE] || OldForKEV(v.CVE, v.Added) {
			continue
		}
		for _, pkg := range kev.FedoraPackages(v.Vendor, v.Product) {
			for _, i := range byPkg[pkg] {
				if !advisories[i].Published.Before(v.Added) {
					if !slices.Contains(advisories[i].Inferred, v.CVE) {
						advisories[i].Inferred = append(advisories[i].Inferred, v.CVE)
					}
					break
				}
			}
		}
	}
}

// OldForKEV diz se a CVE tem dois anos ou mais quando entrou no KEV.
func OldForKEV(cve string, added time.Time) bool {
	parts := strings.SplitN(cve, "-", 3)
	if len(parts) != 3 {
		return false
	}
	year, err := strconv.Atoi(parts[1])
	return err == nil && added.Year()-year >= 2
}

// UbuntuEcosystem escolhe o ecossistema do OSV para a versão do Ubuntu, entre os que o
// catálogo conhece: "Ubuntu:26.04:LTS" para as LTS, "Ubuntu:25.10" para as intermediárias.
// O Ubuntu Pro (ESM) fica de fora: exige saber se a máquina tem a assinatura.
func UbuntuEcosystem(osVersion string, known []string) (string, bool) {
	for _, c := range []string{"Ubuntu:" + osVersion + ":LTS", "Ubuntu:" + osVersion} {
		if slices.Contains(known, c) {
			return c, true
		}
	}
	return "", false
}

// FedoraRelease devolve o nome da versão no Bodhi ("44" vira "F44"), se o catálogo a conhece.
func FedoraRelease(osVersion string, known []string) (string, bool) {
	r := "F" + osVersion
	return r, slices.Contains(known, r)
}
