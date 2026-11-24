package tpexec

import (
	"bytes"
	"io"
	"os"
	"regexp"
	"strings"
	"unicode/utf16"
)

// O motivo de uma falha do msiexec (1603 e outros), tirado do log verboso do próprio job: as
// linhas de erro do Windows Installer ("Error 1925.", "Product: … -- Error 1708"), a ação
// personalizada que falhou e a ação que devolveu "Return value 3". O log inteiro continua no
// disco da máquina; o resumo vai no detalhe do job, para não precisar abrir a máquina.

const (
	msiLogTail  = 4 << 20 // só o fim do log: o erro está perto de onde a instalação parou
	msiCauseMax = 3       // linhas
	msiLineMax  = 300     // caracteres por linha
)

var (
	// "MSI (s) (A0:B4) [17:23:01:123]: " e "[17:23:01:123]" no começo da linha.
	msiPrefix = regexp.MustCompile(`^(MSI \([cs]\) \([0-9A-Fa-f]{2}[:!][0-9A-Fa-f]{2}\) )?\[[0-9:]+\]:?\s*`)
	// As mensagens de erro do Windows Installer, em inglês e em português.
	msiErrorLine = regexp.MustCompile(`(?i)(\b(error|erro) [12][0-9]{3}\b|-- (error|erro)|returned actual error code|uma versão mais recente|a newer version)`)
	msiReturn3   = regexp.MustCompile(`(?i)(return value|valor de retorno) 3\b`)
)

// msiCause: até três linhas com o motivo da falha; "" sem log ou sem nada reconhecível.
func msiCause(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ""
	}
	off := max(st.Size()-msiLogTail, 0)
	raw, err := io.ReadAll(io.NewSectionReader(f, off, st.Size()-off))
	if err != nil {
		return ""
	}
	lines := strings.Split(decodeLog(raw, off), "\n")
	var out []string
	add := func(l string) {
		l = strings.TrimSpace(msiPrefix.ReplaceAllString(strings.TrimSpace(l), ""))
		if l == "" || len(out) >= msiCauseMax {
			return
		}
		if r := []rune(l); len(r) > msiLineMax {
			l = string(r[:msiLineMax-1]) + "…"
		}
		for _, o := range out {
			if o == l {
				return
			}
		}
		out = append(out, l)
	}
	// O primeiro "Return value 3": é a ação que falhou; as linhas de erro antes dela, a causa.
	end, failed := len(lines), -1
	for i, l := range lines {
		if msiReturn3.MatchString(l) {
			end, failed = i+1, i
			break
		}
	}
	for _, l := range lines[:end] {
		if msiErrorLine.MatchString(l) {
			add(l)
		}
	}
	if failed >= 0 {
		add(lines[failed])
	}
	return strings.Join(out, " | ")
}

// decodeLog: o log do msiexec sai em UTF-16 (com BOM, os caracteres nas posições pares) ou
// na página de código do sistema. off: de onde o pedaço lido começa no arquivo.
func decodeLog(b []byte, off int64) string {
	utf16le := bytes.HasPrefix(b, []byte{0xFF, 0xFE}) || (off > 0 && len(b) >= 64 && bytes.Count(b[:64], []byte{0}) > 16)
	if !utf16le {
		return strings.ReplaceAll(latin1(b), "\r", "")
	}
	switch {
	case off == 0:
		b = b[2:]
	case off%2 == 1: // começou no meio de um caractere
		b = b[1:]
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
	}
	return strings.ReplaceAll(string(utf16.Decode(u)), "\r", "")
}

// latin1: os bytes como Windows-1252/Latin-1 (o log ANSI do msiexec em português).
func latin1(b []byte) string {
	if isUTF8(b) {
		return string(b)
	}
	r := make([]rune, len(b))
	for i, c := range b {
		r[i] = rune(c)
	}
	return string(r)
}

func isUTF8(b []byte) bool { return strings.ToValidUTF8(string(b), "�") == string(b) }

// msiMaintenance: o msiexec terminou bem, mas o mesmo produto (o mesmo ProductCode) já estava
// instalado: entrou em "modo de manutenção" e só reconfigurou, sem trocar arquivo nenhum. É o
// MSI de link fixo (o do Chrome) que o fabricante não trocou, ou o mesmo instalador de novo.
// "" se o log não diz isso.
func msiMaintenance(path string) string {
	raw, err := os.ReadFile(path) // #nosec G304 -- o log do próprio job, na pasta de logs do agente
	if err != nil || len(raw) > msiLogTail*4 {
		return ""
	}
	if !strings.Contains(decodeLog(raw, 0), "Product registered: entering maintenance mode") {
		return ""
	}
	return "o msiexec não instalou nada: este MSI (o mesmo ProductCode) já estava instalado, e ele só reconfigurou o produto " +
		"(modo de manutenção). O programa ficou na versão que já tinha; o instalador guardado é o mesmo da instalação anterior"
}
