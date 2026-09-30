package version

import (
	"fmt"
	"strings"
)

// CompareDpkg compara versões do dpkg: [época:]upstream[-revisão].
// A época vence tudo; depois upstream e revisão, pelo algoritmo verrevcmp do dpkg.
// Revisão ausente equivale a "0".
func CompareDpkg(a, b string) (int, error) {
	ea, ua, ra, err := parseDpkg(a)
	if err != nil {
		return 0, err
	}
	eb, ub, rb, err := parseDpkg(b)
	if err != nil {
		return 0, err
	}
	if c := cmpInt(ea, eb); c != 0 {
		return c, nil
	}
	if c := verrevcmp(ua, ub); c != 0 {
		return c, nil
	}
	return verrevcmp(ra, rb), nil
}

func parseDpkg(v string) (epoch int, upstream, revision string, err error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, "", "", fmt.Errorf("versão dpkg vazia")
	}
	epoch, rest, err := parseEpoch(v)
	if err != nil {
		return 0, "", "", err
	}
	upstream, revision = splitLast(rest)
	if upstream == "" {
		return 0, "", "", fmt.Errorf("versão dpkg %q: sem parte upstream", v)
	}
	return epoch, upstream, revision, nil
}

// dpkgOrder é o peso de um caractere na parte não numérica, como no dpkg:
// fim e dígito = 0; letra = o próprio código; '~' = -1 (antes de tudo, até do fim);
// demais símbolos = código + 256 (depois das letras).
func dpkgOrder(c byte, ok bool) int {
	switch {
	case !ok || isDigit(c):
		return 0
	case isAlpha(c):
		return int(c)
	case c == '~':
		return -1
	default:
		return int(c) + 256
	}
}

// verrevcmp compara em blocos alternados: primeiro a parte não numérica (pelo peso de
// cada caractere), depois a numérica (como número, ignorando zeros à esquerda).
func verrevcmp(a, b string) int {
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		for (i < len(a) && !isDigit(a[i])) || (j < len(b) && !isDigit(b[j])) {
			var ca, cb byte
			if i < len(a) {
				ca = a[i]
			}
			if j < len(b) {
				cb = b[j]
			}
			oa, ob := dpkgOrder(ca, i < len(a)), dpkgOrder(cb, j < len(b))
			if oa != ob {
				return cmpInt(oa, ob)
			}
			if i < len(a) {
				i++
			}
			if j < len(b) {
				j++
			}
		}
		for i < len(a) && a[i] == '0' {
			i++
		}
		for j < len(b) && b[j] == '0' {
			j++
		}
		firstDiff := 0
		for i < len(a) && j < len(b) && isDigit(a[i]) && isDigit(b[j]) {
			if firstDiff == 0 {
				firstDiff = cmpInt(int(a[i]), int(b[j]))
			}
			i++
			j++
		}
		if i < len(a) && isDigit(a[i]) {
			return 1
		}
		if j < len(b) && isDigit(b[j]) {
			return -1
		}
		if firstDiff != 0 {
			return firstDiff
		}
	}
	return 0
}
