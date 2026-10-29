package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/bundle"
)

type contratoBundles interface {
	AddScriptBundle(ctx context.Context, b ScriptBundle) (int64, error)
	ScriptBundles(ctx context.Context) ([]ScriptBundle, error)
	ScriptBundle(ctx context.Context, id int64) (ScriptBundle, bool, error)
	DeleteScriptBundle(ctx context.Context, id int64) (ScriptBundle, bool, error)
}

// Aula 23.8: cadastro dos bundles, o mesmo ZIP recusado duas vezes, e a lista de arquivos
// (que a tela usa para o seletor de ponto de entrada) voltando inteira.
func testarBundles(t *testing.T, st contratoBundles) {
	ctx := context.Background()
	if l, err := st.ScriptBundles(ctx); err != nil || len(l) != 0 {
		t.Fatalf("vazio: %v %v", l, err)
	}
	b := ScriptBundle{Name: "instala-exemplo", SHA256: strings.Repeat("a", 64), Size: 4096, OS: "windows",
		Note: "pacote de exemplo", UploadedBy: "thyago",
		Files: []bundle.Entry{{Name: "instalar.ps1", Size: 120}, {Name: "config.ini", Size: 40}, {Name: "bin/setup.exe", Size: 3000}}}
	id, err := st.AddScriptBundle(ctx, b)
	if err != nil || id == 0 {
		t.Fatal(err)
	}
	if _, err := st.AddScriptBundle(ctx, b); !errors.Is(err, ErrBundleExists) {
		t.Fatalf("mesmo ZIP: %v", err)
	}
	// Outro ZIP, mesmo nome: pode.
	b2 := b
	b2.SHA256 = strings.Repeat("b", 64)
	id2, err := st.AddScriptBundle(ctx, b2)
	if err != nil || id2 == id {
		t.Fatalf("segundo: %d %v", id2, err)
	}
	lido, ok, err := st.ScriptBundle(ctx, id)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if lido.Name != "instala-exemplo" || lido.Size != 4096 || lido.OS != "windows" || lido.UploadedBy != "thyago" || lido.UploadedAt.IsZero() {
		t.Errorf("metadados: %+v", lido)
	}
	if len(lido.Files) != 3 || lido.Files[0].Name != "instalar.ps1" || lido.Files[2].Size != 3000 {
		t.Errorf("arquivos: %+v", lido.Files)
	}
	// Só .ps1 e .exe servem de ponto de entrada; o .ini não.
	if e := lido.Entries(); len(e) != 2 || e[0] != "instalar.ps1" || e[1] != "bin/setup.exe" {
		t.Errorf("pontos de entrada: %v", e)
	}
	if l, _ := st.ScriptBundles(ctx); len(l) != 2 || l[0].ID != id2 { // mais novo primeiro
		t.Errorf("lista: %+v", l)
	}
	if _, ok, _ := st.ScriptBundle(ctx, 9999); ok {
		t.Error("inexistente devia dar não encontrado")
	}
	removido, ok, err := st.DeleteScriptBundle(ctx, id)
	if err != nil || !ok || removido.SHA256 != b.SHA256 {
		t.Fatalf("remover: %+v %v %v", removido, ok, err)
	}
	if _, ok, _ := st.DeleteScriptBundle(ctx, id); ok {
		t.Error("remover duas vezes")
	}
	if l, _ := st.ScriptBundles(ctx); len(l) != 1 {
		t.Errorf("depois de remover: %+v", l)
	}
}

func TestBundlesMemoria(t *testing.T) { testarBundles(t, NewMemory()) }

func TestBundlesPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarBundles(t, NewPostgres(pool))
}
