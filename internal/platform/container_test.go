package platform

import (
	"io/fs"
	"strings"
	"testing"
)

func TestDetectContainer(t *testing.T) {
	semNada := func(string) bool { return false }
	semArquivo := func(string) ([]byte, error) { return nil, fs.ErrNotExist }
	semVariavel := func(string) string { return "" }
	cgroup := func(s string) func(string) ([]byte, error) {
		return func(string) ([]byte, error) { return []byte(s), nil }
	}

	casos := []struct {
		nome     string
		exists   func(string) bool
		readFile func(string) ([]byte, error)
		getenv   func(string) string
		quer     string // trecho do motivo; vazio = não é container
	}{
		{"docker", func(p string) bool { return p == "/.dockerenv" }, semArquivo, semVariavel, "dockerenv"},
		{"podman", func(p string) bool { return p == "/run/.containerenv" }, semArquivo, semVariavel, "containerenv"},
		{"nspawn", semNada, semArquivo, func(string) string { return "systemd-nspawn" }, "container=systemd-nspawn"},
		{"cgroup v1 do docker", semNada, cgroup("12:pids:/docker/3f2a9c\n"), semVariavel, "docker"},
		{"kubernetes", semNada, cgroup("0::/kubepods/besteffort/pod1\n"), semVariavel, "kubepods"},
		{"máquina comum, cgroup v2", semNada, cgroup("0::/init.scope\n"), semVariavel, ""},
		{"máquina comum, cgroup v1 (systemd)", semNada, cgroup("9:name=systemd:/\n8:pids:/\n"), semVariavel, ""},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			got, why := detectContainer(c.exists, c.readFile, c.getenv)
			if got != (c.quer != "") || !strings.Contains(why, c.quer) {
				t.Errorf("detectContainer = %t %q, esperado %q", got, why, c.quer)
			}
		})
	}
}
