package platform

import "testing"

func TestRunning(t *testing.T) {
	list := []Process{{Name: "Code.exe", PID: 1, Session: 1}, {Name: "code.EXE", PID: 2, Session: 2}, {Name: "explorer.exe", PID: 3, Session: 1}}
	got := Running(list, []string{"Code.exe"})
	if len(got) != 2 || got[0].PID != 1 || got[1].PID != 2 {
		t.Errorf("Code.exe: %+v", got)
	}
	if got := Running(list, nil); len(got) != 0 {
		t.Errorf("sem nomes: %+v", got)
	}
}
