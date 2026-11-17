package shortcuts

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/tasantiago/patchd/internal/protocol"
)

// Os atalhos da área de trabalho pública: criados quando o executável existe, refeitos quando
// a regra muda, apagados quando o programa sai ou a regra deixa de pedir; o que alguém alterou
// fica.
func TestAtalhosDaAreaDeTrabalhoPublica(t *testing.T) {
	root := t.TempDir()
	desktop := filepath.Join(root, "Public", "Desktop")
	progs := filepath.Join(root, "progs")
	for _, d := range []string{desktop, progs} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	disco := map[string]string{ // o caminho do Windows → o arquivo de teste
		`C:\IRPF\irpf.exe`: filepath.Join(progs, "irpf.exe"),
		`C:\IRPF\irpf.ico`: filepath.Join(progs, "irpf.ico"),
		`C:\Outro\o.exe`:   filepath.Join(progs, "o.exe"),
	}
	instala := func(win string) {
		if err := os.WriteFile(disco[win], []byte("MZ"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	escritas := 0
	m := &PublicManager{Desktop: desktop, StatePath: filepath.Join(root, "dados", "atalhos-publicos.json"),
		Expand: func(p string) (string, error) {
			if v, ok := disco[p]; ok {
				return v, nil
			}
			return "", errors.New("fora do teste")
		},
		Write: func(path string, s protocol.DesktopShortcut, target, icon string, index int) error {
			escritas++
			return os.WriteFile(path, []byte(fmt.Sprintf("%s|%s|%s|%d", s.Name, target, icon, index)), 0o644)
		}}
	irpf := protocol.DesktopShortcut{App: "irpf-2026", Name: "IRPF 2026", Target: `C:\IRPF\irpf.exe`, Icon: `C:\IRPF\irpf.ico,1`}
	outro := protocol.DesktopShortcut{App: "outro", Name: "Outro", Target: `C:\Outro\o.exe`}
	lista := protocol.DesktopShortcuts{Items: []protocol.DesktopShortcut{irpf, outro}}
	estados := func(res []PublicResult) map[string]string {
		out := map[string]string{}
		for _, r := range res {
			out[r.App] = r.State
		}
		return out
	}
	aplica := func() map[string]string {
		t.Helper()
		res, err := m.Apply(lista)
		if err != nil {
			t.Fatal(err)
		}
		return estados(res)
	}
	lnk := func(nome string) string { return filepath.Join(desktop, nome+".lnk") }

	// Nada instalado: nenhum atalho.
	if e := aplica(); e["irpf-2026"] != PublicNoProgram || e["outro"] != PublicNoProgram || escritas != 0 {
		t.Fatalf("sem programa: %v", e)
	}
	// O IRPF instalado (o ícone ainda não): o atalho com o ícone do executável. Um arquivo do
	// instalador com o mesmo nome é trocado pelo da regra.
	instala(`C:\IRPF\irpf.exe`)
	_ = os.WriteFile(lnk("IRPF 2026"), []byte("do instalador"), 0o644)
	if e := aplica(); e["irpf-2026"] != PublicCreated {
		t.Fatalf("instalado: %v", e)
	}
	if b, _ := os.ReadFile(lnk("IRPF 2026")); string(b) != "IRPF 2026|"+disco[`C:\IRPF\irpf.exe`]+"||0" {
		t.Errorf("atalho: %q", b)
	}
	// De novo, sem mudança: em dia, sem escrever.
	antes := escritas
	if e := aplica(); e["irpf-2026"] != PublicCurrent || escritas != antes {
		t.Errorf("em dia: %v (%d escritas)", e, escritas-antes)
	}
	// A regra muda o nome: o novo é criado e o antigo, apagado.
	irpf.Name = "Imposto de Renda"
	lista.Items[0] = irpf
	instala(`C:\IRPF\irpf.ico`)
	if e := aplica(); e["irpf-2026"] != PublicCreated {
		t.Fatalf("renomeado: %v", e)
	}
	if _, err := os.Stat(lnk("IRPF 2026")); !os.IsNotExist(err) {
		t.Error("o atalho com o nome antigo ficou")
	}
	if b, _ := os.ReadFile(lnk("Imposto de Renda")); string(b) != "Imposto de Renda|"+disco[`C:\IRPF\irpf.exe`]+"|"+disco[`C:\IRPF\irpf.ico`]+"|1" {
		t.Errorf("atalho com ícone: %q", b)
	}
	// Apagado pelo usuário: volta.
	_ = os.Remove(lnk("Imposto de Renda"))
	if e := aplica(); e["irpf-2026"] != PublicCreated {
		t.Errorf("recriado: %v", e)
	}
	// O outro programa instalado; alguém altera o atalho dele; a regra deixa de pedir: fica.
	instala(`C:\Outro\o.exe`)
	aplica()
	_ = os.WriteFile(lnk("Outro"), []byte("mexido"), 0o644)
	lista.Items = lista.Items[:1]
	if e := aplica(); e["outro"] != PublicKept {
		t.Errorf("alterado: %v", e)
	}
	if _, err := os.Stat(lnk("Outro")); err != nil {
		t.Error("o atalho alterado foi apagado")
	}
	// O programa sai: o atalho do patchd sai junto.
	_ = os.Remove(disco[`C:\IRPF\irpf.exe`])
	if e := aplica(); e["irpf-2026"] != PublicRemoved {
		t.Errorf("desinstalado: %v", e)
	}
	if _, err := os.Stat(lnk("Imposto de Renda")); !os.IsNotExist(err) {
		t.Error("o atalho ficou sem o programa")
	}
	// Uma lista que não confere não muda nada.
	if _, err := m.Apply(protocol.DesktopShortcuts{Items: []protocol.DesktopShortcut{{App: "x", Name: "X", Target: `\\rede\x.exe`}}}); err == nil {
		t.Error("lista inválida aceita")
	}
	if left, _ := filepath.Glob(filepath.Join(desktop, ".patchd-*")); len(left) != 0 {
		t.Errorf("temporários na área de trabalho: %v", left)
	}
}
