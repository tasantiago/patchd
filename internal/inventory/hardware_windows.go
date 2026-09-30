package inventory

import (
	"context"
	"errors"
	"strings"

	"github.com/tasantiago/patchd/internal/wincom"
)

// Hardware consulta cinco classes WMI dentro de uma única sessão COM. Classe que falhar
// vira erro parcial; as demais seguem.
func (c *windowsCollector) Hardware(ctx context.Context) (Hardware, error) {
	var w wmiHardware
	var errs []error

	err := wincom.Do(ctx, func() error {
		query := func(class string, props ...string) []wincom.Row {
			wql := "SELECT " + strings.Join(props, ", ") + " FROM " + class
			rows, err := wincom.Query(`root\cimv2`, wql, props)
			if err != nil {
				errs = append(errs, err)
			}
			return rows
		}
		if r := query("Win32_ComputerSystemProduct", "Vendor", "Name", "IdentifyingNumber", "UUID"); len(r) > 0 {
			w.Product = r[0]
		}
		if r := query("Win32_BIOS", "Manufacturer", "SMBIOSBIOSVersion", "ReleaseDate"); len(r) > 0 {
			w.BIOS = r[0]
		}
		if r := query("Win32_SystemEnclosure", "ChassisTypes"); len(r) > 0 {
			w.Enclosure = r[0]
		}
		if r := query("Win32_ComputerSystem", "TotalPhysicalMemory"); len(r) > 0 {
			w.System = r[0]
		}
		for _, r := range query("Win32_Processor", "Name", "NumberOfLogicalProcessors") {
			w.Processors = append(w.Processors, r)
		}
		return nil
	})
	if err != nil {
		// COM não inicializou ou o prazo acabou: w e errs podem ainda estar em uso pela
		// goroutine do COM, então não são lidos.
		return Hardware{}, err
	}
	return windowsHardwareInfo(w), errors.Join(errs...)
}
