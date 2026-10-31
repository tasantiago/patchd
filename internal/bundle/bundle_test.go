package bundle

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// zipa monta um ZIP na memória. nome → conteúdo; nome terminado em "/" vira pasta.
func zipa(t *testing.T, arquivos map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for nome, conteudo := range arquivos {
		if strings.HasSuffix(nome, "/") {
			if _, err := zw.Create(nome); err != nil {
				t.Fatal(err)
			}
			continue
		}
		w, err := zw.Create(nome)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(conteudo)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestInspect(t *testing.T) {
	b := zipa(t, map[string]string{
		"instalar.ps1":  "Write-Host ok",
		"config.ini":    "[geral]",
		"bin/setup.exe": "MZ",
		"bin/":          "",
	})
	info, err := Inspect(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Files) != 3 || info.Files[0].Name != "bin/setup.exe" || info.Files[1].Name != "config.ini" {
		t.Fatalf("arquivos: %+v", info.Files)
	}
	// Os pontos de entrada são os executáveis, em ordem; o .ini fica de fora.
	if strings.Join(info.Entries, ",") != "bin/setup.exe,instalar.ps1" {
		t.Errorf("pontos de entrada: %v", info.Entries)
	}
	if info.OS != OSWindows {
		t.Errorf("SO: %q", info.OS)
	}
	if info.Total != int64(len("Write-Host ok")+len("[geral]")+len("MZ")) {
		t.Errorf("total: %d", info.Total)
	}
}

func TestInspectSO(t *testing.T) {
	b := zipa(t, map[string]string{"rodar.sh": "#!/bin/sh\n", "dados.ini": "x"})
	info, err := Inspect(bytes.NewReader(b), int64(len(b)))
	if err != nil || info.OS != OSUnix {
		t.Fatalf("unix: %q %v", info.OS, err)
	}
	// Com os dois lados, o bundle serve aos dois e o SO fica vazio.
	b = zipa(t, map[string]string{"rodar.sh": "#!/bin/sh\n", "rodar.ps1": "x"})
	info, err = Inspect(bytes.NewReader(b), int64(len(b)))
	if err != nil || info.OS != "" {
		t.Fatalf("os dois: %q %v", info.OS, err)
	}
}

func TestInspectRecusa(t *testing.T) {
	casos := map[string]map[string]string{
		"sem nada":             {},
		"sem ponto de entrada": {"leiame.txt": "x", "config.ini": "y"},
		"caminho para fora":    {"../fora.ps1": "x"},
		"caminho absoluto":     {"/etc/cron.d/x.sh": "x"},
		"barra invertida":      {`..\..\Windows\System32\evil.ps1`: "x"},
		"unidade do Windows":   {`C:/Windows/x.ps1`: "x"},
		"ponto ponto no meio":  {"bin/../../x.sh": "x"},
		"fundo demais":         {"a/b/c/d/e/f.sh": "x"},
	}
	for nome, arq := range casos {
		b := zipa(t, arq)
		if _, err := Inspect(bytes.NewReader(b), int64(len(b))); err == nil {
			t.Errorf("%s: devia recusar", nome)
		}
	}
	// Não é ZIP.
	lixo := []byte("isto não é um zip")
	if _, err := Inspect(bytes.NewReader(lixo), int64(len(lixo))); err == nil {
		t.Error("lixo devia recusar")
	}
}

func TestValidEntry(t *testing.T) {
	bons := []string{"a.ps1", "bin/setup.exe", "um dois/tres.sh", "a/b/c/d.sh", "x-1_2+3.ini"}
	for _, n := range bons {
		if !ValidEntry(n) {
			t.Errorf("%q devia passar", n)
		}
	}
	ruins := []string{"", "..", "../x", "/x", `a\b`, "C:/x", "a//b", "a/./b", "a/b/c/d/e/f", " x", "x ", "x.", strings.Repeat("a", 130)}
	for _, n := range ruins {
		if ValidEntry(n) {
			t.Errorf("%q devia recusar", n)
		}
	}
}

func TestRunnerFor(t *testing.T) {
	casos := map[string]string{"a.ps1": RunnerPowerShell, "a.PS1": RunnerPowerShell, "a.bat": RunnerCmd, "a.cmd": RunnerCmd,
		"a.exe": RunnerExe, "a.sh": RunnerBash, "a.ini": "", "a": "", "a.txt": ""}
	for nome, quer := range casos {
		if got := RunnerFor(nome); got != quer {
			t.Errorf("%s: %q, queria %q", nome, got, quer)
		}
	}
	if OSForRunner(RunnerBash) != OSUnix || OSForRunner(RunnerCmd) != OSWindows || OSForRunner("x") != "" {
		t.Error("OSForRunner")
	}
}

func TestExtract(t *testing.T) {
	b := zipa(t, map[string]string{"instalar.sh": "#!/bin/sh\necho ok\n", "config.ini": "[geral]\n", "bin/dados.bin": "\x00\x01"})
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "b.zip")
	if err := os.WriteFile(zipPath, b, 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "saida")
	n, err := Extract(zipPath, dest)
	if err != nil || n != 3 {
		t.Fatalf("extrair: %d %v", n, err)
	}
	if c, err := os.ReadFile(filepath.Join(dest, "config.ini")); err != nil || string(c) != "[geral]\n" {
		t.Errorf("config.ini: %q %v", c, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "bin", "dados.bin")); err != nil {
		t.Errorf("subpasta: %v", err)
	}
	// O script fica executável; o resto, não. Windows usa ACLs, não estes bits Unix.
	st, err := os.Stat(filepath.Join(dest, "instalar.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0o700 {
		t.Errorf("modo do script: %v", st.Mode().Perm())
	}
	st, err = os.Stat(filepath.Join(dest, "config.ini"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Errorf("modo do .ini: %v", st.Mode().Perm())
	}
	// Nada escapou da pasta.
	if _, err := os.Stat(filepath.Join(dir, "fora.ps1")); err == nil {
		t.Error("arquivo fora da pasta")
	}
}

func TestExtractRecusaZipSlip(t *testing.T) {
	// Um ZIP com nome de saída da pasta nem chega a escrever: o Inspect recusa antes.
	b := zipa(t, map[string]string{"../fora.sh": "x", "ok.sh": "y"})
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "b.zip")
	if err := os.WriteFile(zipPath, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if n, err := Extract(zipPath, filepath.Join(dir, "saida")); err == nil {
		t.Fatalf("devia recusar; gravou %d", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "fora.sh")); err == nil {
		t.Fatal("o Zip Slip escreveu fora da pasta")
	}
}
