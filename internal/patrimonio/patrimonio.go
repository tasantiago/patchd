// Package patrimonio lê o CSV do sistema de patrimônio e liga o tombo à máquina (Aula 23.6).
//
// O arquivo é o que o sistema de patrimônio exporta: uma linha de cabeçalho com os nomes das
// colunas (DEPREDIO, DESALA, UO, TOMBO, DEBEM, DESUBGRUPOBEM, em qualquer ordem; só o TOMBO é
// obrigatório) e uma linha por bem. Vírgula ou ponto e vírgula (o Excel em português salva com
// ponto e vírgula); UTF-8, com ou sem BOM, ou Windows-1252 (o "CSV" do Excel no Windows).
//
// A máquina e o tombo se ligam pelo nome: cada sequência de dígitos do hostname, sem os zeros
// à esquerda, é comparada com o tombo, também sem os zeros. FJ088594 é o tombo 88594; o
// prefixo e o sufixo de letras variam à vontade. A sequência inteira, e não um pedaço: o
// tombo 1234 não liga a máquina PC112345.
package patrimonio

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/tasantiago/patchd/internal/texto"
)

// Item é um bem do arquivo: o tombo (sem os zeros à esquerda) e onde ele está.
type Item struct {
	Tombo, Building, Room, Unit, Description, Subgroup string
}

// Result é o arquivo lido: os bens (um por tombo; repetido, vale a última linha) e o que
// ficou de fora.
type Result struct {
	Items      []Item
	Skipped    int   // linhas sem tombo válido
	SkippedAt  []int // as primeiras linhas puladas (número da linha no arquivo), para a tela
	Duplicates int   // tombos repetidos (vale a última linha)
	Separator  rune
	Converted  bool // o arquivo veio em Windows-1252 e foi convertido
}

// Limites do arquivo.
const (
	MaxBytes   = 20 << 20 // 20 MB: o patrimônio inteiro de um tribunal cabe com folga
	MaxRows    = 300000
	maxField   = 200
	maxSkipped = 10
)

// Colunas reconhecidas no cabeçalho.
const (
	ColBuilding    = "DEPREDIO"
	ColRoom        = "DESALA"
	ColUnit        = "UO"
	ColTombo       = "TOMBO"
	ColDescription = "DEBEM"
	ColSubgroup    = "DESUBGRUPOBEM"
)

// Parse lê o arquivo inteiro (até MaxBytes).
func Parse(r io.Reader) (Result, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxBytes+1))
	if err != nil {
		return Result{}, err
	}
	if len(data) > MaxBytes {
		return Result{}, fmt.Errorf("o arquivo passa de %d MB", MaxBytes>>20)
	}
	var res Result
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(data) {
		data, res.Converted = fromCP1252(data), true
	}
	first, _, _ := strings.Cut(string(data), "\n")
	res.Separator = ','
	if strings.Count(first, ";") > strings.Count(first, ",") {
		res.Separator = ';'
	}
	cr := csv.NewReader(bytes.NewReader(data))
	cr.Comma, cr.FieldsPerRecord, cr.LazyQuotes, cr.ReuseRecord = res.Separator, -1, true, true

	header, err := cr.Read()
	if err != nil {
		return Result{}, errors.New("o arquivo está vazio ou não é um CSV")
	}
	col := map[string]int{}
	for i, h := range header {
		col[strings.ToUpper(strings.TrimSpace(h))] = i
	}
	ti, ok := col[ColTombo]
	if !ok {
		return Result{}, fmt.Errorf("falta a coluna %s no cabeçalho (a primeira linha: %s, %s, %s, %s, %s, %s)",
			ColTombo, ColBuilding, ColRoom, ColUnit, ColTombo, ColDescription, ColSubgroup)
	}
	field := func(rec []string, name string) string {
		i, ok := col[name]
		if !ok || i >= len(rec) {
			return ""
		}
		return clean(rec[i])
	}
	pos := map[string]int{}
	line := 1
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		line++
		if err != nil {
			return Result{}, fmt.Errorf("linha %d: %v", line, err)
		}
		if len(rec) == 1 && strings.TrimSpace(rec[0]) == "" {
			continue // linha em branco
		}
		if len(pos) >= MaxRows {
			return Result{}, fmt.Errorf("o arquivo passa de %d bens", MaxRows)
		}
		tombo := ""
		if ti < len(rec) {
			tombo = Normalize(rec[ti])
		}
		if tombo == "" {
			res.Skipped++
			if len(res.SkippedAt) < maxSkipped {
				res.SkippedAt = append(res.SkippedAt, line)
			}
			continue
		}
		it := Item{Tombo: tombo, Building: field(rec, ColBuilding), Room: field(rec, ColRoom), Unit: field(rec, ColUnit),
			Description: field(rec, ColDescription), Subgroup: field(rec, ColSubgroup)}
		if i, ok := pos[tombo]; ok {
			res.Duplicates++
			res.Items[i] = it
			continue
		}
		pos[tombo] = len(res.Items)
		res.Items = append(res.Items, it)
	}
	return res, nil
}

// Normalize: o tombo só com dígitos (espaços em volta saem), sem os zeros à esquerda; vazio
// se não for um número de 1 a 20 dígitos.
func Normalize(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 20 {
		return ""
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return ""
		}
	}
	return strings.TrimLeft(s, "0")
}

// Tombos: as sequências de dígitos do hostname, sem os zeros à esquerda, na ordem (as que
// só têm zeros ficam de fora).
func Tombos(hostname string) []string {
	var out []string
	start := -1
	for i := 0; i <= len(hostname); i++ {
		digit := i < len(hostname) && hostname[i] >= '0' && hostname[i] <= '9'
		switch {
		case digit && start < 0:
			start = i
		case !digit && start >= 0:
			if t := Normalize(hostname[start:i]); t != "" {
				out = append(out, t)
			}
			start = -1
		}
	}
	return out
}

// Match: o bem da máquina, pelo hostname; a primeira sequência de dígitos que for um tombo.
func Match[T any](hostname string, byTombo map[string]T) (T, bool) {
	for _, t := range Tombos(hostname) {
		if v, ok := byTombo[t]; ok {
			return v, true
		}
	}
	var zero T
	return zero, false
}

// clean: um campo do arquivo para a tela e o banco: sem controles, espaços repetidos viram um,
// no máximo maxField bytes.
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	return texto.Clip(strings.Join(strings.Fields(s), " "), maxField)
}

// cp1252: os caracteres de 0x80 a 0x9F do Windows-1252 (de 0xA0 em diante, é o Latin-1, igual
// ao Unicode). Os cinco indefinidos viram U+FFFD.
var cp1252 = [32]rune{
	'€', '�', '‚', 'ƒ', '„', '…', '†', '‡', 'ˆ', '‰', 'Š', '‹', 'Œ', '�', 'Ž', '�',
	'�', '‘', '’', '“', '”', '•', '–', '—', '˜', '™', 'š', '›', 'œ', '�', 'ž', 'Ÿ',
}

func fromCP1252(b []byte) []byte {
	var out bytes.Buffer
	out.Grow(len(b) + len(b)/8)
	for _, c := range b {
		switch {
		case c < 0x80:
			out.WriteByte(c)
		case c < 0xA0:
			out.WriteRune(cp1252[c-0x80])
		default:
			out.WriteRune(rune(c))
		}
	}
	return out.Bytes()
}
