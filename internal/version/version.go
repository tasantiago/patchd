// Package version compara versões nas regras de cada ecossistema: dpkg (Debian/Ubuntu),
// rpm (Fedora/RHEL) e numérica com pontos (build do Windows, versão do macOS).
// É usado pelo servidor, no catálogo e no cálculo de compliance.
package version

import (
	"fmt"
	"strconv"
	"strings"
)

// Scheme identifica as regras de comparação.
type Scheme string

const (
	Dpkg   Scheme = "dpkg"   // [época:]upstream[-revisão], com o til ordenando antes de tudo
	RPM    Scheme = "rpm"    // [época:]versão[-release], com til e circunflexo
	Dotted Scheme = "dotted" // números separados por pontos: "26200.9457", "26.5.2"
)

// Compare devolve -1, 0 ou 1 conforme a seja menor, igual ou maior que b nas regras do esquema.
func Compare(s Scheme, a, b string) (int, error) {
	switch s {
	case Dpkg:
		return CompareDpkg(a, b)
	case RPM:
		return CompareRPM(a, b)
	case Dotted:
		return CompareDotted(a, b)
	default:
		return 0, fmt.Errorf("esquema de versão desconhecido: %q", s)
	}
}

// CompareDotted compara números separados por pontos. Parte ausente vale zero
// ("26.5" = "26.5.0"); qualquer parte não numérica é erro ("25H2" não é comparável).
func CompareDotted(a, b string) (int, error) {
	pa, err := parseDotted(a)
	if err != nil {
		return 0, err
	}
	pb, err := parseDotted(b)
	if err != nil {
		return 0, err
	}
	for i := 0; i < max(len(pa), len(pb)); i++ {
		var x, y uint64
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			if x < y {
				return -1, nil
			}
			return 1, nil
		}
	}
	return 0, nil
}

func parseDotted(v string) ([]uint64, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, fmt.Errorf("versão vazia")
	}
	parts := strings.Split(v, ".")
	out := make([]uint64, len(parts))
	for i, p := range parts {
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("versão %q: parte %q não é numérica", v, p)
		}
		out[i] = n
	}
	return out, nil
}

// parseEpoch separa a época ("1:3.5.7-2") do resto. Sem dois-pontos, a época é zero.
func parseEpoch(v string) (int, string, error) {
	e, rest, ok := strings.Cut(v, ":")
	if !ok {
		return 0, v, nil
	}
	n, err := strconv.Atoi(e)
	if err != nil || n < 0 {
		return 0, "", fmt.Errorf("versão %q: época inválida %q", v, e)
	}
	return n, rest, nil
}

// splitLast quebra no último hífen: "3.5.7-2.fc44" vira ("3.5.7", "2.fc44").
func splitLast(v string) (string, string) {
	if k := strings.LastIndex(v, "-"); k >= 0 {
		return v[:k], v[k+1:]
	}
	return v, ""
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
func isAlpha(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
