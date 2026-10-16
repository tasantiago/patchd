// Package wuerr dá nome aos HRESULTs do Windows Update (Aula 16.2): o agente os põe no
// resultado do job e na busca, e o painel os mostra com o nome que a documentação da
// Microsoft usa e, para os mais comuns no lab, o que costuma estar por trás.
package wuerr

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type info struct{ name, hint string }

var known = map[uint32]info{
	0x80240016: {"WU_E_INSTALL_NOT_ALLOWED", "outra instalação em andamento, ou reinício pendente"},
	0x80240017: {"WU_E_NOT_APPLICABLE", ""},
	0x8024001E: {"WU_E_SERVICE_STOP", ""},
	0x80240022: {"WU_E_ALL_UPDATES_FAILED", ""},
	0x80244007: {"WU_E_PT_SOAPCLIENT_SOAPFAULT", "o servidor (WSUS) recusou a sincronização; o motivo está no WindowsUpdate.log (ErrorCode do SOAP)"},
	0x80244010: {"WU_E_PT_EXCEEDED_MAX_SERVER_TRIPS", "idas e voltas demais ao servidor: comum na primeira sincronização, ou com catálogo grande no WSUS"},
	0x80244017: {"WU_E_PT_HTTP_STATUS_DENIED", ""},
	0x80244018: {"WU_E_PT_HTTP_STATUS_FORBIDDEN", ""},
	0x80244019: {"WU_E_PT_HTTP_STATUS_NOT_FOUND", ""},
	0x8024401C: {"WU_E_PT_HTTP_STATUS_REQUEST_TIMEOUT", ""},
	0x8024401F: {"WU_E_PT_HTTP_STATUS_SERVER_ERROR", "erro interno no servidor de atualizações"},
	0x8024402C: {"WU_E_PT_WINHTTP_NAME_NOT_RESOLVED", "o nome do servidor de atualizações não resolve"},
	0x80244022: {"WU_E_PT_HTTP_STATUS_SERVICE_UNAVAIL", ""},
	0x80072EE2: {"ERROR_INTERNET_TIMEOUT", ""},
	0x80072EFD: {"ERROR_INTERNET_CANNOT_CONNECT", "sem conexão com o servidor de atualizações"},
	0x80070070: {"ERROR_DISK_FULL", ""},
	0x800F0922: {"CBS_E_INSTALLERS_FAILED", ""},
}

// Name devolve o nome do código ("" se desconhecido).
func Name(code uint32) string { return known[code].name }

// Text: "0x80244007 WU_E_PT_SOAPCLIENT_SOAPFAULT", ou só o hexadecimal.
func Text(code uint32) string {
	if n := Name(code); n != "" {
		return fmt.Sprintf("0x%08X %s", code, n)
	}
	return fmt.Sprintf("0x%08X", code)
}

var hexCode = regexp.MustCompile(`0[xX][0-9A-Fa-f]{8}`)

// Annotate acrescenta, a um texto de erro, o nome do primeiro HRESULT conhecido e a dica,
// se houver: "busca no WUA: Search: HRESULT 0x80244007" → "… 0x80244007
// (WU_E_PT_SOAPCLIENT_SOAPFAULT: o servidor (WSUS) recusou …)". Texto sem código conhecido
// volta igual.
func Annotate(s string) string {
	for _, m := range hexCode.FindAllString(s, -1) {
		v, err := strconv.ParseUint(m[2:], 16, 32)
		if err != nil {
			continue
		}
		i, ok := known[uint32(v)]
		if !ok || strings.Contains(s, i.name) {
			continue
		}
		extra := i.name
		if i.hint != "" {
			extra += ": " + i.hint
		}
		return strings.Replace(s, m, m+" ("+extra+")", 1)
	}
	return s
}
