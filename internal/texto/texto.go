// Package texto prepara o texto que vem de fora (agente, URL, provedor) para a tela e para
// o PostgreSQL (Aula 20.3). O banco recusa dois tipos de texto que o Go aceita sem
// reclamar: bytes que não formam UTF-8 e o caractere NUL. Recusado, o INSERT falha, a
// requisição vira um 500 e o dado se perde (um inventário, o resultado de um job). Os dois
// viram U+FFFD (�), a mesma troca que o encoding/json já faz com UTF-8 inválido.
package texto

import (
	"reflect"
	"strings"
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
