package patch

import (
	"context"
	"io/fs"
	"reflect"
	"strings"
	"testing"
	"time"
)

func scannerDeReboot(arquivos map[string]string, boot []string) linuxScanner {
	return linuxScanner{
		readFile: func(p string) ([]byte, error) {
			if c, ok := arquivos[p]; ok {
				return []byte(c), nil
			}
			return nil, fs.ErrNotExist
		},
		listDir: func(p string) ([]string, error) {
			if p == bootDir && boot != nil {
				return boot, nil
			}
			return nil, fs.ErrNotExist
		},
		modTime: func(p string) (time.Time, error) {
			if _, ok := arquivos[p]; ok {
				return time.Now(), nil
			}
			return time.Time{}, fs.ErrNotExist
		},
	}
}

func TestLinuxRebootUbuntuPendente(t *testing.T) {
	// Situação real da VM na Aula 0.3: kernel -14 em execução, -34 instalado, arquivo presente.
	s := scannerDeReboot(map[string]string{
		"/etc/os-release":      "ID=ubuntu\nID_LIKE=debian\n",
		rebootRequiredPath:     "*** System restart required ***\n",
		rebootRequiredPkgsPath: "libc6\nlinux-image-7.0.0-34-generic\n",
		runningKernelPath:      "7.0.0-14-generic\n",
		procStatPath:           "btime 1791065810\n",
	}, []string{"vmlinuz", "vmlinuz-7.0.0-14-generic", "vmlinuz-7.0.0-34-generic"})

	st, err := s.Reboot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Pending == nil || !*st.Pending {
		t.Fatalf("esperado pendente: %+v", st)
	}
	if len(st.Signals) != 2 || !st.Signals[0].Pending || !st.Signals[1].Pending {
		t.Errorf("os dois sinais deveriam dizer sim: %+v", st.Signals)
	}
	if st.RunningKernel != "7.0.0-14-generic" || st.NewestKernel != "7.0.0-34-generic" {
		t.Errorf("kernels errados: %+v", st)
	}
	if !reflect.DeepEqual(st.Packages, []string{"libc6", "linux-image-7.0.0-34-generic"}) || st.LastBoot == nil {
		t.Errorf("pacotes ou última inicialização errados: %+v", st)
	}
}

func TestLinuxRebootFedoraSemArquivoEmDia(t *testing.T) {
	s := scannerDeReboot(map[string]string{
		"/etc/os-release": "ID=fedora\n",
		runningKernelPath: "6.16.1-100.fc44.x86_64\n",
	}, []string{"vmlinuz-0-rescue-abc", "vmlinuz-6.16.1-100.fc44.x86_64"})

	st, _ := s.Reboot(context.Background())
	if len(st.Signals) != 1 || st.Signals[0].Source != kernelSignalSource {
		t.Errorf("no Fedora, só o sinal do kernel: %+v", st.Signals)
	}
	if st.Pending == nil || *st.Pending {
		t.Errorf("kernel em execução é o mais novo: não pendente: %+v", st)
	}
}

func TestLinuxRebootContainerSemBoot(t *testing.T) {
	s := scannerDeReboot(map[string]string{
		"/etc/os-release": "ID=fedora\n",
		runningKernelPath: "6.6.87.2-microsoft-standard-WSL2\n",
	}, nil)
	st, _ := s.Reboot(context.Background())
	if st.Pending != nil || st.Signals[0].Error == "" {
		t.Errorf("sem /boot (container), o estado é desconhecido: %+v", st)
	}
}

func TestLinuxRebootEmContainerEhDesconhecido(t *testing.T) {
	// A situação da Aula 4.2: Debian em container, sem /run/reboot-required.
	s := scannerDeReboot(map[string]string{
		"/etc/os-release": "ID=debian\n",
		runningKernelPath: "6.6.87.2-microsoft-standard-WSL2\n",
	}, nil)
	s.inContainer = func() (bool, string) { return true, "/.dockerenv" }

	st, err := s.Reboot(context.Background())
	if err != nil || st.Pending != nil || len(st.Signals) != 0 || !strings.Contains(st.Note, "container") {
		t.Errorf("em container, o reinício pendente é desconhecido, com nota: %+v %v", st, err)
	}
}
