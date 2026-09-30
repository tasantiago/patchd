package inventory

import "testing"

func TestWindowsOSInfo(t *testing.T) {
	casos := []struct {
		nome      string
		cv        currentVersion
		querNome  string
		querBuild string
	}{
		// Build, UBR e DisplayVersion reais do laboratório (Aula 0.1). O EditionID
		// não foi coletado ainda (pendência A4); "Professional" é um valor de exemplo.
		{"windows 11 25H2 do laboratório",
			currentVersion{CurrentBuild: "26200", UBR: 9457, DisplayVersion: "25H2", EditionID: "Professional"},
			"Windows 11 Pro", "26200.9457"},
		{"windows 10 22H2",
			currentVersion{CurrentBuild: "19045", UBR: 6456, DisplayVersion: "22H2", EditionID: "Enterprise"},
			"Windows 10 Enterprise", "19045.6456"},
		{"limite exato do Windows 11",
			currentVersion{CurrentBuild: "22000", UBR: 1, EditionID: "Core"},
			"Windows 11 Home", "22000.1"},
		{"edição desconhecida aparece crua",
			currentVersion{CurrentBuild: "26100", UBR: 100, EditionID: "IoTEnterpriseS"},
			"Windows 11 IoTEnterpriseS", "26100.100"},
		{"sem UBR",
			currentVersion{CurrentBuild: "26200"},
			"Windows 11", "26200"},
		{"build ilegível",
			currentVersion{CurrentBuild: "xyz", EditionID: "Professional"},
			"Windows Pro", "xyz"},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			got := windowsOSInfo(c.cv, "pc-teste")
			if got.Name != c.querNome || got.Build != c.querBuild {
				t.Errorf("nome=%q build=%q, esperado nome=%q build=%q", got.Name, got.Build, c.querNome, c.querBuild)
			}
			if got.Edition != c.cv.EditionID || got.Family != "windows" || got.Hostname != "pc-teste" {
				t.Errorf("campos brutos errados: %+v", got)
			}
		})
	}
}
