package biosvendor

import (
	"fmt"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/wincom"
)

// ReadDell lê as configurações da Dell. Deve ser chamada dentro de wincom.Do. Sem a classe
// de enumeração (modelo sem o provedor de fábrica): o erro do WMI, que wincom.Missing
// reconhece.
func ReadDell() ([]protocol.BIOSSetting, error) {
	var rows []DellRow
	for i, c := range DellClasses {
		got, err := wincom.QueryLoose(DellAttrNamespace, c.Class, DellProps)
		if err != nil {
			if i > 0 && wincom.Missing(err) { // um modelo sem números ou sem texto
				continue
			}
			return nil, fmt.Errorf("interface da Dell: %w", err)
		}
		for _, r := range got {
			rows = append(rows, DellRow{Kind: c.Kind, Row: r})
		}
	}
	return DellSettings(rows), nil
}

// ReadLenovo lê as configurações da Lenovo, com os valores possíveis das que passam em want
// (a lista fechada). Deve ser chamada dentro de wincom.Do.
func ReadLenovo(want func(name string) bool) ([]protocol.BIOSSetting, error) {
	rows, err := wincom.Query(LenovoNamespace, "SELECT CurrentSetting FROM Lenovo_BiosSetting", []string{"CurrentSetting"})
	if err != nil {
		return nil, fmt.Errorf("interface da Lenovo: %w", err)
	}
	currents := make([]string, 0, len(rows))
	for _, r := range rows {
		currents = append(currents, str(r["CurrentSetting"]))
	}
	selections := map[string]string{}
	for _, name := range LenovoNeedSelections(currents, want) {
		out, _, err := wincom.ExecMethod(LenovoNamespace, "SELECT * FROM Lenovo_GetBiosSelections", "GetBiosSelections",
			[]wincom.Param{{Name: "Item", Value: name}}, []string{"Selections"})
		if err != nil {
			continue // sem a lista, a configuração fica sem seletor
		}
		selections[name] = str(out["Selections"])
	}
	return LenovoSettings(currents, selections), nil
}
