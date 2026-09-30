package inventory

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Consulta ao banco do dpkg: formato definido pelo agente, campos separados por TAB.
// db:Status-Abbrev tem três caracteres: estado desejado, estado atual e flag de erro.
const (
	dpkgQueryPath = "/usr/bin/dpkg-query"
	dpkgFormat    = "${db:Status-Abbrev}\t${Package}\t${Version}\t${Architecture}\t${source:Package}\t${source:Version}\n"
	dpkgSource    = "/var/lib/dpkg/status (dpkg-query)"
)

// Consulta ao rpmdb. Campos ausentes aparecem como "(none)".
const (
	rpmPath   = "/usr/bin/rpm"
	rpmFormat = "%{NAME}\t%{EPOCH}\t%{VERSION}\t%{RELEASE}\t%{ARCH}\t%{SOURCERPM}\t%{INSTALLTIME}\t%{VENDOR}\n"
	rpmSource = "rpmdb (rpm -qa)"
	rpmNone   = "(none)"
)

// packageManagerFor escolhe o banco de pacotes pelo ID e ID_LIKE do os-release.
// A presença do executável não decide: um Ubuntu pode ter o rpm instalado.
func packageManagerFor(id, idLike string) string {
	for _, v := range append([]string{id}, strings.Fields(idLike)...) {
		switch v {
		case "debian", "ubuntu":
			return "dpkg"
		case "fedora", "rhel", "centos":
			return "rpm"
		}
	}
	return ""
}

// parseDpkgQuery interpreta a saída do dpkg-query no formato dpkgFormat.
// Só entram pacotes instalados. Pacotes em estado quebrado viram erro: um dpkg
// nesse estado trava o apt, e nenhuma atualização passa até ser corrigido.
func parseDpkgQuery(data []byte) ([]Software, error) {
	var list []Software
	var problems []string
	var broken []string

	for i, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 6 || len(f[0]) != 3 {
			problems = append(problems, fmt.Sprintf("linha %d fora do formato: %q", i+1, line))
			continue
		}
		want, status, flag := f[0][0], f[0][1], f[0][2]

		switch {
		case status == 'H' || status == 'U' || status == 'F' || flag == 'R':
			broken = append(broken, f[1])
			continue
		case status != 'i' && status != 'W' && status != 't':
			// n (não instalado), c (só arquivos de configuração): fora do inventário.
			continue
		}

		list = append(list, Software{
			Name:                 f[1],
			Version:              f[2],
			Arch:                 f[3],
			SourcePackage:        f[4],
			SourcePackageVersion: f[5],
			Held:                 want == 'h',
			Scope:                "machine",
			Source:               dpkgSource,
		})
	}

	if len(broken) > 0 {
		problems = append(problems, fmt.Sprintf(
			"dpkg: %d pacote(s) em estado inconsistente (ex.: %s); o apt fica travado até a correção",
			len(broken), broken[0]))
	}
	return list, joinProblems("dpkg-query", problems)
}

// parseRPMQuery interpreta a saída do rpm -qa no formato rpmFormat.
// A versão sai no formato EVR do rpm ("época:versão-release"), sem época quando ela não existe.
func parseRPMQuery(data []byte) ([]Software, error) {
	var list []Software
	var problems []string

	for i, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 8 {
			problems = append(problems, fmt.Sprintf("linha %d fora do formato: %q", i+1, line))
			continue
		}
		name, epoch, version, release, arch, srpm, installTime, vendor :=
			f[0], none(f[1]), f[2], f[3], none(f[4]), none(f[5]), f[6], none(f[7])

		// Chaves GPG importadas aparecem como pseudo-pacotes; não são software.
		if name == "gpg-pubkey" {
			continue
		}

		evr := version + "-" + release
		if epoch != "" {
			evr = epoch + ":" + evr
		}
		srcName, srcVersion := parseSourceRPM(srpm)

		list = append(list, Software{
			Name:                 name,
			Version:              evr,
			Publisher:            vendor,
			InstallDate:          unixDate(installTime),
			Arch:                 arch,
			SourcePackage:        srcName,
			SourcePackageVersion: srcVersion,
			Scope:                "machine",
			Source:               rpmSource,
		})
	}
	return list, joinProblems("rpm", problems)
}

// none converte o marcador "(none)" do rpm em vazio.
func none(s string) string {
	if s == rpmNone {
		return ""
	}
	return s
}

// parseSourceRPM quebra "openssl-3.5.7-2.fc44.src.rpm" em ("openssl", "3.5.7-2.fc44").
// O nome pode ter hífens, então a quebra é da direita para a esquerda.
func parseSourceRPM(s string) (name, version string) {
	s = strings.TrimSuffix(strings.TrimSuffix(s, ".src.rpm"), ".nosrc.rpm")
	r := strings.LastIndex(s, "-")
	if r <= 0 {
		return "", ""
	}
	v := strings.LastIndex(s[:r], "-")
	if v <= 0 {
		return "", ""
	}
	return s[:v], s[v+1:]
}

// unixDate converte segundos Unix (texto) em AAAA-MM-DD, em UTC. Valor inválido vira vazio.
func unixDate(s string) string {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return ""
	}
	return time.Unix(n, 0).UTC().Format("2006-01-02")
}

// joinProblems junta as mensagens num único erro, ou nil se não houver nenhuma.
func joinProblems(tool string, problems []string) error {
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("%s: %s", tool, strings.Join(problems, "; "))
}
