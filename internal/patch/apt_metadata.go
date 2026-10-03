package patch

import (
	"strings"
	"time"
)

// aptUpdateStamp é tocado pelo Ubuntu a cada "apt update" bem-sucedido.
const aptUpdateStamp = "/var/lib/apt/periodic/update-success-stamp"

// aptMetadataDates devolve duas datas diferentes:
//   - published: quão novo é o catálogo que a máquina tem, pelo Date: assinado do InRelease
//     do pocket -security (ou, sem ele, do InRelease mais recente);
//   - checked: o último "apt update" bem-sucedido.
//
// Máquina que ficou desligada tem as duas velhas (resultado suspeito); pocket sem novidade
// tem só a primeira velha (resultado confiável). Data ausente fica nula.
func aptMetadataDates(readFile func(string) ([]byte, error), listDir func(string) ([]string, error),
	modTime func(string) (time.Time, error)) (published, checked *time.Time) {

	if names, err := listDir(aptListsDir); err == nil {
		var security, newest time.Time
		for _, n := range names {
			if !strings.HasSuffix(n, "_InRelease") {
				continue
			}
			data, err := readFile(aptListsDir + "/" + n)
			if err != nil {
				continue
			}
			t, ok := inReleaseDate(data)
			if !ok {
				continue
			}
			if t.After(newest) {
				newest = t
			}
			if strings.Contains(n, "-security_") && t.After(security) {
				security = t
			}
		}
		switch {
		case !security.IsZero():
			published = &security
		case !newest.IsZero():
			published = &newest
		}
	}

	if t, err := modTime(aptUpdateStamp); err == nil {
		u := t.UTC()
		checked = &u
	}
	return published, checked
}

// inReleaseDate lê o campo Date: do InRelease, ex.: "Date: Tue, 29 Sep 2026 18:35:33 UTC".
func inReleaseDate(data []byte) (time.Time, bool) {
	for _, line := range strings.Split(string(data), "\n") {
		v, ok := strings.CutPrefix(line, "Date:")
		if !ok {
			continue
		}
		t, err := time.Parse(time.RFC1123, strings.TrimSpace(v))
		if err != nil {
			return time.Time{}, false
		}
		return t.UTC(), true
	}
	return time.Time{}, false
}
