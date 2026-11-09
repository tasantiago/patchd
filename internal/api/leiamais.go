package api

import (
	"strings"
	"unicode/utf8"
)

// Texto longo nas tabelas (o detalhe de uma campanha pausada lista centenas de jobs): mostra o
// começo e esconde o resto num <details> "leia mais", sem JavaScript (o painel não tem).

// leiaMaisCurto: até onde o texto aparece inteiro.
const leiaMaisCurto = 220

type leiaMaisView struct {
	Short, Full string
	Long        bool
}

// leiaMais corta o texto num limite de palavra (ou de frase, se houver uma perto), com "…".
func leiaMais(s string) leiaMaisView {
	if utf8.RuneCountInString(s) <= leiaMaisCurto+40 { // um pouco acima do limite: não vale esconder
		return leiaMaisView{Full: s}
	}
	cut := 0
	for i := range s {
		if utf8.RuneCountInString(s[:i]) >= leiaMaisCurto {
			cut = i
			break
		}
	}
	head := s[:cut]
	switch i := strings.LastIndexAny(head, ".;"); {
	case i >= leiaMaisCurto/2:
		head = head[:i+1]
	default:
		if j := strings.LastIndexByte(head, ' '); j >= leiaMaisCurto/2 {
			head = head[:j]
		}
	}
	return leiaMaisView{Short: strings.TrimRight(head, " ,") + " …", Full: s, Long: true}
}
