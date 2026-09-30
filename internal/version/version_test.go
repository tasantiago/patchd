package version

import "testing"

type caso struct {
	a, b string
	quer int
}

func confere(t *testing.T, s Scheme, casos []caso) {
	t.Helper()
	for _, c := range casos {
		got, err := Compare(s, c.a, c.b)
		if err != nil {
			t.Errorf("%s: Compare(%q, %q): erro %v", s, c.a, c.b, err)
			continue
		}
		if got != c.quer {
			t.Errorf("%s: Compare(%q, %q) = %d, esperado %d", s, c.a, c.b, got, c.quer)
		}
		// Antissimetria: trocar os lados inverte o resultado.
		if inv, _ := Compare(s, c.b, c.a); inv != -c.quer {
			t.Errorf("%s: Compare(%q, %q) = %d, esperado %d (antissimetria)", s, c.b, c.a, inv, -c.quer)
		}
	}
}

func TestDpkgVersoesReais(t *testing.T) {
	confere(t, Dpkg, []caso{
		{"1.34.6-1", "1.34.6-1ubuntu0.1", -1},                  // libcares2: backport de segurança
		{"5.0.0~beta1-0ubuntu7", "5.0.2-0ubuntu1~26.04.1", -1}, // apparmor (Aula 0.1)
		{"1:9.20.18-1ubuntu2", "1:9.20.24-1ubuntu0.3", -1},     // bind9 com época
		{"7.0.0-14.14", "7.0.0-34.34", -1},                     // kernels instalados
		{"2.43-2ubuntu2.4", "2.43-2ubuntu2.4", 0},              // libc6 igual a si mesmo
	})
}

func TestDpkgRegras(t *testing.T) {
	confere(t, Dpkg, []caso{
		{"1:1.0", "2.0", 1},    // época vence tudo
		{"1.0~rc1", "1.0", -1}, // til antes do fim
		{"1.0", "1.0+b1", -1},  // símbolo depois do fim
		{"1.0a", "1.0+", -1},   // letra antes de símbolo
		{"1.0", "1.0-0", 0},    // revisão ausente equivale a 0
		{"1.10", "1.9", 1},     // numérico, não texto
		{"1.001", "1.1", 0},    // zeros à esquerda não contam
	})
}

func TestDpkgSequenciaDaPolitica(t *testing.T) {
	// Ordem crescente do exemplo da política do Debian: ~~ < ~~a < ~ < (nada) < a.
	seq := []string{"1.0~~", "1.0~~a", "1.0~", "1.0", "1.0a"}
	for i := 0; i+1 < len(seq); i++ {
		if c, _ := CompareDpkg(seq[i], seq[i+1]); c != -1 {
			t.Errorf("esperado %q < %q, veio %d", seq[i], seq[i+1], c)
		}
	}
}

func TestDpkgInvalida(t *testing.T) {
	for _, v := range []string{"", "a:1.0", "-1:1.0", "1:-1"} {
		if _, err := CompareDpkg(v, "1.0"); err == nil {
			t.Errorf("esperado erro para %q", v)
		}
	}
}

func TestRPMVersoesReais(t *testing.T) {
	confere(t, RPM, []caso{
		{"1:3.5.7-2.fc44", "1:3.5.8-1.fc44", -1}, // openssl-libs instalado x advisory
		{"8.18.0-8.fc44", "8.18.0-10.fc44", -1},  // curl: release 8 < 10 (numérico)
		{"2:1.35-8.fc44", "2:1.35-9.fc44", -1},   // tar com época
		{"5.3.9-3.fc44", "5.3.9-3.fc44", 0},      // bash igual a si mesmo
	})
}

func TestRPMRegras(t *testing.T) {
	confere(t, RPM, []caso{
		{"1.0~rc1", "1.0", -1},       // til antes de tudo
		{"1.0^git1", "1.0", 1},       // circunflexo depois da base...
		{"1.0^git1", "1.0.1", -1},    // ...mas antes de um segmento a mais
		{"1.0a", "1.0.1", -1},        // segmento numérico é mais novo que alfabético
		{"1.0", "1_0", 0},            // separadores diferentes não contam
		{"1.0-1", "1:0.1-1", -1},     // época ausente vale 0
		{"3.5.8", "3.5.8-1.fc44", 0}, // sem release de um lado: só a versão conta
		{"1.10", "1.9", 1},           // numérico, não texto
	})
}

func TestDotted(t *testing.T) {
	confere(t, Dotted, []caso{
		{"26200.9457", "26200.9400", 1}, // UBR do laboratório acima de um alvo antigo
		{"26.5.2", "26.7.1", -1},        // macOS do laboratório abaixo da atualização oferecida
		{"26.10", "26.9", 1},
		{"26.5", "26.5.0", 0},
	})
	if _, err := CompareDotted("25H2", "24H2"); err == nil {
		t.Error("DisplayVersion do Windows não é comparável numericamente")
	}
}

func TestEsquemaDesconhecido(t *testing.T) {
	if _, err := Compare("semver", "1.0.0", "1.0.1"); err == nil {
		t.Error("esperado erro para esquema desconhecido")
	}
}
