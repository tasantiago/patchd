package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"slices"
	"testing"

	"github.com/tasantiago/patchd/internal/jobs"
)

// TestKeyIDs (Aula 20.7): as impressões das chaves aceitas, na ordem atual → seguinte, sem
// a seguinte quando o build não a tem.
func TestKeyIDs(t *testing.T) {
	a, _, _ := ed25519.GenerateKey(rand.Reader)
	b, _, _ := ed25519.GenerateKey(rand.Reader)
	if got := keyIDs(jobs.KeyID, a, nil); !slices.Equal(got, []string{jobs.KeyID(a)}) {
		t.Errorf("só a atual: %v", got)
	}
	if got := keyIDs(jobs.KeyID, a, b); !slices.Equal(got, []string{jobs.KeyID(a), jobs.KeyID(b)}) {
		t.Errorf("as duas: %v", got)
	}
}
