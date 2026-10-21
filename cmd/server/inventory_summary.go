package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf16"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/wake"
)

const inventoryUsage = `uso: patchd-server inventory summary -dir PASTA [-maquinas]

Resume uma pasta de inventários avulsos (a saída do "patchd-agent -inventory", um arquivo
.json por máquina) sem banco nem servidor: o firmware (Aula 21.2) e ligar pela rede (Aula
22.1), contados. Serve a frota que ainda não fala com o servidor: a coleta roda pelo PsExec
ou pelo ITOM, e o resumo, aqui.

  -maquinas  lista também os nomes das máquinas em cada contagem (para você; não repasse:
             nome de máquina é dado do ambiente)
`

func runInventory(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "summary" {
		_, _ = io.WriteString(stderr, inventoryUsage)
		return exitConfig
	}
	fs := flag.NewFlagSet("inventory summary", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", "", "pasta com os .json")
	names := fs.Bool("maquinas", false, "lista os nomes das máquinas em cada contagem")
	if err := fs.Parse(args[1:]); err != nil || *dir == "" || fs.NArg() > 0 {
		_, _ = io.WriteString(stderr, inventoryUsage)
		return exitConfig
	}
	files, err := filepath.Glob(filepath.Join(*dir, "*.json"))
	if err != nil || len(files) == 0 {
		fmt.Fprintf(stderr, "patchd-server: nenhum .json em %s\n", *dir)
		return exitConfig
	}
	s := newFleetSummary()
	bad := 0
	for _, f := range files {
		rep, err := readInventoryFile(f)
		if err != nil {
			bad++
			continue
		}
		s.add(rep)
	}
	s.print(stdout, len(files), bad, *names)
	return exitOK
}

// maxInventoryFile: um inventário com milhares de programas cabe com folga.
const maxInventoryFile = 32 << 20

// readInventoryFile lê um .json da coleta. O "> arquivo" do PowerShell 5 grava UTF-16 com BOM;
// o do 7, UTF-8 (às vezes com BOM): os três passam.
func readInventoryFile(path string) (protocol.InventoryReport, error) {
	var rep protocol.InventoryReport
	f, err := os.Open(path) // #nosec G304 -- arquivo da pasta que o operador indicou
	if err != nil {
		return rep, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxInventoryFile+1))
	if err != nil {
		return rep, err
	}
	if len(b) > maxInventoryFile {
		return rep, fmt.Errorf("%s: maior que %d MiB", path, maxInventoryFile>>20)
	}
	if err := json.Unmarshal(textUTF8(b), &rep); err != nil {
		return rep, err
	}
	if rep.OS.Family == "" {
		return rep, fmt.Errorf("%s: não é um inventário do patchd", path)
	}
	return rep, nil
}

// textUTF8: o conteúdo em UTF-8, pelo BOM.
func textUTF8(b []byte) []byte {
	switch {
	case bytes.HasPrefix(b, []byte{0xEF, 0xBB, 0xBF}):
		return b[3:]
	case bytes.HasPrefix(b, []byte{0xFF, 0xFE}), bytes.HasPrefix(b, []byte{0xFE, 0xFF}):
		var order binary.ByteOrder = binary.LittleEndian
		if b[0] == 0xFE {
			order = binary.BigEndian
		}
		u := make([]uint16, 0, len(b)/2)
		for i := 2; i+1 < len(b); i += 2 {
			u = append(u, order.Uint16(b[i:]))
		}
		return []byte(string(utf16.Decode(u)))
	}
	return b
}

// fleetSummary conta por pergunta e por resposta, guardando os nomes para o -maquinas.
type fleetSummary struct {
	answers map[string][]string            // pergunta → respostas, na ordem
	who     map[string]map[string][]string // pergunta → resposta → máquinas
}

func newFleetSummary() *fleetSummary {
	return &fleetSummary{answers: map[string][]string{}, who: map[string]map[string][]string{}}
}

func (s *fleetSummary) count(question, answer, host string) {
	if _, ok := s.who[question]; !ok {
		s.who[question] = map[string][]string{}
	}
	if _, ok := s.who[question][answer]; !ok {
		s.answers[question] = append(s.answers[question], answer)
	}
	s.who[question][answer] = append(s.who[question][answer], host)
}

func yesNoUnknown(p *bool, yes, no string) string {
	switch {
	case p == nil:
		return "sem informação"
	case *p:
		return yes
	}
	return no
}

// Perguntas do resumo.
const (
	qVendor   = "Interface da BIOS (fabricante)"
	qAdmin    = "Senha de administrador da BIOS"
	qSure     = "HP Sure Admin (só HP)"
	qSecure   = "Secure Boot"
	qTPM      = "TPM"
	qWake     = "Ligar pela rede"
	qBlockers = "Ligar pela rede: o que impede (uma máquina pode ter mais de um)"
	qNotes    = "Ligar pela rede: atenção"
)

func (s *fleetSummary) add(rep protocol.InventoryReport) {
	host := rep.OS.Hostname
	if host == "" {
		host = "(sem nome)"
	}
	var hw protocol.Hardware
	if rep.Hardware != nil {
		hw = *rep.Hardware
	}
	fw := hw.Firmware
	if fw != nil && fw.Check() != nil {
		fw = nil
	}
	if fw == nil {
		fw = &protocol.Firmware{}
	}
	vendor := fw.Interface
	if vendor == "" {
		vendor = "nenhuma"
	}
	s.count(qVendor, vendor, host)
	s.count(qAdmin, yesNoUnknown(fw.AdminPassword, "definida", "NÃO definida"), host)
	if fw.Interface == protocol.FirmwareHP {
		s.count(qSure, yesNoUnknown(fw.SureAdmin, "ligado", "desligado"), host)
	}
	s.count(qSecure, yesNoUnknown(fw.SecureBoot, "ligado", "desligado"), host)
	tpm := fw.TPM
	if tpm == "" {
		tpm = "sem informação"
	}
	s.count(qTPM, tpm, host)
	w := hw.Wake
	if w != nil && w.Check() != nil {
		w = nil
	}
	v := wake.Decide(w, wake.LaptopChassis(hw.ChassisType))
	s.count(qWake, v.State, host)
	for _, b := range v.Blockers {
		s.count(qBlockers, b, host)
	}
	for _, n := range v.Notes {
		s.count(qNotes, n, host)
	}
}

func (s *fleetSummary) print(w io.Writer, files, bad int, names bool) {
	fmt.Fprintf(w, "inventários lidos: %d de %d arquivos", files-bad, files)
	if bad > 0 {
		fmt.Fprintf(w, " (%d ilegíveis ou fora do formato)", bad)
	}
	fmt.Fprintln(w)
	for _, q := range []string{qVendor, qAdmin, qSure, qSecure, qTPM, qWake, qBlockers, qNotes} {
		if len(s.answers[q]) == 0 {
			continue
		}
		fmt.Fprintf(w, "\n%s\n", q)
		ans := s.answers[q]
		sort.SliceStable(ans, func(i, k int) bool { return len(s.who[q][ans[i]]) > len(s.who[q][ans[k]]) })
		for _, a := range ans {
			fmt.Fprintf(w, "  %4d  %s\n", len(s.who[q][a]), a)
			if names {
				list := append([]string(nil), s.who[q][a]...)
				sort.Strings(list)
				fmt.Fprintf(w, "        %s\n", strings.Join(list, ", "))
			}
		}
	}
	if names {
		fmt.Fprintln(w, "\n(a lista acima tem nomes de máquinas: fica com você)")
	}
}
