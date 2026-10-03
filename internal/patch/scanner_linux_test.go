package patch

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"
)

// runnerFalso responde por linha de comando exata; comando não previsto é erro.
type runnerFalso map[string]struct {
	saida string
	err   error
}

func (r runnerFalso) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	linha := strings.Join(append([]string{name}, args...), " ")
	resp, ok := r[linha]
	if !ok {
		return nil, fmt.Errorf("comando não previsto no teste: %s", linha)
	}
	return []byte(resp.saida), resp.err
}

func scannerFalso(osRelease string, run runnerFalso) linuxScanner {
	return linuxScanner{
		run: run,
		readFile: func(p string) ([]byte, error) {
			if p == "/etc/os-release" {
				return []byte(osRelease), nil
			}
			return nil, fs.ErrNotExist
		},
		listDir: func(string) ([]string, error) { return nil, errors.New("sem listas no teste") },
		modTime: func(string) (time.Time, error) { return time.Time{}, errors.New("sem listas no teste") },
	}
}

func TestLinuxScanUbuntuUsaApt(t *testing.T) {
	run := runnerFalso{"/usr/bin/apt-get -s dist-upgrade": {saida: aptSimulacao}}
	r := scannerFalso("ID=ubuntu\nID_LIKE=debian\n", run).Scan(context.Background(), Options{})
	if len(r) != 1 || r[0].Source != SourceApt || len(r[0].Missing) != 4 || r[0].Error != "" {
		t.Errorf("esperado um resultado apt com 4 itens: %+v", r)
	}
}

func TestLinuxScanFedoraUsaDnf(t *testing.T) {
	run := runnerFalso{"/usr/bin/dnf advisory list --security --json": {saida: dnfReal}}
	r := scannerFalso("ID=fedora\n", run).Scan(context.Background(), Options{OfflineCatalog: "/x.cab"})
	if len(r) != 2 || r[0].Source != SourceDnf || len(r[0].Missing) != 5 || r[1].Source != SourceWUAOffline {
		t.Errorf("esperado o dnf com 5 itens e a recusa do catálogo offline: %+v", r)
	}
}

func TestLinuxScanAptSemNadaEhListaVazia(t *testing.T) {
	run := runnerFalso{"/usr/bin/apt-get -s dist-upgrade": {saida: "0 upgraded, 0 newly installed.\n"}}
	r := scannerFalso("ID=ubuntu\n", run).Scan(context.Background(), Options{})
	if r[0].Missing == nil || len(r[0].Missing) != 0 {
		t.Errorf("nada faltando deveria ser [] e não null: %+v", r[0])
	}
}

func TestLinuxScanFalhaNoComando(t *testing.T) {
	run := runnerFalso{"/usr/bin/apt-get -s dist-upgrade": {err: errors.New("E: dpkg was interrupted")}}
	r := scannerFalso("ID=ubuntu\n", run).Scan(context.Background(), Options{})
	if r[0].Missing != nil || !strings.Contains(r[0].Error, "dpkg was interrupted") {
		t.Errorf("falha do apt-get: lista nula e motivo: %+v", r[0])
	}
}

func TestLinuxScanDistribuicaoSemSuporte(t *testing.T) {
	r := scannerFalso("ID=opensuse-leap\nID_LIKE=\"suse opensuse\"\n", runnerFalso{}).Scan(context.Background(), Options{})
	if r[0].Missing != nil || !strings.Contains(r[0].Error, "opensuse-leap") {
		t.Errorf("distribuição sem suporte: lista nula e motivo: %+v", r[0])
	}
}
