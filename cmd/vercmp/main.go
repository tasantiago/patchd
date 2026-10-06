// Comando vercmp: compara pares de versões com as regras do patchd (internal/version), para
// conferir o resultado contra as ferramentas de referência (dpkg --compare-versions, rpm).
// Ferramenta de desenvolvimento e laboratório; não vai para as máquinas nem para o servidor.
//
// Uso: vercmp -scheme dpkg|rpm|dotted < pares.txt
//
// Cada linha da entrada tem duas versões separadas por espaço ou TAB. Cada linha da saída
// repete o par e acrescenta "<", "=" ou ">" (a comparada com b). Uma versão inválida sai
// com "!" e a mensagem vai para a saída de erro; o código de saída é 1 se houver alguma.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/tasantiago/patchd/internal/version"
)

func main() {
	scheme := flag.String("scheme", "", "regras de comparação: dpkg, rpm ou dotted")
	flag.Parse()
	if flag.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "vercmp: os pares vêm pela entrada padrão")
		os.Exit(2)
	}
	bad, err := run(version.Scheme(*scheme), os.Stdin, os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vercmp: %v\n", err)
		os.Exit(2)
	}
	if bad > 0 {
		os.Exit(1)
	}
}

// run compara os pares da entrada e devolve quantos tinham versão inválida.
func run(s version.Scheme, in io.Reader, out, errOut io.Writer) (bad int, err error) {
	if _, err := version.Compare(s, "1", "1"); err != nil {
		return 0, err
	}
	w := bufio.NewWriter(out)
	defer w.Flush()
	sc := bufio.NewScanner(in)
	n := 0
	for sc.Scan() {
		n++
		f := strings.Fields(sc.Text())
		if len(f) == 0 {
			continue
		}
		if len(f) != 2 {
			return bad, fmt.Errorf("linha %d: esperadas duas versões, vieram %d", n, len(f))
		}
		c, err := version.Compare(s, f[0], f[1])
		sym := [...]string{"<", "=", ">"}[c+1]
		if err != nil {
			bad++
			sym = "!"
			fmt.Fprintf(errOut, "linha %d: %v\n", n, err)
		}
		fmt.Fprintf(w, "%s %s %s\n", f[0], f[1], sym)
	}
	return bad, sc.Err()
}
