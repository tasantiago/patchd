package patch

import (
	"context"
	"errors"

	"golang.org/x/sys/windows/registry"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/wincom"
)

// Chaves que existem apenas enquanto há reinício pendente.
var rebootKeys = []string{
	`SOFTWARE\Microsoft\Windows\CurrentVersion\Component Based Servicing\RebootPending`,
	`SOFTWARE\Microsoft\Windows\CurrentVersion\WindowsUpdate\Auto Update\RebootRequired`,
}

const sessionManagerKey = `SYSTEM\CurrentControlSet\Control\Session Manager`

// Reboot combina o WUA, as duas chaves de reinício pendente e, como indício fraco, o
// PendingFileRenameOperations. A última inicialização vem do WMI.
func (wuaScanner) Reboot(ctx context.Context) (protocol.RebootStatus, error) {
	st := protocol.RebootStatus{Signals: []protocol.RebootSignal{}}
	wua := protocol.RebootSignal{Source: "WUA ISystemInformation.RebootRequired"}
	var lastBoot string

	err := wincom.Do(ctx, func() error {
		si, err := wincom.CreateDispatch("Microsoft.Update.SystemInfo")
		if err != nil {
			wua.Error = err.Error()
		} else {
			wua.Pending, err = wincom.GetBool(si, "RebootRequired")
			si.Release()
			if err != nil {
				wua.Error = err.Error()
			}
		}
		rows, err := wincom.Query(`root\cimv2`, "SELECT LastBootUpTime FROM Win32_OperatingSystem", []string{"LastBootUpTime"})
		if err == nil && len(rows) > 0 {
			lastBoot, _ = rows[0]["LastBootUpTime"].(string)
		}
		return nil
	})
	if err != nil {
		return st, err
	}

	st.Signals = append(st.Signals, wua)
	for _, k := range rebootKeys {
		st.Signals = append(st.Signals, keySignal(k))
	}
	st.Signals = append(st.Signals, pendingRenamesSignal())

	if t, err := parseCIMDateTime(lastBoot); err == nil {
		st.LastBoot = &t
	}
	st.Pending = rebootDecision(st.Signals)
	return st, nil
}

// keySignal: a chave existir é o sinal; não existir é "não pendente".
func keySignal(path string) protocol.RebootSignal {
	sig := protocol.RebootSignal{Source: `HKLM\` + path}
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE|registry.WOW64_64KEY)
	switch {
	case err == nil:
		k.Close()
		sig.Pending = true
	case errors.Is(err, registry.ErrNotExist):
	default:
		sig.Error = err.Error()
	}
	return sig
}

// pendingRenamesSignal: arquivos a substituir no próximo boot. Muitos instaladores gravam
// aqui e a lista pode ficar sem relação com patch: indício fraco, não decide sozinho.
func pendingRenamesSignal() protocol.RebootSignal {
	sig := protocol.RebootSignal{Source: `HKLM\` + sessionManagerKey + `\PendingFileRenameOperations`, Weak: true}
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, sessionManagerKey, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		sig.Error = err.Error()
		return sig
	}
	defer k.Close()
	v, _, err := k.GetStringsValue("PendingFileRenameOperations")
	switch {
	case err == nil:
		sig.Pending = len(v) > 0
	case errors.Is(err, registry.ErrNotExist):
	default:
		sig.Error = err.Error()
	}
	return sig
}
