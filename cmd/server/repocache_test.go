package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func lookMap(env map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := env[k]; return v, ok }
}

func TestRepoCacheLimits(t *testing.T) {
	opts, err := repoCacheLimits(lookMap(nil))
	if err != nil || opts.MaxAge != 30*24*time.Hour || opts.MaxSize != 0 {
		t.Errorf("padrão: %+v %v", opts, err)
	}
	opts, err = repoCacheLimits(lookMap(map[string]string{"PATCHD_REPO_CACHE_MAX_AGE": "0", "PATCHD_REPO_CACHE_MAX_SIZE": "50G"}))
	if err != nil || opts.MaxAge != 0 || opts.MaxSize != 50<<30 {
		t.Errorf("desligado por idade, 50G: %+v %v", opts, err)
	}
	for _, env := range []map[string]string{
		{"PATCHD_REPO_CACHE_MAX_AGE": "1h"},
		{"PATCHD_REPO_CACHE_MAX_AGE": "trinta dias"},
		{"PATCHD_REPO_CACHE_MAX_SIZE": "muito"},
	} {
		if _, err := repoCacheLimits(lookMap(env)); err == nil {
			t.Errorf("%v aceito", env)
		}
	}
}

func TestRunRepoCacheStatusEPrune(t *testing.T) {
	dir := t.TempDir()
	velho := filepath.Join(dir, "pool", "archive.ubuntu.com", "ubuntu", "pool", "main", "a", "a_1_all.deb")
	os.MkdirAll(filepath.Dir(velho), 0o750)
	os.WriteFile(velho, []byte("deb"), 0o640)
	look := lookMap(map[string]string{"PATCHD_REPO_CACHE_DIR": dir})

	// O primeiro uso do cache marca o formato (as datas viram "agora"); depois disso, o
	// pacote fica 60 dias sem uso.
	var out, errb bytes.Buffer
	if code := runRepoCache([]string{"status"}, look, &out, &errb); code != exitOK || !strings.Contains(out.String(), "pool    1") {
		t.Errorf("status: %d %q %q", code, out.String(), errb.String())
	}
	antes := time.Now().Add(-60 * 24 * time.Hour)
	os.Chtimes(velho, antes, antes)
	out.Reset()
	if code := runRepoCache([]string{"prune", "-dry-run"}, look, &out, &errb); code != exitOK || !strings.Contains(out.String(), "sairiam (simulação): 1 arquivo(s)") {
		t.Errorf("dry-run: %d %q", code, out.String())
	}
	if _, err := os.Stat(velho); err != nil {
		t.Error("o dry-run apagou")
	}
	out.Reset()
	if code := runRepoCache([]string{"prune"}, look, &out, &errb); code != exitOK || !strings.Contains(out.String(), "removidos: 1 arquivo(s)") {
		t.Errorf("prune: %d %q", code, out.String())
	}
	if _, err := os.Stat(velho); err == nil {
		t.Error("o prune não apagou")
	}
	if code := runRepoCache([]string{"status"}, lookMap(nil), &out, &errb); code != exitConfig {
		t.Errorf("sem PATCHD_REPO_CACHE_DIR: %d", code)
	}
}
