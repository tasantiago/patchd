package inventory

import (
	"context"
	"io/fs"
	"reflect"
	"testing"
)

// arquivosFalsos simula o sistema de arquivos com um mapa caminho -> conteúdo.
func arquivosFalsos(arquivos map[string]string) func(string) ([]byte, error) {
	return func(p string) ([]byte, error) {
		if c, ok := arquivos[p]; ok {
			return []byte(c), nil
		}
		return nil, fs.ErrNotExist
	}
}

func coletorFalso(arquivos map[string]string) *linuxCollector {
	return &linuxCollector{
		readFile: arquivosFalsos(arquivos),
		hostname: func() (string, error) { return "itom-teste", nil },
	}
}

func TestLinuxOSUbuntu(t *testing.T) {
	c := coletorFalso(map[string]string{
		"/etc/os-release":            "NAME=\"Ubuntu\"\nID=ubuntu\nVERSION_ID=\"26.04\"\nVERSION_CODENAME=resolute\n",
		"/proc/sys/kernel/osrelease": "7.0.0-14-generic\n",
	})
	got, err := c.OS(context.Background())
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if got.ID != "ubuntu" || got.Name != "Ubuntu" || got.Version != "26.04" || got.Codename != "resolute" {
		t.Errorf("identificação errada: %+v", got)
	}
	if got.Kernel != "7.0.0-14-generic" || got.Hostname != "itom-teste" || got.Family != "linux" {
		t.Errorf("kernel, hostname ou família errados: %+v", got)
	}
	quer := []string{"/etc/os-release", "/proc/sys/kernel/osrelease"}
	if !reflect.DeepEqual(got.Sources, quer) {
		t.Errorf("fontes = %v, esperado %v", got.Sources, quer)
	}
}

func TestLinuxOSUsaAlternativaDoOSRelease(t *testing.T) {
	c := coletorFalso(map[string]string{
		"/usr/lib/os-release": "NAME=\"Fedora Linux\"\nID=fedora\nVERSION_ID=44\n",
	})
	got, err := c.OS(context.Background())
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if got.ID != "fedora" || got.Version != "44" || got.Sources[0] != "/usr/lib/os-release" {
		t.Errorf("esperado Fedora 44 vindo de /usr/lib/os-release, veio %+v", got)
	}
	if got.Kernel != "" {
		t.Errorf("sem o arquivo do kernel, o campo deveria ficar vazio: %q", got.Kernel)
	}
}

func TestLinuxOSSemOSRelease(t *testing.T) {
	c := coletorFalso(map[string]string{})
	if _, err := c.OS(context.Background()); err == nil {
		t.Error("esperado erro sem nenhum os-release")
	}
}
