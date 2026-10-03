package patch

import (
	"strings"
	"testing"
)

// Linha real do Ubuntu 26.04 do laboratório (Aula 0.3), mais casos sintéticos.
const aptSimulacao = `NOTE: This is only a simulation!
      apt-get needs root privileges for real execution.
Reading package lists...
Inst libcares2 [1.34.6-1] (1.34.6-1ubuntu0.1 Ubuntu:26.04/resolute-security [amd64])
Inst openssl [3.5.0-1ubuntu1] (3.5.0-1ubuntu1.2 Ubuntu:26.04/resolute-updates, Ubuntu:26.04/resolute-security [amd64])
Inst gnome-shell [49.0-1ubuntu1] (49.1-1ubuntu1 Ubuntu:26.04/resolute-updates [amd64])
Inst linux-image-7.0.0-40-generic (7.0.0-40.40 Ubuntu:26.04/resolute-security [amd64])
Conf libcares2 (1.34.6-1ubuntu0.1 Ubuntu:26.04/resolute-security [amd64])
`

func TestParseAptSimulation(t *testing.T) {
	list, err := parseAptSimulation([]byte(aptSimulacao))
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if len(list) != 4 {
		t.Fatalf("esperadas 4 linhas Inst (Conf e avisos fora), vieram %d", len(list))
	}
	c := list[0]
	if c.Package != "libcares2" || c.InstalledVersion != "1.34.6-1" || c.FixedVersion != "1.34.6-1ubuntu0.1" ||
		c.Arch != "amd64" || c.Classification != "security" || c.Source != SourceApt {
		t.Errorf("libcares2 errado: %+v", c)
	}
	if list[1].Classification != "security" {
		t.Errorf("origem -security entre várias deveria marcar segurança: %+v", list[1])
	}
	if list[2].Classification != "update" {
		t.Errorf("só -updates não é segurança: %+v", list[2])
	}
	if list[3].InstalledVersion != "" || list[3].FixedVersion != "7.0.0-40.40" || list[3].Classification != "security" {
		t.Errorf("pacote novo (kernel) sem versão instalada: %+v", list[3])
	}
}

func TestParseAptSimulationVaziaELinhaRuim(t *testing.T) {
	list, err := parseAptSimulation([]byte("Reading package lists...\n0 upgraded, 0 newly installed.\n"))
	if err != nil || list != nil {
		t.Errorf("simulação sem Inst: lista nula do parser e sem erro: %+v, %v", list, err)
	}
	_, err = parseAptSimulation([]byte("Inst quebrado sem parenteses\n"))
	if err == nil || !strings.Contains(err.Error(), "fora do formato") {
		t.Errorf("linha Inst ilegível deveria virar erro: %v", err)
	}
}

// Saída real do Fedora 44 do laboratório (dnf5 5.4.3).
const dnfReal = `[
  {"name":"FEDORA-2026-f26b509a1a","type":"security","severity":"Important","nevra":"curl-8.18.0-10.fc44.x86_64","buildtime":1789088419},
  {"name":"FEDORA-2026-f26b509a1a","type":"security","severity":"Important","nevra":"libcurl-8.18.0-10.fc44.x86_64","buildtime":1789088419},
  {"name":"FEDORA-2026-72d10c874d","type":"security","severity":"Important","nevra":"libevent-2.1.13-1.fc44.x86_64","buildtime":1788929228},
  {"name":"FEDORA-2026-51251b4657","type":"security","severity":"Moderate","nevra":"openssl-libs-1:3.5.8-1.fc44.x86_64","buildtime":1788312756},
  {"name":"FEDORA-2026-044e56d9ff","type":"security","severity":"Moderate","nevra":"tar-2:1.35-9.fc44.x86_64","buildtime":1788929228}
]`

func TestParseDnfAdvisoriesReal(t *testing.T) {
	list, err := parseDnfAdvisories([]byte(dnfReal))
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if len(list) != 5 {
		t.Fatalf("esperadas 5 linhas (advisory × pacote), vieram %d", len(list))
	}
	o := list[3]
	if o.ID != "FEDORA-2026-51251b4657" || o.Package != "openssl-libs" || o.FixedVersion != "1:3.5.8-1.fc44" ||
		o.Arch != "x86_64" || o.Severity != "Moderate" || o.Classification != "security" || o.Source != SourceDnf {
		t.Errorf("openssl-libs errado: %+v", o)
	}
	if list[0].ReleasedAt != "2026-09-11" {
		t.Errorf("buildtime deveria virar a data em UTC: %q", list[0].ReleasedAt)
	}
	if list[0].ID != list[1].ID || list[0].Package == list[1].Package {
		t.Error("um advisory com dois pacotes deveria gerar duas entradas com o mesmo ID")
	}
}

func TestParseDnfAdvisoriesVazio(t *testing.T) {
	for _, entrada := range []string{"", "[]", "  \n"} {
		list, err := parseDnfAdvisories([]byte(entrada))
		if err != nil || list == nil || len(list) != 0 {
			t.Errorf("entrada %q: esperado [] sem erro, veio %+v, %v", entrada, list, err)
		}
	}
	if _, err := parseDnfAdvisories([]byte("não é json")); err == nil {
		t.Error("esperado erro com JSON inválido")
	}
}

func TestParseNEVRA(t *testing.T) {
	casos := []struct{ entrada, nome, evr, arch string }{
		{"openssl-libs-1:3.5.8-1.fc44.x86_64", "openssl-libs", "1:3.5.8-1.fc44", "x86_64"},
		{"curl-8.18.0-10.fc44.x86_64", "curl", "8.18.0-10.fc44", "x86_64"},
		{"python3-dnf-plugins-core-4.9.0-1.fc44.noarch", "python3-dnf-plugins-core", "4.9.0-1.fc44", "noarch"},
	}
	for _, c := range casos {
		n, e, a, ok := parseNEVRA(c.entrada)
		if !ok || n != c.nome || e != c.evr || a != c.arch {
			t.Errorf("parseNEVRA(%q) = (%q, %q, %q, %t)", c.entrada, n, e, a, ok)
		}
	}
	if _, _, _, ok := parseNEVRA("semformato"); ok {
		t.Error("esperado falha para NEVRA inválida")
	}
}
