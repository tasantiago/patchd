package biospw

import (
	"errors"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/machinebox"
)

func TestSorteio(t *testing.T) {
	vistos := map[rune]int{}
	senhas := map[string]bool{}
	for range 2000 {
		pw, err := Generate()
		if err != nil {
			t.Fatal(err)
		}
		if err := Check(pw); err != nil {
			t.Fatalf("%q: %v", pw, err)
		}
		senhas[pw] = true
		for _, r := range pw {
			vistos[r]++
		}
	}
	if len(senhas) != 2000 {
		t.Error("senha repetida em 2000 sorteios")
	}
	// Todos os 31 símbolos aparecem, nenhum de fora, e nenhum muito acima ou abaixo da média
	// (24000 símbolos / 31 = 774; a folga cobre a variação normal).
	if len(vistos) != len(Alphabet) {
		t.Errorf("%d símbolos distintos, esperava %d", len(vistos), len(Alphabet))
	}
	for r, n := range vistos {
		if !strings.ContainsRune(Alphabet, r) || n < 600 || n > 950 {
			t.Errorf("%q: %d vezes", r, n)
		}
	}
	for _, c := range "0o1li" {
		if strings.ContainsRune(Alphabet, c) {
			t.Errorf("ambíguo %q no alfabeto", c)
		}
	}
}

func TestFormato(t *testing.T) {
	for _, ruim := range []string{"", "abcdefghjkm", "abcdefghjkmnp", "abcdefghjkm0", "ABCDEFGHJKMN", "abcd efgh jk", "abcdefghjkmç"} {
		if Check(ruim) == nil {
			t.Errorf("%q aceita", ruim)
		}
	}
	if Grouped("abcdefghjkmn") != "abcd efgh jkmn" {
		t.Error(Grouped("abcdefghjkmn"))
	}
}

func TestGuarda(t *testing.T) {
	priv, _ := machinebox.Generate()
	pub, _ := machinebox.Public(priv)
	pw, _ := Generate()
	env, err := Seal(pub, "60ef97e4-0001", pw)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(env), pw) {
		t.Fatal("a senha aparece no envelope")
	}
	if got, err := Open(priv, "60ef97e4-0001", env); err != nil || got != pw {
		t.Fatalf("abrir: %q %v", got, err)
	}
	if _, err := Open(priv, "7aa604f7-0002", env); !errors.Is(err, ErrOtherMachine) {
		t.Errorf("outra máquina: %v", err)
	}
	outra, _ := machinebox.Generate()
	if _, err := Open(outra, "60ef97e4-0001", env); err == nil {
		t.Error("abriu com outra chave de guarda")
	}
	// Um envelope da caixa da máquina (outro propósito) não abre como guarda.
	caixa, _ := machinebox.Seal(pub, machinebox.PurposeBIOS, []byte("60ef97e4-0001\n"+pw))
	if _, err := Open(priv, "60ef97e4-0001", caixa); err == nil {
		t.Error("envelope de outro propósito aberto como guarda")
	}
	if _, err := Seal(pub, "60ef97e4-0001", "curta"); err == nil {
		t.Error("senha fora do formato fechada")
	}
	if _, err := Seal(pub, "a\nb", pw); err == nil {
		t.Error("ID com quebra de linha aceito")
	}
}
