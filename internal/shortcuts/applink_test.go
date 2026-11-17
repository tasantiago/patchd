package shortcuts

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// lnkFalso: o .lnk de teste guarda o alvo como texto.
func lnkFalso(t *testing.T) (AppLink, string) {
	t.Helper()
	desk := t.TempDir()
	app := filepath.Join(t.TempDir(), "patchd-app.exe")
	return AppLink{Desktop: desk, Target: app, Icon: `C:\Windows\System32\imageres.dll,73`,
		Read: func(p string) (string, error) {
			b, err := os.ReadFile(p)
			if err != nil {
				return "", err
			}
			alvo, _, _ := strings.Cut(string(b), "|")
			return alvo, nil
		},
		Write: func(p, target, desc, icon string) error {
			return os.WriteFile(p, []byte(target+"|"+desc+"|"+icon), 0o600)
		}}, desk
}

func lnks(t *testing.T, dir string) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(dir, "*"))
	var out []string
	for _, f := range m {
		out = append(out, filepath.Base(f))
	}
	slices.Sort(out)
	return out
}

// O atalho da Central: criado com o nome do painel, em dia na segunda vez, refeito com o nome
// novo (o antigo sai), e só os que apontam para a Central saem na remoção.
func TestAtalhoDaCentral(t *testing.T) {
	l, desk := lnkFalso(t)
	outro := filepath.Join(desk, "Outro programa.lnk")
	_ = os.WriteFile(outro, []byte(`C:\Outro\outro.exe|x|y`), 0o600)
	_ = os.WriteFile(filepath.Join(desk, "nota.txt"), []byte(l.Target), 0o600)

	st, rem, err := l.Sync("Central de software")
	if err != nil || st != AppLinkCreated || len(rem) != 0 {
		t.Fatalf("criar: %s %v %v", st, rem, err)
	}
	b, _ := os.ReadFile(filepath.Join(desk, "Central de software.lnk"))
	if string(b) != l.Target+"|Central de software|"+l.Icon {
		t.Errorf("atalho: %q", b)
	}
	if st, _, err := l.Sync("Central de software"); err != nil || st != AppLinkCurrent {
		t.Errorf("de novo: %s %v", st, err)
	}
	// O nome mudou no painel: um só, com o nome novo.
	st, rem, err = l.Sync("Loja da TI")
	if err != nil || st != AppLinkCreated || !slices.Equal(rem, []string{"Central de software"}) {
		t.Fatalf("renomear: %s %v %v", st, rem, err)
	}
	if got := lnks(t, desk); !slices.Equal(got, []string{"Loja da TI.lnk", "Outro programa.lnk", "nota.txt"}) {
		t.Errorf("pasta: %v", got)
	}
	// Um .lnk com o nome da Central que aponta para outro lugar: trocado pelo da Central.
	_ = os.WriteFile(filepath.Join(desk, "Loja da TI.lnk"), []byte(`C:\Outro\x.exe|a|b`), 0o600)
	if st, _, err := l.Sync("Loja da TI"); err != nil || st != AppLinkCreated {
		t.Errorf("tomado: %s %v", st, err)
	}
	// Nome que não vale como arquivo: recusado, nada muda.
	if _, _, err := l.Sync(`Loja: TI?`); err == nil {
		t.Error("nome inválido aceito")
	}
	// Remoção: só os da Central.
	rem, err = l.Remove()
	if err != nil || !slices.Equal(rem, []string{"Loja da TI"}) {
		t.Fatalf("remover: %v %v", rem, err)
	}
	if got := lnks(t, desk); !slices.Equal(got, []string{"Outro programa.lnk", "nota.txt"}) {
		t.Errorf("depois de remover: %v", got)
	}
	// O gravador falhou: nada fica pela metade na pasta.
	l.Write = func(string, string, string, string) error { return errors.New("COM indisponível") }
	if _, _, err := l.Sync("Central"); err == nil || len(lnks(t, desk)) != 2 {
		t.Errorf("gravador falhou: %v %v", err, lnks(t, desk))
	}
	// Pasta que não existe: erro, sem criar.
	l.Desktop = filepath.Join(desk, "nao-existe")
	if _, _, err := l.Sync("Central"); err == nil {
		t.Error("pasta inexistente aceita")
	}
}
