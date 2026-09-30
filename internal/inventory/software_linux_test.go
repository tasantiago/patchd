package inventory

import (
	"context"
	"errors"
	"testing"
)

func coletorLinuxFalso(osRelease string, run runnerFalso) *linuxCollector {
	return &linuxCollector{
		run:      run,
		readFile: arquivosFalsos(map[string]string{"/etc/os-release": osRelease}),
		hostname: hostnameFalso,
	}
}

func TestLinuxSoftwareUbuntuUsaDpkg(t *testing.T) {
	run := runnerFalso{
		dpkgQueryPath + " -W --showformat=" + dpkgFormat: {saida: dpkgReal},
	}
	c := coletorLinuxFalso("ID=ubuntu\nID_LIKE=debian\n", run)
	list, err := c.Software(context.Background())
	if err != nil || len(list) != 5 {
		t.Fatalf("esperados 5 pacotes sem erro: %d, %v", len(list), err)
	}
	if list[0].Name != "bind9-host" {
		t.Errorf("a lista deveria vir ordenada por nome: %q", list[0].Name)
	}
}

func TestLinuxSoftwareFedoraUsaRPM(t *testing.T) {
	run := runnerFalso{
		rpmPath + " -qa --queryformat " + rpmFormat: {saida: rpmReal},
	}
	c := coletorLinuxFalso("NAME=\"Fedora Linux\"\nID=fedora\nVERSION_ID=44\n", run)
	list, err := c.Software(context.Background())
	if err != nil || len(list) != 4 {
		t.Fatalf("esperados 4 pacotes sem erro: %d, %v", len(list), err)
	}
}

func TestLinuxSoftwareDistribuicaoSemSuporte(t *testing.T) {
	c := coletorLinuxFalso("ID=opensuse-leap\nID_LIKE=\"suse opensuse\"\n", runnerFalso{})
	_, err := c.Software(context.Background())
	if !errors.Is(err, ErrNotImplemented) {
		t.Errorf("esperado ErrNotImplemented, veio %v", err)
	}
}

func TestLinuxSoftwareFalhaNoComando(t *testing.T) {
	run := runnerFalso{
		dpkgQueryPath + " -W --showformat=" + dpkgFormat: {err: errors.New("dpkg-query: banco travado")},
	}
	c := coletorLinuxFalso("ID=ubuntu\nID_LIKE=debian\n", run)
	if list, err := c.Software(context.Background()); err == nil || list != nil {
		t.Errorf("esperado erro sem lista: %+v, %v", list, err)
	}
}
