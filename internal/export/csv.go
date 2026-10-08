// Package export escreve o estado de compliance em CSV (RF-26, Aula 7.8), para abrir no
// Excel ou importar no Google Sheets.
//
// Duas escolhas:
//   - separador ";" e BOM UTF-8 no início: é o que o Excel em português abre direto,
//     com acentos (a vírgula é o separador decimal no Brasil). O Google Sheets detecta o
//     separador na importação;
//   - nenhum campo começa com = + - @ ou tabulação: o nome da máquina, a versão de um
//     pacote e o título de uma CVE vêm do agente ou das fontes, e uma planilha executaria
//     um campo como "=HYPERLINK(...)" como fórmula (injeção de CSV, OWASP). Esses campos
//     ganham um apóstrofo na frente, que a planilha mostra como texto.
package export

import (
	"encoding/csv"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/compliance"
	"github.com/tasantiago/patchd/internal/protocol"
)

// BOM é a marca de UTF-8 que o Excel usa para reconhecer a codificação.
const BOM = "\xef\xbb\xbf"

// safe neutraliza o campo que uma planilha interpretaria como fórmula.
func safe(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

func when(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02 15:04:05")
}

func newWriter(w io.Writer) (*csv.Writer, error) {
	if _, err := io.WriteString(w, BOM); err != nil {
		return nil, err
	}
	cw := csv.NewWriter(w)
	cw.Comma = ';'
	cw.UseCRLF = true // o Excel e o Bloco de Notas do Windows esperam CRLF
	return cw, nil
}

func writeRow(cw *csv.Writer, fields ...string) error {
	for i, f := range fields {
		fields[i] = safe(f)
	}
	return cw.Write(fields)
}

// Fleet escreve uma linha por máquina. As datas saem em UTC, no formato que as
// planilhas reconhecem como data.
func Fleet(w io.Writer, fleet protocol.ComplianceFleet) error {
	cw, err := newWriter(w)
	if err != nil {
		return err
	}
	if err := writeRow(cw, "maquina", "nome", "sistema", "estado", "pendentes", "unidade", "exploradas",
		"ultimo_contato_utc", "inventario_coletado_utc", "catalogo", "motivos", "avaliado_em_utc"); err != nil {
		return err
	}
	for _, m := range fleet.Machines {
		collected := ""
		if m.InventoryCollectedAt != nil {
			collected = when(*m.InventoryCollectedAt)
		}
		if err := writeRow(cw, m.MachineID, m.Hostname, m.OS, compliance.State(m.State).Label(),
			fmt.Sprint(m.Pending), m.PendingUnit, fmt.Sprint(m.Exploited), when(m.LastSeenAt), collected,
			m.Catalog, strings.Join(m.Reasons, "; "), when(m.EvaluatedAt)); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// Items escreve as pendências de uma máquina, uma por linha.
func Items(w io.Writer, d protocol.ComplianceDetail) error {
	cw, err := newWriter(w)
	if err != nil {
		return err
	}
	if err := writeRow(cw, "maquina", "nome", "item", "explorada", "severidade", "instalado", "corrigido_em", "observacao"); err != nil {
		return err
	}
	for _, it := range d.Items {
		exploited := "não"
		if it.Exploited {
			exploited = "sim"
		}
		if err := writeRow(cw, d.MachineID, d.Hostname, it.ID, exploited, it.Severity, it.Installed, it.FixedIn, it.Note); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}
