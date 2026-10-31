// Package bundle lê e extrai com segurança o ZIP de um conjunto de scripts (Aula 23.8).
//
// O bundle é o que o admin sobe no painel: um executável, um .ini, e o script que começa a
// execução e chama os outros arquivos. O servidor confere o ZIP na subida; o agente o extrai
// na máquina. Os dois usam as mesmas regras, deste pacote.
//
// Um ZIP é um formato hostil: um nome de arquivo dentro dele pode ser "../../etc/cron.d/x",
// "C:\Windows\System32\x.dll" ou um link simbólico para fora da pasta. Extrair sem conferir
// é o Zip Slip. Aqui, todo nome é conferido antes de qualquer escrita: relativo, sem "..",
// sem barra invertida, só caracteres previsíveis, no máximo MaxDepth pastas — e nada que não
// seja arquivo ou pasta comum (link simbólico e dispositivo ficam de fora). Os tetos de
// quantidade e de tamanho descompactado fecham o zip bomb.
package bundle

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Limites do bundle.
const (
	MaxZipBytes   = 256 << 20 // o ZIP em si
	MaxTotalBytes = 512 << 20 // soma descompactada
	MaxFileBytes  = 256 << 20 // um arquivo descompactado
	MaxFiles      = 512
	MaxDepth      = 4   // pastas dentro do ZIP
	MaxSegment    = 120 // letras de cada parte do caminho
	MaxScriptBytes = 16 << 20 // script avulso; instaladores maiores devem vir no ZIP
	maxRatio      = 200 // descompactado / compactado, por arquivo: acima disso é zip bomb
	ratioFloor    = 1 << 20
)

// Runners: como o ponto de entrada é executado. Nenhum deles recebe linha de comando: o
// agente passa o arquivo e os argumentos como argv, nunca por um shell.
const (
	RunnerPowerShell = "powershell" // .ps1
	RunnerCmd        = "cmd"        // .bat, .cmd
	RunnerExe        = "exe"        // .exe
	RunnerBash       = "bash"       // .sh
	RunnerVBScript   = "cscript"    // .vbs (Windows Script Host)
)

// Sistemas a que um runner serve.
const (
	OSWindows = "windows"
	OSUnix    = "unix" // Linux e macOS
)

// Entry é um arquivo dentro do bundle.
type Entry struct {
	Name string `json:"name"` // caminho relativo, com barras normais
	Size int64  `json:"size"` // descompactado
}

// Info é o que o servidor guarda de um bundle conferido.
type Info struct {
	Files   []Entry  // só arquivos (pastas não entram)
	Total   int64    // soma descompactada
	Entries []string // os candidatos a ponto de entrada, em ordem
	OS      string   // OSWindows, OSUnix, ou "" quando serve aos dois
}

// Erros.
var (
	ErrEmpty   = errors.New("o ZIP não tem nenhum arquivo")
	ErrNoEntry = errors.New("o ZIP não tem nenhum script ou executável que sirva de ponto de entrada (.ps1, .bat, .cmd, .vbs, .exe ou .sh)")
)

// RunnerFor devolve o runner do ponto de entrada pela extensão ("" se não serve).
func RunnerFor(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".ps1":
		return RunnerPowerShell
	case ".bat", ".cmd":
		return RunnerCmd
	case ".exe":
		return RunnerExe
	case ".sh":
		return RunnerBash
	case ".vbs":
		return RunnerVBScript
	}
	return ""
}

// OSForRunner: o sistema em que o runner existe.
func OSForRunner(r string) string {
	switch r {
	case RunnerPowerShell, RunnerCmd, RunnerExe, RunnerVBScript:
		return OSWindows
	case RunnerBash:
		return OSUnix
	}
	return ""
}

// ValidEntry confere o nome de um arquivo dentro do ZIP: relativo, com barras normais, sem
// "..", sem barra invertida, sem dois pontos (unidade do Windows), até MaxDepth pastas.
func ValidEntry(name string) bool {
	if name == "" || len(name) > (MaxSegment+1)*(MaxDepth+1) {
		return false
	}
	if strings.ContainsAny(name, `\:`) || strings.HasPrefix(name, "/") {
		return false
	}
	parts := strings.Split(name, "/")
	if len(parts) > MaxDepth+1 {
		return false
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || len(p) > MaxSegment {
			return false
		}
		if strings.HasPrefix(p, " ") || strings.HasSuffix(p, " ") || strings.HasSuffix(p, ".") {
			return false // nomes que o Windows trata de outro jeito
		}
		for _, r := range p {
			ok := r == '-' || r == '_' || r == '.' || r == ' ' || r == '+' ||
				(r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')
			if !ok {
				return false
			}
		}
	}
	return true
}

// Inspect confere o ZIP inteiro e descreve o que há dentro, sem extrair nada.
func Inspect(r io.ReaderAt, size int64) (Info, error) {
	var info Info
	if size <= 0 || size > MaxZipBytes {
		return info, fmt.Errorf("o ZIP tem %d bytes (o limite é %d MiB)", size, MaxZipBytes>>20)
	}
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return info, fmt.Errorf("não é um ZIP válido: %w", err)
	}
	if len(zr.File) > MaxFiles {
		return info, fmt.Errorf("o ZIP tem %d itens (o limite é %d)", len(zr.File), MaxFiles)
	}
	for _, f := range zr.File {
		name := f.Name
		if strings.HasSuffix(name, "/") { // pasta: confere o nome e segue
			if !ValidEntry(strings.TrimSuffix(name, "/")) {
				return info, fmt.Errorf("pasta %q não é aceita dentro do ZIP", name)
			}
			continue
		}
		if !ValidEntry(name) {
			return info, fmt.Errorf("arquivo %q não é aceito dentro do ZIP (use caminhos relativos simples, sem \"..\")", name)
		}
		mode := f.Mode()
		if !mode.IsRegular() {
			return info, fmt.Errorf("%q não é um arquivo comum (link simbólico ou dispositivo não entram no bundle)", name)
		}
		us := int64(f.UncompressedSize64) // #nosec G115 -- conferido logo abaixo contra MaxFileBytes
		if f.UncompressedSize64 > MaxFileBytes {
			return info, fmt.Errorf("%q tem %d bytes descompactado (o limite por arquivo é %d MiB)", name, f.UncompressedSize64, MaxFileBytes>>20)
		}
		if cs := f.CompressedSize64; cs > 0 && f.UncompressedSize64 > ratioFloor && f.UncompressedSize64/cs > maxRatio {
			return info, fmt.Errorf("%q descompacta %dx (parece um zip bomb)", name, f.UncompressedSize64/cs)
		}
		info.Total += us
		if info.Total > MaxTotalBytes {
			return info, fmt.Errorf("o ZIP passa de %d MiB descompactado", MaxTotalBytes>>20)
		}
		info.Files = append(info.Files, Entry{Name: name, Size: us})
	}
	if len(info.Files) == 0 {
		return info, ErrEmpty
	}
	sort.Slice(info.Files, func(i, j int) bool { return info.Files[i].Name < info.Files[j].Name })

	win, unix := false, false
	for _, f := range info.Files {
		switch r := RunnerFor(f.Name); OSForRunner(r) {
		case OSWindows:
			win = true
			info.Entries = append(info.Entries, f.Name)
		case OSUnix:
			unix = true
			info.Entries = append(info.Entries, f.Name)
		}
	}
	if len(info.Entries) == 0 {
		return info, ErrNoEntry
	}
	switch {
	case win && !unix:
		info.OS = OSWindows
	case unix && !win:
		info.OS = OSUnix
	}
	return info, nil
}

// Extract abre o ZIP e grava o conteúdo em dest, que precisa estar vazio. Devolve quantos
// arquivos gravou. As mesmas conferências do Inspect valem aqui: o agente não confia no que
// o servidor conferiu, confere de novo antes de escrever.
func Extract(zipPath, dest string) (int, error) {
	zf, err := os.Open(zipPath) // #nosec G304 -- caminho montado pelo agente, na pasta de dados dele
	if err != nil {
		return 0, err
	}
	defer zf.Close()
	st, err := zf.Stat()
	if err != nil {
		return 0, err
	}
	info, err := Inspect(zf, st.Size())
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return 0, err
	}
	zr, err := zip.NewReader(zf, st.Size())
	if err != nil {
		return 0, err
	}
	root, err := filepath.Abs(dest)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, "/") {
			continue // as pastas saem do caminho dos arquivos
		}
		if !ValidEntry(f.Name) || !f.Mode().IsRegular() {
			return n, fmt.Errorf("arquivo %q não é aceito dentro do ZIP", f.Name)
		}
		out := filepath.Join(root, filepath.FromSlash(f.Name))
		// Cinto e suspensório: mesmo com o nome conferido, o destino tem de ficar na pasta.
		if out != root && !strings.HasPrefix(out, root+string(os.PathSeparator)) {
			return n, fmt.Errorf("arquivo %q sairia da pasta do bundle", f.Name)
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
			return n, err
		}
		if err := writeOne(f, out); err != nil {
			return n, err
		}
		n++
	}
	_ = info
	return n, nil
}

func writeOne(f *zip.File, out string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	// O modo é do agente, não do ZIP: 0700 nos executáveis e scripts, 0600 no resto. Um
	// bundle não concede permissão a mais ninguém na máquina.
	mode := os.FileMode(0o600)
	if RunnerFor(f.Name) != "" {
		mode = 0o700
	}
	w, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	// LimitReader fecha o caso de o cabeçalho mentir sobre o tamanho.
	if _, err := io.Copy(w, io.LimitReader(rc, MaxFileBytes+1)); err != nil {
		_ = w.Close()
		return err
	}
	return w.Close()
}
