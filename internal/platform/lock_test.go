package platform

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestTryLockExclusiva(t *testing.T) {
	p := filepath.Join(t.TempDir(), "install.lock")
	unlock, err := TryLock(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := TryLock(p); !errors.Is(err, ErrLocked) {
		t.Fatalf("com a trava tomada, a segunda tentativa deve dar ErrLocked: %v", err)
	}
	unlock()
	unlock2, err := TryLock(p)
	if err != nil {
		t.Fatalf("depois de soltar, a trava volta a estar livre: %v", err)
	}
	unlock2()
}
