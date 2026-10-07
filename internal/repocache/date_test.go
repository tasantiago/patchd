package repocache

import (
	"testing"
	"time"
)

func TestReleaseDate(t *testing.T) {
	casos := map[string]time.Time{
		"-----BEGIN PGP SIGNED MESSAGE-----\nHash: SHA512\n\nOrigin: Ubuntu\nDate: Mon, 05 Oct 2026 13:50:19 UTC\nMD5Sum:\n": time.Date(2026, 10, 5, 13, 50, 19, 0, time.UTC),
		"Origin: Ubuntu\nDate: Mon, 05 Oct 2026  1:08:00 UTC\n":                                                              time.Date(2026, 10, 5, 1, 8, 0, 0, time.UTC),
		"Date: Thu, 3 Apr 2026 17:07:15 UTC\n":                                                                               time.Date(2026, 4, 3, 17, 7, 15, 0, time.UTC),
		"Origin: Ubuntu\nSHA256:\n Date: Mon, 05 Oct 2026 13:50:19 UTC\n":                                                    {},
		"\x89PNG binário sem campos":                                                                                         {},
	}
	for in, want := range casos {
		if got := releaseDate([]byte(in)); !got.Equal(want) {
			t.Errorf("%q: %v, esperado %v", in[:min(len(in), 40)], got, want)
		}
	}
}
