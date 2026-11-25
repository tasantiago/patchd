// Package texto prepara o texto que vem de fora (agente, URL, provedor) para a tela e para
// o PostgreSQL (Aula 20.3). O banco recusa dois tipos de texto que o Go aceita sem
// reclamar: bytes que não formam UTF-8 e o caractere NUL. Recusado, o INSERT falha, a
// requisição vira um 500 e o dado se perde (um inventário, o resultado de um job). Os dois
// viram U+FFFD (�), a mesma troca que o encoding/json já faz com UTF-8 inválido.
package texto

import (
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"unicode"
	"unicode/utf8"
)

// Clean devolve s com UTF-8 inválido e NUL trocados por U+FFFD.
func Clean(s string) string {
	if utf8.ValidString(s) && !strings.ContainsRune(s, 0) {
		return s
	}
	return strings.ReplaceAll(strings.ToValidUTF8(s, "�"), "\x00", "�")
}

// Clip limpa s (Clean) e corta em no máximo n bytes sem partir um caractere: s[:n] pode
// cortar o "ç" ao meio, e meio "ç" é UTF-8 inválido.
func Clip(s string, n int) string {
	s = Clean(s)
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// CleanAll aplica o Clean a todo texto alcançável a partir de v (ponteiro para struct,
// slice, mapa ou interface, como o que o json.Decoder preenche) e diz quantos mudaram.
// Campos não exportados e []byte ficam como estão.
func CleanAll(v any) int {
	return walk(reflect.ValueOf(v))
}

func walk(v reflect.Value) int {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return 0
		}
		return walk(v.Elem())
	case reflect.Interface:
		if v.IsNil() || !v.CanSet() {
			return 0
		}
		// O valor dentro da interface não é endereçável: limpa uma cópia e devolve.
		c := reflect.New(v.Elem().Type()).Elem()
		c.Set(v.Elem())
		n := walk(c)
		if n > 0 {
			v.Set(c)
		}
		return n
	case reflect.String:
		s := v.String()
		if c := Clean(s); c != s && v.CanSet() {
			v.SetString(c)
			return 1
		}
	case reflect.Struct:
		n := 0
		for i := range v.NumField() {
			if f := v.Field(i); f.CanSet() {
				n += walk(f)
			}
		}
		return n
	case reflect.Slice, reflect.Array:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return 0
		}
		n := 0
		for i := range v.Len() {
			n += walk(v.Index(i))
		}
		return n
	case reflect.Map:
		if v.IsNil() {
			return 0
		}
		n := 0
		for _, k := range v.MapKeys() {
			// Chave e valor de um mapa também não são endereçáveis: cópias.
			nk := reflect.New(k.Type()).Elem()
			nk.Set(k)
			nv := reflect.New(v.Type().Elem()).Elem()
			nv.Set(v.MapIndex(k))
			m := walk(nk) + walk(nv)
			if m > 0 {
				v.SetMapIndex(k, reflect.Value{})
				v.SetMapIndex(nk, nv)
				n += m
			}
		}
		return n
	}
	return 0
}

// Siglas que ficam em maiúsculas nos nomes de lugar. No código, só o que é genérico (os
// algarismos romanos e "TI"); as siglas da instituição (as do patrimônio dela) vêm do
// parâmetro do painel, por SetPlaceAcronyms — nada de nome de instituição no código.
var (
	baseAcronyms = map[string]bool{"TI": true, "I": true, "II": true, "III": true, "IV": true, "V": true, "VI": true, "VII": true,
		"VIII": true, "IX": true, "X": true, "XI": true, "XII": true, "XIII": true, "XIV": true, "XV": true, "XVI": true, "XVII": true,
		"XVIII": true, "XIX": true, "XX": true}
	extraAcronyms atomic.Pointer[map[string]bool]
)

// MaxPlaceAcronyms e MaxPlaceAcronym: os limites do parâmetro.
const (
	MaxPlaceAcronyms = 100
	MaxPlaceAcronym  = 12
)

// ParsePlaceAcronyms lê a lista do parâmetro (separada por vírgula, ponto e vírgula ou
// espaço) e devolve as siglas em maiúsculas, sem repetição. Erro: sigla fora do formato
// (letras e números, até MaxPlaceAcronym) ou siglas demais.
func ParsePlaceAcronyms(v string) ([]string, error) {
	fields := strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ';' || unicode.IsSpace(r) })
	if len(fields) > MaxPlaceAcronyms {
		return nil, fmt.Errorf("no máximo %d siglas", MaxPlaceAcronyms)
	}
	seen := map[string]bool{}
	var out []string
	for _, f := range fields {
		f = strings.ToUpper(f)
		if utf8.RuneCountInString(f) > MaxPlaceAcronym || strings.IndexFunc(f, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) >= 0 {
			return nil, fmt.Errorf("sigla inválida %q: use letras e números, até %d", f, MaxPlaceAcronym)
		}
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out, nil
}

// SetPlaceAcronyms troca as siglas da instituição (o valor do parâmetro). Valor inválido
// vale como vazio: a tela só grava o que ParsePlaceAcronyms aceita.
func SetPlaceAcronyms(v string) {
	list, _ := ParsePlaceAcronyms(v)
	m := make(map[string]bool, len(list))
	for _, a := range list {
		m[a] = true
	}
	extraAcronyms.Store(&m)
}

func isAcronym(upper string) bool {
	if baseAcronyms[upper] {
		return true
	}
	if m := extraAcronyms.Load(); m != nil {
		return (*m)[upper]
	}
	return false
}

// placeDesignators: palavras que vêm antes da letra de um bloco ("Bloco A", "Torre B").
var placeDesignators = map[string]bool{"bloco": true, "torre": true, "ala": true, "setor": true, "módulo": true, "modulo": true,
	"prédio": true, "predio": true, "edifício": true, "edificio": true, "anexo": true, "pavimento": true, "andar": true, "sala": true}

// PlaceName formats a physical location for display only. Never use as a storage key.
func PlaceName(s string) string {
	words := strings.Fields(s)
	for i, word := range words {
		upper := strings.ToUpper(word)
		lower := strings.ToLower(word)
		if isAcronym(upper) {
			words[i] = upper
			continue
		}
		// A letra de um bloco ("Bloco A") não é o artigo "a".
		if utf8.RuneCountInString(word) == 1 && i > 0 && placeDesignators[strings.ToLower(words[i-1])] {
			words[i] = upper
			continue
		}
		// Artigos e preposições só no meio do nome (nunca a primeira nem a última palavra).
		if i > 0 && i < len(words)-1 {
			switch lower {
			case "a", "o", "as", "os", "de", "da", "do", "das", "dos", "e", "em", "na", "no", "nas", "nos":
				words[i] = lower
				continue
			}
		}
		runes := []rune(lower)
		if len(runes) > 0 {
			runes[0] = unicode.ToUpper(runes[0])
		}
		words[i] = string(runes)
	}
	return strings.Join(words, " ")
}
