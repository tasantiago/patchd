package protocol

import (
	"strings"
	"testing"
)

// Os motivos do reinício, em português e sem repetir; os sinais sem pendência não contam.
func TestMotivosDoReinicio(t *testing.T) {
	st := RebootStatus{Signals: []RebootSignal{
		{Source: "WUA ISystemInformation.RebootRequired", Pending: true},
		{Source: `HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\WindowsUpdate\Auto Update\RebootRequired`, Pending: true},
		{Source: `HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Component Based Servicing\RebootPending`},
		{Source: "/run/reboot-required", Pending: true},
		{Source: "kernel em execução × mais novo em /boot", Pending: true},
	}}
	got := RebootReasons(st)
	if len(got) != 3 || !strings.Contains(got[0], "Windows Update") || !strings.Contains(got[1], "Pacotes") || !strings.Contains(got[2], "kernel") {
		t.Errorf("motivos: %q", got)
	}
	if RebootReasons(RebootStatus{}) != nil {
		t.Error("sem sinais, sem motivos")
	}
}
