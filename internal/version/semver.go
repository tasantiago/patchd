package version

import (
	"fmt"
	"strconv"
	"strings"
)

// CompareSemver compara versões no formato do patchd (SemVer 2.0 com o "v" na frente):
// vMAIOR.MENOR.CORREÇÃO[-pré-lançamento][+build]. O build é ignorado. Pré-lançamento vem
// antes do lançamento (v0.5.0-dev < v0.5.0), e os identificadores do pré-lançamento são
// comparados um a um: numéricos como número, os demais como texto, numérico antes de texto.
func CompareSemver(a, b string) (int, error) {
	pa, err := parseSemver(a)
	if err != nil {
		return 0, err
	}
	pb, err := parseSemver(b)
	if err != nil {
		return 0, err
	}
	for i := range 3 {
		if c := cmpInt(pa.core[i], pb.core[i]); c != 0 {
			return c, nil
		}
	}
	switch {
	case len(pa.pre) == 0 && len(pb.pre) == 0:
		return 0, nil
	case len(pa.pre) == 0:
		return 1, nil
	case len(pb.pre) == 0:
		return -1, nil
	}
	for i := 0; i < len(pa.pre) && i < len(pb.pre); i++ {
		if c := cmpPreID(pa.pre[i], pb.pre[i]); c != 0 {
			return c, nil
		}
	}
	return cmpInt(len(pa.pre), len(pb.pre)), nil
}

type semver struct {
	core [3]int
	pre  []string
}

func parseSemver(v string) (semver, error) {
	var s semver
	rest, ok := strings.CutPrefix(v, "v")
	if !ok {
		return s, fmt.Errorf("versão %q: falta o \"v\" inicial (ex.: v0.5.0)", v)
	}
	rest, _, _ = strings.Cut(rest, "+")
	core, pre, hasPre := strings.Cut(rest, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return s, fmt.Errorf("versão %q: use vMAIOR.MENOR.CORREÇÃO", v)
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || (len(p) > 1 && p[0] == '0') {
			return s, fmt.Errorf("versão %q: %q não é um número válido", v, p)
		}
		s.core[i] = n
	}
	if hasPre {
		s.pre = strings.Split(pre, ".")
		for _, id := range s.pre {
			if id == "" {
				return s, fmt.Errorf("versão %q: identificador de pré-lançamento vazio", v)
			}
		}
	}
	return s, nil
}

func cmpPreID(a, b string) int {
	na, errA := strconv.Atoi(a)
	nb, errB := strconv.Atoi(b)
	switch {
	case errA == nil && errB == nil:
		return cmpInt(na, nb)
	case errA == nil:
		return -1
	case errB == nil:
		return 1
	default:
		return strings.Compare(a, b)
	}
}
