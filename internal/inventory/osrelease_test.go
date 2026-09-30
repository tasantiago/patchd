package inventory

import "testing"

func TestParseOSRelease(t *testing.T) {
	casos := []struct {
		nome  string
		dados string
		chave string
		quer  string
	}{
		// Linhas reais do laboratório (Módulo 0)
		{"ubuntu nome com aspas", "NAME=\"Ubuntu\"\nVERSION_ID=\"26.04\"\n", "NAME", "Ubuntu"},
		{"ubuntu versão com aspas", "NAME=\"Ubuntu\"\nVERSION_ID=\"26.04\"\n", "VERSION_ID", "26.04"},
		{"ubuntu codinome sem aspas", "VERSION_CODENAME=resolute\n", "VERSION_CODENAME", "resolute"},
		{"fedora versão sem aspas", "NAME=\"Fedora Linux\"\nVERSION_ID=44\n", "VERSION_ID", "44"},
		{"fedora nome com espaço", "NAME=\"Fedora Linux\"\nVERSION_ID=44\n", "NAME", "Fedora Linux"},
		// Casos de borda da especificação
		{"aspas simples", "PRETTY_NAME='Minha Distro 1.0'\n", "PRETTY_NAME", "Minha Distro 1.0"},
		{"escape de aspas", `NAME="Distro \"Beta\""` + "\n", "NAME", `Distro "Beta"`},
		{"escape de cifrão", `NAME="Custo \$0"` + "\n", "NAME", "Custo $0"},
		{"comentário ignorado", "# NAME=Errado\nNAME=Certo\n", "NAME", "Certo"},
		{"sinal de igual no valor", "CONFIG=\"a=1,b=2\"\n", "CONFIG", "a=1,b=2"},
		{"espaços em volta", "  ID = ubuntu  \n", "ID", "ubuntu"},
		{"quebra de linha do Windows", "ID=fedora\r\n", "ID", "fedora"},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			campos := parseOSRelease([]byte(c.dados))
			if got := campos[c.chave]; got != c.quer {
				t.Errorf("%s = %q, esperado %q", c.chave, got, c.quer)
			}
		})
	}
}

func TestParseOSReleaseIgnoraLinhaInvalida(t *testing.T) {
	campos := parseOSRelease([]byte("linha sem igual\nID=ubuntu\n"))
	if len(campos) != 1 || campos["ID"] != "ubuntu" {
		t.Errorf("esperado só ID=ubuntu, veio %v", campos)
	}
}
