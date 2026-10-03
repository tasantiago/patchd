package patch

import (
	"reflect"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

func TestRebootDecision(t *testing.T) {
	forte := func(p bool) protocol.RebootSignal { return protocol.RebootSignal{Source: "x", Pending: p} }
	fraco := protocol.RebootSignal{Source: "pfro", Pending: true, Weak: true}
	ilegivel := protocol.RebootSignal{Source: "y", Pending: true, Error: "acesso negado"}

	casos := []struct {
		nome   string
		sinais []protocol.RebootSignal
		quer   *bool
	}{
		{"um forte diz sim", []protocol.RebootSignal{forte(false), forte(true)}, ptr(true)},
		{"fortes dizem não", []protocol.RebootSignal{forte(false), forte(false)}, ptr(false)},
		{"fraco sozinho não decide", []protocol.RebootSignal{fraco}, nil},
		{"fraco com forte negativo", []protocol.RebootSignal{fraco, forte(false)}, ptr(false)},
		{"sinal ilegível não conta", []protocol.RebootSignal{ilegivel}, nil},
		{"nenhum sinal", nil, nil},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			got := rebootDecision(c.sinais)
			if (got == nil) != (c.quer == nil) || (got != nil && *got != *c.quer) {
				t.Errorf("decisão = %v, esperado %v", deref(got), deref(c.quer))
			}
		})
	}
}

func ptr(b bool) *bool { return &b }

func deref(b *bool) any {
	if b == nil {
		return nil
	}
	return *b
}

func TestParseCIMDateTime(t *testing.T) {
	got, err := parseCIMDateTime("20260928110538.500000-240")
	quer := time.Date(2026, 9, 28, 15, 5, 38, 500000000, time.UTC)
	if err != nil || !got.Equal(quer) {
		t.Errorf("parseCIMDateTime = %v, %v; esperado %v (o -240 é UTC-4, em minutos)", got, err, quer)
	}
	for _, ruim := range []string{"", "20260928110538", "20260928110538.500000x240", "2026092811053X.500000-240"} {
		if _, err := parseCIMDateTime(ruim); err == nil {
			t.Errorf("esperado erro para %q", ruim)
		}
	}
}

func TestParseProcStatBtime(t *testing.T) {
	dados := "cpu  1 2 3\nbtime 1791065810\nprocesses 1234\n"
	got, err := parseProcStatBtime([]byte(dados))
	if err != nil || !got.Equal(time.Date(2026, 10, 3, 22, 16, 50, 0, time.UTC)) {
		t.Errorf("btime = %v, %v", got, err)
	}
	if _, err := parseProcStatBtime([]byte("cpu 1\n")); err == nil {
		t.Error("esperado erro sem btime")
	}
}

func TestParseKernBoottime(t *testing.T) {
	got, err := parseKernBoottime([]byte("{ sec = 1791065810, usec = 452031 } Sat Oct  3 18:16:50 2026\n"))
	if err != nil || !got.Equal(time.Date(2026, 10, 3, 22, 16, 50, 0, time.UTC)) {
		t.Errorf("kern.boottime = %v, %v", got, err)
	}
	if _, err := parseKernBoottime([]byte("lixo")); err == nil {
		t.Error("esperado erro fora do formato")
	}
}

func TestNewestKernel(t *testing.T) {
	// Nomes reais do Ubuntu do laboratório, mais o link e um .old.
	ubuntu := []string{"vmlinuz", "vmlinuz.old", "vmlinuz-7.0.0-14-generic", "vmlinuz-7.0.0-34-generic", "initrd.img-7.0.0-34-generic"}
	if got, ok := newestKernel(ubuntu); !ok || got != "7.0.0-34-generic" {
		t.Errorf("Ubuntu: %q, %t", got, ok)
	}
	fedora := []string{"vmlinuz-0-rescue-0123456789abcdef", "vmlinuz-6.15.4-200.fc44.x86_64", "vmlinuz-6.16.1-100.fc44.x86_64"}
	if got, ok := newestKernel(fedora); !ok || got != "6.16.1-100.fc44.x86_64" {
		t.Errorf("Fedora (sem o de recuperação): %q, %t", got, ok)
	}
	if _, ok := newestKernel([]string{"grub", "efi"}); ok {
		t.Error("sem vmlinuz-*, não há kernel")
	}
}

func TestKernelNewer(t *testing.T) {
	if !kernelNewer("7.0.0-34-generic", "7.0.0-14-generic") {
		t.Error("o -34 instalado é mais novo que o -14 em execução (caso real da Aula 2.1)")
	}
	if kernelNewer("7.0.0-34-generic", "7.0.0-34-generic") {
		t.Error("mesmo kernel não é pendência")
	}
}

func TestParseRebootRequiredPkgsReal(t *testing.T) {
	// Conteúdo real do /run/reboot-required.pkgs da VM Ubuntu (Aula 0.3).
	got := parseRebootRequiredPkgs([]byte("libc6\ngnome-shell\nlinux-image-7.0.0-34-generic\nlinux-base\nlibc6\n"))
	quer := []string{"libc6", "gnome-shell", "linux-image-7.0.0-34-generic", "linux-base"}
	if !reflect.DeepEqual(got, quer) {
		t.Errorf("pacotes = %v, esperado %v", got, quer)
	}
}
