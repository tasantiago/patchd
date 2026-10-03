package patch

import (
	"strings"
	"testing"
	"time"
)

// Saída real do Mac do laboratório (Aula 0.2), numa máquina com macOS 26.5.2.
const softwareUpdateReal = `Software Update Tool

Finding available software
Software Update found the following new or updated software:
* Label: Safari27.0TahoeAuto-27.0
	Title: Safari, Version: 27.0, Size: 249465KiB, Recommended: YES,
* Label: macOS Tahoe 26.7.1-25G241
	Title: macOS Tahoe 26.7.1, Version: 26.7.1, Size: 3852999KiB, Recommended: YES, Action: restart,
* Label: macOS 27.0.1-26A434
	Title: macOS 27.0.1, Version: 27.0.1, Size: 11852702KiB, Recommended: YES, Action: restart,
`

func TestParseSoftwareUpdateListReal(t *testing.T) {
	list := parseSoftwareUpdateList([]byte(softwareUpdateReal), "26.5.2")
	if len(list) != 3 {
		t.Fatalf("esperados 3 itens, vieram %d: %+v", len(list), list)
	}

	safari, tahoe, major := list[0], list[1], list[2]
	if safari.ID != "Safari27.0TahoeAuto-27.0" || safari.Package != "Safari" || safari.FixedVersion != "27.0" ||
		safari.Classification != "update" || safari.RestartRequired {
		t.Errorf("Safari errado: %+v", safari)
	}
	if tahoe.Package != "macOS" || tahoe.FixedVersion != "26.7.1" || tahoe.Classification != "update" || !tahoe.RestartRequired {
		t.Errorf("atualização menor do macOS errada: %+v", tahoe)
	}
	if major.Package != "macOS" || major.FixedVersion != "27.0.1" || major.Classification != "upgrade" || !major.RestartRequired {
		t.Errorf("troca de versão principal errada: %+v", major)
	}
	if tahoe.Source != SourceSoftwareUpdate || tahoe.Type != "software" {
		t.Errorf("fonte ou tipo errados: %+v", tahoe)
	}
}

func TestParseSoftwareUpdateListSemNada(t *testing.T) {
	saida := "Software Update Tool\n\nFinding available software\nNo new software available.\n"
	if list := parseSoftwareUpdateList([]byte(saida), "26.7.1"); list != nil {
		t.Errorf("sem itens, o parser devolve nil (o scanner troca por []): %+v", list)
	}
}

func TestParseSoftwareUpdateFieldsTituloComVirgulaECampoNovo(t *testing.T) {
	f := parseSoftwareUpdateFields("Title: Ferramentas, Edição Pro, Version: 1.2, Deferred: NO, Size: 10KiB,")
	if f["Title"] != "Ferramentas, Edição Pro" || f["Version"] != "1.2" || f["Deferred"] != "NO" {
		t.Errorf("campos errados: %v", f)
	}
}

func TestMajorOf(t *testing.T) {
	casos := map[string]int{"26.7.1": 26, "27.0": 27, " 15 ": 15, "": 0, "Tahoe": 0}
	for v, quer := range casos {
		if got := majorOf(v); got != quer {
			t.Errorf("majorOf(%q) = %d, esperado %d", v, got, quer)
		}
	}
}

// Entradas reais do histórico do Mac do laboratório (Aula 0.3), fora de ordem de propósito.
const historicoMacReal = `{
  "SPInstallHistoryDataType" : [
    {
      "_name" : "MAContent10_AssetPack_0048_AlchemyPadsDigitalHolyGhost",
      "install_date" : "2025-01-26T01:16:31Z",
      "install_version" : "2.0.0.0",
      "package_source" : "package_source_apple"
    },
    {
      "_name" : "macOS 15.2",
      "install_date" : "2025-01-26T01:14:30Z",
      "install_version" : "15.2",
      "package_source" : "package_source_apple"
    },
    {
      "_name" : "Agente Exemplo",
      "install_date" : "2026-07-22T16:40:06Z",
      "install_version" : "8.2.1",
      "package_source" : "package_source_other"
    }
  ]
}`

func TestParseInstallHistoryReal(t *testing.T) {
	list, err := parseInstallHistory([]byte(historicoMacReal), 2)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("o limite de 2 deveria ser respeitado: %d", len(list))
	}
	if list[0].ID != "Agente Exemplo" || list[0].Title != "Agente Exemplo 8.2.1" || list[0].ClientApplicationID != "package_source_other" {
		t.Errorf("a mais recente deveria vir primeiro, com a versão no título: %+v", list[0])
	}
	if !list[1].Date.Equal(time.Date(2025, 1, 26, 1, 16, 31, 0, time.UTC)) || list[1].Result != "succeeded" {
		t.Errorf("segunda entrada errada: %+v", list[1])
	}
}

func TestParseInstallHistoryNaoRepeteVersao(t *testing.T) {
	list, err := parseInstallHistory([]byte(historicoMacReal), 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range list {
		if e.ID == "macOS 15.2" && e.Title != "macOS 15.2" {
			t.Errorf("a versão já está no nome; não deveria repetir: %q", e.Title)
		}
	}
}

func TestParseInstallHistoryDataRuim(t *testing.T) {
	dados := `{"SPInstallHistoryDataType":[{"_name":"x","install_date":"ontem"}]}`
	list, err := parseInstallHistory([]byte(dados), 10)
	if len(list) != 0 || err == nil || !strings.Contains(err.Error(), "data ilegível") {
		t.Errorf("data ilegível: entrada fora e erro: %+v, %v", list, err)
	}
}
