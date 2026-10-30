package api

import (
	"cmp"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/texto"
)

// Detalhe do equipamento em abas (Aula 12.3), como o ITOM: Resumo, Hardware, Software,
// Pendências e Tarefas. Cada aba tem a sua URL (?aba=), para mandar o link de uma aba
// direto e para o botão voltar do navegador funcionar. Sem JavaScript: a CSP do painel
// continua sem script nenhum.

var machineTabs = []struct{ Key, Label string }{
	{"resumo", "Resumo"}, {"hardware", "Hardware"}, {"bios", "BIOS"}, {"software", "Software"},
	{"pendencias", "Pendências"}, {"tarefas", "Tarefas"},
}

// Avisos que voltam de um formulário de outra aba: a página abre nela.
var (
	tabOfError = map[string]string{
		"nada-marcado": "pendencias", "nao-aprovado": "pendencias", "pacotes": "pendencias", "quando": "pendencias", "sem-janela": "pendencias",
		"sem-reinicio": "pendencias", "reinicio-form": "pendencias", "reinicio-usuarios": "pendencias",
		"windows-fonte": "pendencias", "windows-nada": "pendencias", "windows-muitas": "pendencias",
		"terceiros-nada": "pendencias", "terceiros-nao-aprovado": "pendencias", "terceiros-sem-arquivo": "pendencias", "terceiros-job": "pendencias",
		"job-aberto": "tarefas", "update-aberto": "tarefas", "https-aberto": "tarefas", "reinicio-aberto": "tarefas", "terceiros-aberto": "tarefas", "windows-aberto": "tarefas",
		"sem-chave": "tarefas", "sem-url": "tarefas", "cancelar": "tarefas",
	}
)

// machineTab: a aba pedida; sem pedido, a do aviso que voltou de um formulário; senão, Resumo.
func machineTab(q url.Values) string {
	if t := q.Get("aba"); slices.ContainsFunc(machineTabs, func(m struct{ Key, Label string }) bool { return m.Key == t }) {
		return t
	}
	if t, ok := tabOfError[q.Get("erro")]; ok {
		return t
	}
	if q.Has("criado") || q.Has("cancelado") {
		return "tarefas"
	}
	return "resumo"
}

type tabView struct {
	Label, Href, Count string
	Active             bool
}

// paramRow: uma linha de "parâmetro: valor".
type paramRow struct {
	Label, Value string
	Mono         bool // valor em fonte fixa (IDs, seriais, versões)
}

// params monta as linhas, sem as vazias.
func params(rows ...paramRow) []paramRow {
	out := rows[:0:0]
	for _, r := range rows {
		if strings.TrimSpace(r.Value) != "" {
			out = append(out, r)
		}
	}
	return out
}

// --- Resumo e Hardware --------------------------------------------------------------

// osParams: o sistema operacional como o inventário informa.
func osParams(o protocol.OSInfo) []paramRow {
	return params(
		paramRow{Label: "Sistema", Value: o.Name},
		paramRow{Label: "Edição", Value: o.Edition},
		paramRow{Label: "Versão", Value: o.Version},
		paramRow{Label: "Build", Value: o.Build, Mono: true},
		paramRow{Label: "Codinome", Value: o.Codename},
		paramRow{Label: "Kernel", Value: o.Kernel, Mono: true},
		paramRow{Label: "Arquitetura", Value: o.Arch},
		paramRow{Label: "Nome na rede", Value: o.Hostname},
	)
}

// serialText: o serial normalizado, ou o cru marcado como genérico.
func serialText(s string) string {
	if n, ok := identity.NormalizeSerial(s); ok {
		return n
	}
	if strings.TrimSpace(s) != "" {
		return "— (genérico: " + strings.TrimSpace(s) + ")"
	}
	return ""
}

func categoryText(hw protocol.Hardware) string {
	c := category(hw.Manufacturer, hw.Model, hw.ChassisType)
	switch {
	case c == "" && hw.ChassisType == "":
		return ""
	case c == "":
		return "— (chassi SMBIOS " + hw.ChassisType + ")"
	case hw.ChassisType == "":
		return c
	}
	return c + " (chassi SMBIOS " + hw.ChassisType + ")"
}

// hardwareParams: o hardware como o inventário informa.
func hardwareParams(hw protocol.Hardware) []paramRow {
	bios := strings.TrimSpace(strings.Join([]string{hw.BIOSVendor, hw.BIOSVersion}, " "))
	if hw.BIOSDate != "" {
		bios += " · " + hw.BIOSDate
	}
	mem, cpus := "", ""
	if hw.MemoryBytes > 0 {
		mem = bytesLabel(hw.MemoryBytes)
	}
	if hw.CPUThreads > 0 {
		cpus = strconv.Itoa(hw.CPUThreads)
	}
	return params(
		paramRow{Label: "Fabricante", Value: hw.Manufacturer},
		paramRow{Label: "Modelo", Value: hw.Model},
		paramRow{Label: "Categoria", Value: categoryText(hw)},
		paramRow{Label: "Número de série", Value: serialText(hw.SerialNumber), Mono: true},
		paramRow{Label: "UUID", Value: hw.UUID, Mono: true},
		paramRow{Label: "BIOS", Value: bios},
		paramRow{Label: "Processador", Value: hw.CPUModel},
		paramRow{Label: "Processadores lógicos", Value: cpus},
		paramRow{Label: "Memória", Value: mem},
	)
}

// donut: o gráfico de um volume, desenhado no servidor em SVG (sem script, sem imagem de
// fora: a CSP continua fechada). O arco é o traço de um círculo: stroke-dasharray com o
// comprimento ocupado e a circunferência.
type donut struct {
	Mount, Free, Total, Level, Dash string
	Pct                             int
}

const donutRadius = 36

func donuts(disks []diskRow) []donut {
	circ := 2 * math.Pi * donutRadius
	out := make([]donut, 0, len(disks))
	for _, d := range disks {
		level := d.Level
		if level == "" {
			level = "ok"
		}
		out = append(out, donut{Mount: d.Mount, Free: d.Free, Total: d.Total, Pct: d.Pct, Level: level,
			Dash: fmt.Sprintf("%.2f %.2f", circ*float64(d.Pct)/100, circ)})
	}
	return out
}

// --- Software -----------------------------------------------------------------------

const softwarePerPage = 50

type swRow struct {
	Name, Version, Publisher, Installed, Scope, Arch, Kind string
}

type softwareView struct {
	Total, Found      int
	Q                 string
	Rows              []swRow
	Shown, Prev, Next string
	Collected         string
}

func softwareTab(id string, inv protocol.InventoryReport, q url.Values, stamp func(time.Time) string) softwareView {
	v := softwareView{Total: len(inv.Software), Q: strings.TrimSpace(q.Get("q"))}
	v.Q = texto.Clip(v.Q, maxSearchLen) // Aula 20.3: sem partir caractere, sem UTF-8 inválido da URL
	if !inv.CollectedAt.IsZero() {
		v.Collected = stamp(inv.CollectedAt)
	}
	needle := strings.ToLower(v.Q)
	list := make([]protocol.Software, 0, len(inv.Software))
	for _, s := range inv.Software {
		if needle == "" || strings.Contains(strings.ToLower(s.Name+" "+s.Publisher+" "+s.Version), needle) {
			list = append(list, s)
		}
	}
	slices.SortStableFunc(list, func(a, b protocol.Software) int {
		if c := cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		return cmp.Compare(a.Version, b.Version)
	})
	v.Found = len(list)
	page, _ := strconv.Atoi(q.Get("pagina"))
	pages := max(1, (len(list)+softwarePerPage-1)/softwarePerPage)
	page = min(max(page, 1), pages)
	from, to := (page-1)*softwarePerPage, min(page*softwarePerPage, len(list))
	link := func(p int) string {
		u := url.Values{"aba": {"software"}}
		if v.Q != "" {
			u.Set("q", v.Q)
		}
		if p > 1 {
			u.Set("pagina", strconv.Itoa(p))
		}
		return machinePath(id) + "?" + u.Encode()
	}
	if len(list) > 0 {
		v.Shown = fmt.Sprintf("%d–%d de %d", from+1, to, len(list))
	}
	if page > 1 {
		v.Prev = link(page - 1)
	}
	if page < pages {
		v.Next = link(page + 1)
	}
	for _, s := range list[from:to] {
		row := swRow{Name: s.Name, Version: dash(s.Version), Publisher: dash(s.Publisher), Installed: "—", Arch: dash(s.Arch), Scope: "máquina"}
		if t, err := time.Parse("2006-01-02", s.InstallDate); err == nil {
			row.Installed = t.Format("02/01/2006")
		}
		if s.Scope == "user" {
			row.Scope = "usuário"
		}
		switch {
		case s.SystemComponent:
			row.Kind = "componente do sistema"
		case s.IsUpdate:
			row.Kind = "atualização"
		case s.Held:
			row.Kind = "travado"
		}
		v.Rows = append(v.Rows, row)
	}
	return v
}

// --- Montagem -----------------------------------------------------------------------

// tabsView preenche as abas, com as contagens que ajudam a escolher.
func tabsView(id, active string, software, pending, jobs int) []tabView {
	count := map[string]int{"software": software, "pendencias": pending, "tarefas": jobs}
	out := make([]tabView, 0, len(machineTabs))
	for _, t := range machineTabs {
		tv := tabView{Label: t.Label, Active: t.Key == active, Href: machinePath(id)}
		if t.Key != "resumo" {
			tv.Href += "?aba=" + t.Key
		}
		if n, ok := count[t.Key]; ok && n > 0 {
			tv.Count = strconv.Itoa(n)
		}
		out = append(out, tv)
	}
	return out
}

// inventoryView carrega o inventário atual e preenche o que as abas usam dele.
func (a *api) inventoryView(r *http.Request, id string, v *machineView) error {
	inv, found, err := a.store.LatestInventory(r.Context(), id)
	if err != nil || !found {
		return err
	}
	v.HasInventory = true
	v.OSParams = osParams(inv.OS)
	if inv.Hardware != nil {
		hw := *inv.Hardware
		v.HWParams = hardwareParams(hw)
		v.FWParams = firmwareParams(hw) // Aula 21.1
		v.Wake = wakeView(hw)           // Aula 22.1
		v.HWSources = hw.Sources
		v.Category = category(hw.Manufacturer, hw.Model, hw.ChassisType)
		v.Model = strings.TrimSpace(hw.Manufacturer + " " + hw.Model)
		v.Serial = serialText(hw.SerialNumber)
	}
	v.SoftwareCount = len(inv.Software)
	if v.Tab == "software" {
		v.Software = softwareTab(id, inv, r.URL.Query(), a.stamp)
	}
	return nil
}

// --- Firmware (Aula 21.1) ------------------------------------------------------------

// fwRow: uma linha do firmware, com o nível (o que merece atenção aparece em destaque).
type fwRow struct {
	Label, Value, Level string
}

// firmwareParams: o estado do firmware como o agente informa, só leitura.
func firmwareParams(hw protocol.Hardware) []fwRow {
	f := hw.Firmware
	if f == nil {
		return nil
	}
	yesNo := func(p *bool, yes, no, noLevel string) (string, string) {
		switch {
		case p == nil:
			return "", ""
		case *p:
			return yes, ""
		}
		return no, noLevel
	}
	var rows []fwRow
	add := func(label, value, level string) {
		if value != "" {
			rows = append(rows, fwRow{label, value, level})
		}
	}
	switch f.Mode {
	case protocol.FirmwareUEFI:
		add("Modo", "UEFI", "")
	case protocol.FirmwareLegacy:
		add("Modo", "BIOS legado (sem Secure Boot)", "atencao")
	}
	v, l := yesNo(f.SecureBoot, "ligado", "desligado", "atencao")
	add("Secure Boot", v, l)
	switch f.TPM {
	case protocol.TPMNone:
		add("TPM", "nenhum", "atencao")
	case "1.2":
		add("TPM", "1.2 (o Windows 11 pede o 2.0)", "atencao")
	case "2.0":
		add("TPM", "2.0", "")
	}
	if f.Interface == "" {
		if firmwareVendorKnown(hw.Manufacturer) {
			add("Senha da BIOS", "sem informação: o modelo não respondeu pela interface do fabricante", "")
		}
		return rows
	}
	v, l = yesNo(f.AdminPassword, "definida", "NÃO definida: qualquer pessoa na frente da máquina muda o firmware", "erro")
	add("Senha de administrador da BIOS", v, l)
	v, _ = yesNo(f.PowerOnPassword, "definida", "não definida", "")
	add("Senha de ligar", v, "")
	// Aula 21.5: no HP Sure Admin, a BIOS só aceita mudança por pacote assinado.
	v, _ = yesNo(f.SureAdmin, "ligado: o patchd não troca a senha desta BIOS", "desligado", "")
	if f.SureAdmin != nil && *f.SureAdmin {
		add("HP Sure Admin", v, "atencao")
	} else {
		add("HP Sure Admin", v, "")
	}
	add("Fonte das senhas", firmwareInterfaceLabel[f.Interface], "")
	return rows
}

var firmwareInterfaceLabel = map[string]string{
	protocol.FirmwareHP: "HP (WMI da BIOS)", protocol.FirmwareDell: "Dell (WMI da BIOS)", protocol.FirmwareLenovo: "Lenovo (WMI da BIOS)",
}

// firmwareVendorKnown: fabricantes cuja interface o agente consulta.
func firmwareVendorKnown(manufacturer string) bool {
	m := strings.ToLower(manufacturer)
	return strings.HasPrefix(m, "hp") || strings.Contains(m, "hewlett") || strings.HasPrefix(m, "dell") || strings.HasPrefix(m, "lenovo")
}
