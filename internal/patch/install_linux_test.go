package patch

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/jobs"
)

func instaladorFalso(osRelease string, run runnerFalso) linuxInstaller {
	return linuxInstaller{run: run, readFile: func(p string) ([]byte, error) {
		if p == "/etc/os-release" {
			return []byte(osRelease), nil
		}
		return nil, fs.ErrNotExist
	}}
}

const aptInstalar = "/usr/bin/systemd-run --unit=patchd-job-7 --wait --pipe --collect --quiet --setenv=LC_ALL=C " +
	"--setenv=DEBIAN_FRONTEND=noninteractive -- /usr/bin/apt-get install -y --only-upgrade -o DPkg::Lock::Timeout=300 " +
	"-o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold libcares2 openssl"

func TestInstalarNoUbuntu(t *testing.T) {
	ctx := context.Background()
	run := runnerFalso{
		"/usr/bin/apt-get -s dist-upgrade": {saida: aptSimulacao},
		aptInstalar:                        {saida: "Reading package lists...\n2 upgraded, 0 newly installed, 0 to remove and 2 not upgraded.\nSetting up openssl ...\nProcessing triggers for libc-bin (2.39-0ubuntu8.9) ...\n"},
	}
	ubuntu := instaladorFalso("ID=ubuntu\nID_LIKE=debian\n", run)
	pedido := []jobs.Package{{Name: "libcares2", MinVersion: "1.34.6-1ubuntu0.1"}, {Name: "openssl", MinVersion: "3.5.0-1ubuntu1.1"}}
	det, err := ubuntu.Install(ctx, 7, pedido)
	if err != nil || !strings.Contains(det, "apt-get: 2 pacote(s) pedidos (libcares2, openssl); 2 upgraded") {
		t.Fatalf("instalar: %q %v", det, err)
	}

	// Recusas: nada é instalado (o runner falso não tem outro comando).
	for nome, p := range map[string][]jobs.Package{
		"não pendente":     {{Name: "bash", MinVersion: "5.2"}},
		"candidata velha":  {{Name: "libcares2", MinVersion: "1.34.6-1ubuntu0.2"}},
		"um bom e um ruim": {{Name: "openssl", MinVersion: "3.5.0-1ubuntu1.2"}, {Name: "bash", MinVersion: "5.2"}},
	} {
		_, err := ubuntu.Install(ctx, 8, p)
		if !errors.Is(err, jobs.ErrNotPending) {
			t.Errorf("%s: %v", nome, err)
		}
	}

	// Falha do apt: erro comum (vira "falhou"), com o fim da saída.
	run[aptInstalar] = struct {
		saida string
		err   error
	}{saida: "E: Could not get lock /var/lib/dpkg/lock-frontend\n", err: errors.New("exit status 100")}
	if det, err := ubuntu.Install(ctx, 7, pedido); err == nil || errors.Is(err, jobs.ErrNotPending) || !strings.Contains(det, "Could not get lock") {
		t.Errorf("falha do apt: %q %v", det, err)
	}
}

func TestInstalarNoFedora(t *testing.T) {
	run := runnerFalso{
		"/usr/bin/dnf advisory list --security --json": {saida: dnfReal},
		"/usr/bin/systemd-run --unit=patchd-job-9 --wait --pipe --collect --quiet --setenv=LC_ALL=C -- /usr/bin/dnf upgrade -y openssl-libs curl": {saida: "Complete!\n"},
	}
	fedora := instaladorFalso("ID=fedora\n", run)
	det, err := fedora.Install(context.Background(), 9, []jobs.Package{{Name: "openssl-libs", MinVersion: "1:3.5.8-1.fc44"}, {Name: "curl", MinVersion: "8.18.0-9.fc44"}})
	if err != nil || !strings.Contains(det, "dnf: 2 pacote(s)") {
		t.Fatalf("fedora: %q %v", det, err)
	}
	// Sem a época, 3.5.9 é "mais nova" que 1:3.5.8? Não: a época vence, e a candidata fica velha.
	if _, err := fedora.Install(context.Background(), 9, []jobs.Package{{Name: "openssl-libs", MinVersion: "2:0.1-1.fc44"}}); !errors.Is(err, jobs.ErrNotPending) {
		t.Errorf("época: %v", err)
	}
	if _, err := instaladorFalso("ID=arch\n", run).Install(context.Background(), 1, []jobs.Package{{Name: "x", MinVersion: "1"}}); !errors.Is(err, jobs.ErrNotPending) {
		t.Errorf("distribuição sem suporte: %v", err)
	}
}
