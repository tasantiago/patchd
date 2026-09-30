package config

import (
	"flag"
	"fmt"
	"strings"
)

// UsageError indica um erro de linha de comando que o pacote flag já mostrou
// ao usuário (mensagem seguida da ajuda). Quem recebe deve apenas sair com
// código 2, sem imprimir a mensagem de novo.
type UsageError struct {
	Err error
}

func (e *UsageError) Error() string { return e.Err.Error() }

// Unwrap permite errors.Is(err, flag.ErrHelp) através do UsageError.
func (e *UsageError) Unwrap() error { return e.Err }

// typeNames traduz os nomes de tipo que o pacote flag mostra na ajuda.
var typeNames = map[string]string{
	"string":   "texto",
	"duration": "duração",
	"int":      "número",
	"uint":     "número",
	"float":    "número",
	"value":    "valor",
}

// SetUsage troca a ajuda padrão do pacote flag (em inglês) por uma em português.
func SetUsage(fs *flag.FlagSet) {
	fs.Usage = func() {
		w := fs.Output()
		fmt.Fprintf(w, "Uso de %s:\n", fs.Name())
		fs.VisitAll(func(f *flag.Flag) {
			typeName, usage := flag.UnquoteUsage(f)
			if pt, ok := typeNames[typeName]; ok {
				typeName = pt
			}

			line := "  -" + f.Name
			if typeName != "" {
				line += " " + typeName
			}
			fmt.Fprintln(w, line)

			fmt.Fprintf(w, "    \t%s", strings.ReplaceAll(usage, "\n", "\n    \t"))
			// Flags booleanas desligadas por padrão não precisam mostrar o padrão.
			if f.DefValue != "" && f.DefValue != "false" {
				if typeName == "texto" {
					fmt.Fprintf(w, " (padrão %q)", f.DefValue)
				} else {
					fmt.Fprintf(w, " (padrão %s)", f.DefValue)
				}
			}
			fmt.Fprintln(w)
		})
	}
}
