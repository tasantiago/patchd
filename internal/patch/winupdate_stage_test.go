package patch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// O CopyToCache recebe os arquivos com o nome original (o último trecho do endereço), não o
// nome do cache do agente; numa pasta nova, apagada no fim.
func TestArquivosComONomeOriginal(t *testing.T) {
	dir := t.TempDir()
	const wim = "edition_common_b01aaa34b66aca8fb45dd8fc6c1381c8579a9eaa.wim"
	a := filepath.Join(dir, "282792b491efbfb9-"+wim)
	b := filepath.Join(dir, "1111222233334444-windows11.0-kb5070001-x64_abc.msu")
	for _, f := range []string{a, b} {
		if err := os.WriteFile(f, []byte(filepath.Base(f)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	parts := []cachePart{
		{URLs: []string{"http://dl.delivery.mp.microsoft.com/filestreamingservice/files/x/" + wim + "?P1=1", "http://download.windowsupdate.com/d/msdownload/update/windows11.0-kb5070001-x64_abc.msu"}},
		{URLs: []string{"http://dl.delivery.mp.microsoft.com/filestreamingservice/files/x/" + wim + "?P1=1"}},
		{},
	}
	staged, cleanup, err := stageFiles(dir, parts, [][]string{{a, b}, {a}, nil})
	if err != nil {
		t.Fatal(err)
	}
	stage := filepath.Dir(staged[0][0])
	if filepath.Dir(stage) != dir || !strings.HasPrefix(filepath.Base(stage), "entrega-") {
		t.Errorf("pasta de entrega: %s", stage)
	}
	if filepath.Base(staged[0][0]) != wim || filepath.Base(staged[0][1]) != "windows11.0-kb5070001-x64_abc.msu" || staged[1][0] != staged[0][0] || len(staged[2]) != 0 {
		t.Errorf("nomes: %v", staged)
	}
	if got, _ := os.ReadFile(staged[0][0]); string(got) != filepath.Base(a) {
		t.Errorf("conteúdo: %q", got)
	}
	cleanup()
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Error("a pasta de entrega ficou")
	}
	if _, err := os.Stat(a); err != nil {
		t.Error("o arquivo do cache sumiu junto")
	}

	// Nome que viraria caminho, e dois arquivos diferentes com o mesmo nome: recusados, sem
	// deixar pasta para trás.
	for _, c := range []struct {
		parts []cachePart
		files [][]string
	}{
		{[]cachePart{{URLs: []string{"http://d/x/..%5C..%5Cwindows%5Cevil.dll"}}}, [][]string{{a}}},
		{[]cachePart{{URLs: []string{"http://d/"}}}, [][]string{{a}}},
		{[]cachePart{{URLs: []string{"http://d/1/a.cab", "http://d/2/A.cab"}}}, [][]string{{a, b}}},
		{[]cachePart{{URLs: []string{"http://d/a.cab"}}}, [][]string{{a, b}}},
	} {
		if _, _, err := stageFiles(dir, c.parts, c.files); err == nil {
			t.Errorf("aceito: %v", c.parts)
		}
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "entrega-*")); len(m) != 0 {
		t.Errorf("pastas de entrega esquecidas: %v", m)
	}
}
