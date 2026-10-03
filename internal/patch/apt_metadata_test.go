package patch

import (
	"errors"
	"io/fs"
	"testing"
	"time"
)

// Início de um InRelease assinado, com a linha Date: real do laboratório (Aula 3.4).
const inReleaseSeguranca = `-----BEGIN PGP SIGNED MESSAGE-----
Hash: SHA512

Origin: Ubuntu
Label: Ubuntu
Suite: resolute-security
Codename: resolute
Date: Tue, 29 Sep 2026 18:35:33 UTC
Architectures: amd64 arm64
`

const inReleaseUpdates = `-----BEGIN PGP SIGNED MESSAGE-----
Suite: resolute-updates
Date: Wed, 30 Sep 2026 09:00:00 UTC
`

func TestAptMetadataDates(t *testing.T) {
	arquivos := map[string]string{
		aptListsDir + "/security.ubuntu.com_ubuntu_dists_resolute-security_InRelease": inReleaseSeguranca,
		aptListsDir + "/archive.ubuntu.com_ubuntu_dists_resolute-updates_InRelease":   inReleaseUpdates,
	}
	readFile := func(p string) ([]byte, error) {
		if c, ok := arquivos[p]; ok {
			return []byte(c), nil
		}
		return nil, fs.ErrNotExist
	}
	listDir := func(string) ([]string, error) {
		return []string{
			"archive.ubuntu.com_ubuntu_dists_resolute-updates_InRelease",
			"security.ubuntu.com_ubuntu_dists_resolute-security_InRelease",
			"security.ubuntu.com_ubuntu_dists_resolute-security_main_binary-amd64_Packages",
		}, nil
	}
	// Stamp real: 29/09 às 20:57:36 em Porto Velho (UTC-4).
	stamp := time.Date(2026, 9, 29, 20, 57, 36, 0, time.FixedZone("-04", -4*3600))
	modTime := func(p string) (time.Time, error) {
		if p == aptUpdateStamp {
			return stamp, nil
		}
		return time.Time{}, fs.ErrNotExist
	}

	published, checked := aptMetadataDates(readFile, listDir, modTime)
	if published == nil || !published.Equal(time.Date(2026, 9, 29, 18, 35, 33, 0, time.UTC)) {
		t.Errorf("published deveria ser o Date: do -security (mesmo com o -updates mais novo): %v", published)
	}
	if checked == nil || !checked.Equal(stamp) || checked.Location() != time.UTC {
		t.Errorf("checked deveria ser o stamp, em UTC: %v", checked)
	}
}

func TestAptMetadataDatesSemNada(t *testing.T) {
	semLista := func(string) ([]string, error) { return nil, errors.New("sem acesso") }
	semStamp := func(string) (time.Time, error) { return time.Time{}, fs.ErrNotExist }
	published, checked := aptMetadataDates(nil, semLista, semStamp)
	if published != nil || checked != nil {
		t.Errorf("sem listas nem stamp, as duas datas ficam nulas: %v, %v", published, checked)
	}
}

func TestInReleaseDate(t *testing.T) {
	if _, ok := inReleaseDate([]byte("Origin: Ubuntu\nDate: data inválida\n")); ok {
		t.Error("Date: ilegível não deveria virar data")
	}
	if _, ok := inReleaseDate([]byte("Origin: Ubuntu\n")); ok {
		t.Error("sem Date: não há data")
	}
}
