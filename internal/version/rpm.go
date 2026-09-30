package version

import (
	"fmt"
	"strings"
)

// CompareRPM compara versões do rpm no formato EVR: [época:]versão[-release].
// Época ausente vale 0. A release só é comparada quando os dois lados têm uma:
// um advisory que diz "corrigido na versão 3.5.8" vale para qualquer release.
func CompareRPM(a, b string) (int, error) {
	ea, va, ra, err := parseEVR(a)
	if err != nil {
		return 0, err
	}
	eb, vb, rb, err := parseEVR(b)
	if err != nil {
		return 0, err
	}
	if c := cmpInt(ea, eb); c != 0 {
		return c, nil
	}
	if c := rpmvercmp(va, vb); c != 0 {
		return c, nil
	}
	if ra != "" && rb != "" {
		return rpmvercmp(ra, rb), nil
	}
	return 0, nil
}

func parseEVR(v string) (epoch int, version, release string, err error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, "", "", fmt.Errorf("versão rpm vazia")
	}
	epoch, rest, err := parseEpoch(v)
	if err != nil {
		return 0, "", "", err
	}
	version, release = splitLast(rest)
	if version == "" {
		return 0, "", "", fmt.Errorf("versão rpm %q: sem versão", v)
	}
	return epoch, version, release, nil
}

func isAlnum(c byte) bool { return isDigit(c) || isAlpha(c) }

// rpmvercmp compara por segmentos numéricos ou alfabéticos; separadores são ignorados.
// '~' ordena antes de tudo (inclusive do fim); '^' ordena depois da versão-base,
// mas antes de qualquer segmento adicional ("1.0" < "1.0^git1" < "1.0.1").
func rpmvercmp(a, b string) int {
	if a == b {
		return 0
	}
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		for i < len(a) && !isAlnum(a[i]) && a[i] != '~' && a[i] != '^' {
			i++
		}
		for j < len(b) && !isAlnum(b[j]) && b[j] != '~' && b[j] != '^' {
			j++
		}

		ta, tb := i < len(a) && a[i] == '~', j < len(b) && b[j] == '~'
		if ta || tb {
			if !ta {
				return 1
			}
			if !tb {
				return -1
			}
			i++
			j++
			continue
		}

		ca, cb := i < len(a) && a[i] == '^', j < len(b) && b[j] == '^'
		if ca || cb {
			if i >= len(a) {
				return -1
			}
			if j >= len(b) {
				return 1
			}
			if !ca {
				return 1
			}
			if !cb {
				return -1
			}
			i++
			j++
			continue
		}

		if i >= len(a) || j >= len(b) {
			break
		}

		si, sj := i, j
		isNum := isDigit(a[i])
		if isNum {
			for i < len(a) && isDigit(a[i]) {
				i++
			}
			for j < len(b) && isDigit(b[j]) {
				j++
			}
		} else {
			for i < len(a) && isAlpha(a[i]) {
				i++
			}
			for j < len(b) && isAlpha(b[j]) {
				j++
			}
		}
		segA, segB := a[si:i], b[sj:j]

		// Tipos diferentes no mesmo ponto: segmento numérico é sempre mais novo.
		if segB == "" {
			if isNum {
				return 1
			}
			return -1
		}
		if isNum {
			segA = strings.TrimLeft(segA, "0")
			segB = strings.TrimLeft(segB, "0")
			if len(segA) != len(segB) {
				return cmpInt(len(segA), len(segB))
			}
		}
		if c := strings.Compare(segA, segB); c != 0 {
			return c
		}
	}
	switch {
	case i >= len(a) && j >= len(b):
		return 0
	case i < len(a):
		return 1
	default:
		return -1
	}
}
