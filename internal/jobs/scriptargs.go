package jobs

import (
	"errors"
	"strings"
	"unicode"
)

// SplitScriptArgs agrupa valores entre aspas simples ou duplas. Barras invertidas são
// literais, inclusive antes de aspas: caminhos Windows não viram escapes de shell.
// O resultado ainda deve ser validado para o runner escolhido.
func SplitScriptArgs(line string) ([]string, error) {
	if len(line) > 65536 {
		return nil, errors.New("linha de parâmetros muito grande")
	}
	var args []string
	var word strings.Builder
	var quote rune
	started := false
	for _, r := range line {
		switch {
		case unicode.IsControl(r) && r != '\t':
			return nil, errors.New("parâmetros com caracteres de controle")
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				word.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, started = r, true
		case unicode.IsSpace(r):
			if started {
				args = append(args, word.String())
				word.Reset()
				started = false
			}
		default:
			word.WriteRune(r)
			started = true
		}
		if len(args) > MaxScriptArgs {
			return nil, errors.New("mais de 32 argumentos")
		}
	}
	if quote != 0 {
		return nil, errors.New("feche as aspas dos parâmetros")
	}
	if started {
		args = append(args, word.String())
	}
	if err := ValidateScriptArgs("", args); err != nil {
		return nil, err
	}
	return args, nil
}

// FormatScriptArgs é o inverso de SplitScriptArgs, inclusive para argumentos vazios,
// aspas literais e caminhos com espaços. É usado para editar os valores salvos.
func FormatScriptArgs(args []string) string {
	var words []string
	for _, a := range args {
		words = append(words, "'"+strings.ReplaceAll(a, "'", "'\"'\"'")+"'")
	}
	return strings.Join(words, " ")
}
