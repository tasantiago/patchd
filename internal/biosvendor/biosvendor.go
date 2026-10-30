// Package biosvendor traduz o que a interface WMI da Dell e da Lenovo devolve nas
// configurações da BIOS do protocolo (docs/bios-config.md, seção 8). É portátil: o inventário
// e o agente leem no Windows (read_windows.go) e testam a tradução em qualquer sistema.
//
//   - Dell (BIOS com o provedor WMI de fábrica, 2018 em diante): root\dcim\sysman\biosattributes,
//     classes EnumerationAttribute, IntegerAttribute e StringAttribute (AttributeName,
//     CurrentValue como lista de um item, PossibleValue, IsReadonly). Modelo anterior (só com o
//     Dell Command | Monitor): sem a classe, sem configurações.
//   - Lenovo: root\WMI, Lenovo_BiosSetting.CurrentSetting, uma linha
//     "Nome,Valor;[Optional:A,B,C][Status:ShowOnly]". Os modelos sem o [Optional:...] dão os
//     valores possíveis pelo Lenovo_GetBiosSelections, consultado só para as configurações da
//     lista fechada (são dezenas de chamadas, e as outras não ganham seletor).
package biosvendor

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/texto"
)

// Namespaces WMI.
const (
	DellAttrNamespace     = `root\dcim\sysman\biosattributes`
	DellSecurityNamespace = `root\dcim\sysman\wmisecurity`
	LenovoNamespace       = `root\WMI`
)

// DellClasses: as classes de configuração da Dell e o tipo de cada uma.
var DellClasses = []struct{ Class, Kind string }{
	{"EnumerationAttribute", protocol.BIOSKindEnum},
	{"IntegerAttribute", protocol.BIOSKindInteger},
	{"StringAttribute", protocol.BIOSKindString},
}

// DellProps: o que se lê de cada linha (lidas com tolerância: um modelo sem uma das
// propriedades não perde a linha).
var DellProps = []string{"AttributeName", "CurrentValue", "PossibleValue", "IsReadonly"}

// DellRow é uma linha de uma das classes da Dell.
type DellRow struct {
	Kind string
	Row  map[string]any
}

// DellSettings monta as configurações da Dell.
func DellSettings(rows []DellRow) []protocol.BIOSSetting {
	out := make([]protocol.BIOSSetting, 0, len(rows))
	for _, r := range rows {
		s := protocol.BIOSSetting{Name: str(r.Row["AttributeName"]), Kind: r.Kind, Value: first(r.Row["CurrentValue"])}
		if ro, ok := num(r.Row["IsReadonly"]); ok {
			s.ReadOnly = ro != 0
		}
		if r.Kind == protocol.BIOSKindEnum {
			s.Possible = list(r.Row["PossibleValue"])
		}
		out = append(out, s)
	}
	return Finish(out)
}

// LenovoSetting lê uma linha do Lenovo_BiosSetting. ok = false: linha vazia ou sem nome.
func LenovoSetting(current string) (s protocol.BIOSSetting, ok bool) {
	head, tail, _ := strings.Cut(current, ";")
	name, value, _ := strings.Cut(head, ",")
	s = protocol.BIOSSetting{Name: strings.TrimSpace(name), Value: strings.TrimSpace(value), Kind: protocol.BIOSKindString}
	if s.Name == "" {
		return s, false
	}
	// Os colchetes depois do ";": [Optional:...] e [Status:ShowOnly].
	for tail != "" {
		i := strings.IndexByte(tail, '[')
		j := strings.IndexByte(tail, ']')
		if i < 0 || j < i {
			break
		}
		key, val, _ := strings.Cut(tail[i+1:j], ":")
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "optional":
			s.Possible = LenovoSelections(val)
		case "status":
			s.ReadOnly = strings.EqualFold(strings.TrimSpace(val), "ShowOnly")
		}
		tail = tail[j+1:]
	}
	if len(s.Possible) > 0 {
		s.Kind = protocol.BIOSKindEnum
	}
	return s, true
}

// LenovoSelections lê a lista de valores ("Disable,ACOnly,Enable").
func LenovoSelections(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// LenovoSettings monta as configurações da Lenovo. selections: os valores possíveis vindos do
// Lenovo_GetBiosSelections, por nome, para as linhas sem [Optional:...].
func LenovoSettings(currents []string, selections map[string]string) []protocol.BIOSSetting {
	out := make([]protocol.BIOSSetting, 0, len(currents))
	for _, c := range currents {
		s, ok := LenovoSetting(c)
		if !ok {
			continue
		}
		if sel, found := selections[s.Name]; found && len(s.Possible) == 0 {
			if s.Possible = LenovoSelections(sel); len(s.Possible) > 0 {
				s.Kind = protocol.BIOSKindEnum
			}
		}
		out = append(out, s)
	}
	return Finish(out)
}

// LenovoNeedSelections: as linhas sem valores possíveis que passam em want (a lista fechada).
func LenovoNeedSelections(currents []string, want func(name string) bool) []string {
	var out []string
	for _, c := range currents {
		if s, ok := LenovoSetting(c); ok && len(s.Possible) == 0 && !s.ReadOnly && want(s.Name) && !slices.Contains(out, s.Name) {
			out = append(out, s.Name)
		}
	}
	return out
}

// Finish deixa as configurações como o protocolo aceita: sem nome vazio, repetido, com
// caractere de controle ou com cara de senha; valores cortados; ordem estável (o hash do
// inventário não muda à toa); no máximo protocol.MaxBIOSSettings.
func Finish(in []protocol.BIOSSetting) []protocol.BIOSSetting {
	out := []protocol.BIOSSetting{}
	seen := map[string]bool{}
	for _, s := range in {
		s.Name = texto.Clip(strings.TrimSpace(s.Name), protocol.MaxBIOSName)
		if s.Name == "" || seen[s.Name] || control(s.Name) || strings.Contains(strings.ToLower(s.Name), "password") {
			continue
		}
		seen[s.Name] = true
		s.Path, s.Value = Clip(s.Path), Clip(s.Value)
		var possible []string
		for _, p := range s.Possible {
			if p = Clip(p); p != "" && len(possible) < protocol.MaxBIOSPossible && !slices.Contains(possible, p) {
				possible = append(possible, p)
			}
		}
		s.Possible = possible
		if s.Kind != protocol.BIOSKindEnum {
			s.Possible = nil
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, k int) bool {
		if out[i].Path != out[k].Path {
			return out[i].Path < out[k].Path
		}
		return out[i].Name < out[k].Name
	})
	if len(out) > protocol.MaxBIOSSettings {
		out = out[:protocol.MaxBIOSSettings]
	}
	return out
}

// Clip: o valor cortado no limite, com os caracteres de controle trocados por espaço.
func Clip(v string) string {
	v = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, strings.TrimSpace(v))
	return texto.Clip(strings.TrimSpace(v), protocol.MaxBIOSValue)
}

func control(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

func str(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(x)
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

// first: a Dell devolve o valor atual como lista de um item.
func first(v any) string {
	if l, ok := v.([]any); ok {
		if len(l) == 0 {
			return ""
		}
		return str(l[0])
	}
	return str(v)
}

func list(v any) []string {
	l, ok := v.([]any)
	if !ok {
		if s := str(v); s != "" {
			return []string{s}
		}
		return nil
	}
	out := make([]string, 0, len(l))
	for _, x := range l {
		if s := str(x); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func num(v any) (uint64, bool) {
	switch x := v.(type) {
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case nil:
		return 0, false
	}
	n, err := strconv.ParseInt(str(v), 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return uint64(n), true
}
