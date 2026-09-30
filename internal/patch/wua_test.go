package patch

import (
	"reflect"
	"testing"
	"time"
)

func TestToMissingSeguranca(t *testing.T) {
	u := wuaUpdate{
		UpdateID: "11111111-2222-3333-4444-555555555555",
		Revision: 200,
		Title:    "2026-10 Atualização de Segurança para Windows 11 Version 25H2 (KB5130000) (26200.9600)",
		KBs:      []string{"5130000"},
		CVEs:     []string{"CVE-2026-00001", "CVE-2026-00002"},
		Severity: "Critical",
		Type:     1,
		Categories: []wuaCategory{
			{ID: "A3C2375D-0C8A-42F9-BCE0-28333E198407", Name: "Windows 10, version 1903 and later", Type: "Product"},
			{ID: "0FA1201D-4330-4FA8-8AE9-B877473B6441", Name: "Atualizações de Segurança", Type: "UpdateClassification"},
		},
		Released:   time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC),
		Downloaded: true,
	}
	m := toMissing(u)
	if m.Classification != "security" || m.ClassificationID != "0fa1201d-4330-4fa8-8ae9-b877473b6441" {
		t.Errorf("classificação deveria vir pelo GUID, não pelo nome traduzido: %+v", m)
	}
	if m.Type != "software" || m.Severity != "Critical" || m.ReleasedAt != "2026-10-13" || !m.Downloaded {
		t.Errorf("campos errados: %+v", m)
	}
	if !reflect.DeepEqual(m.KBs, []string{"5130000"}) || len(m.CVEs) != 2 || m.Source != wuaSearchSource {
		t.Errorf("KB, CVEs ou fonte errados: %+v", m)
	}
}

func TestToMissingDriverEClassificacaoDesconhecida(t *testing.T) {
	u := wuaUpdate{
		UpdateID:   "x",
		Type:       2,
		Categories: []wuaCategory{{ID: "99999999-0000-0000-0000-000000000000", Name: "Nova", Type: "UpdateClassification"}},
	}
	m := toMissing(u)
	if m.Type != "driver" {
		t.Errorf("Type 2 deveria ser driver: %q", m.Type)
	}
	if m.Classification != "" || m.ClassificationID != "99999999-0000-0000-0000-000000000000" {
		t.Errorf("GUID desconhecido: nome vazio e GUID preservado: %+v", m)
	}
	if m.ReleasedAt != "" {
		t.Errorf("data zero não deveria virar texto: %q", m.ReleasedAt)
	}
}

func TestToHistoryComTituloReal(t *testing.T) {
	// Título real do histórico do laboratório (Aula 0.2), em português.
	h := wuaHistory{
		Date:                time.Date(2026, 9, 25, 20, 1, 5, 0, time.UTC),
		Operation:           1,
		ResultCode:          2,
		UpdateID:            "aaaa",
		Title:               "2026-09 Atualização de Segurança (KB5129195) (26200.9457)",
		ClientApplicationID: " UpdateOrchestrator ",
		ServerSelection:     1,
	}
	e := toHistory(h)
	if !reflect.DeepEqual(e.KBs, []string{"5129195"}) {
		t.Errorf("KB deveria sair do título em qualquer idioma: %v", e.KBs)
	}
	if e.Operation != "install" || e.Result != "succeeded" || e.ServerSelection != "managed_server" || e.HResult != "" {
		t.Errorf("campos errados: %+v", e)
	}
	if e.ClientApplicationID != "UpdateOrchestrator" {
		t.Errorf("ClientApplicationID sem espaços: %q", e.ClientApplicationID)
	}
}

func TestToHistoryFalhaComHResult(t *testing.T) {
	e := toHistory(wuaHistory{Operation: 2, ResultCode: 4, HResult: -2145124318, ServerSelection: 7})
	if e.Operation != "uninstall" || e.Result != "failed" {
		t.Errorf("operação ou resultado errados: %+v", e)
	}
	if e.HResult != "0x80240022" {
		t.Errorf("HResult negativo deveria virar hexadecimal de 32 bits: %q", e.HResult)
	}
	if e.ServerSelection != "desconhecido_7" {
		t.Errorf("valor fora da tabela deveria ser marcado: %q", e.ServerSelection)
	}
}

func TestExtractKBs(t *testing.T) {
	casos := map[string][]string{
		"Ferramenta de Remoção de Software Mal-intencionado do Windows x64 - v5.145 (KB890830)": {"890830"},
		"Pacote (KB5129195) e reinstalação (KB5129195), mais KB5000001":                         {"5129195", "5000001"},
		"Intel Corporation Display Driver Update (32.0.101.7088)":                               nil,
		"MKB1234567 não é KB": nil,
	}
	for titulo, quer := range casos {
		if got := extractKBs(titulo); !reflect.DeepEqual(got, quer) {
			t.Errorf("extractKBs(%q) = %v, esperado %v", titulo, got, quer)
		}
	}
}

func TestSearchResultError(t *testing.T) {
	if searchResultError(2) != nil {
		t.Error("ResultCode 2 é sucesso")
	}
	if searchResultError(3) == nil || searchResultError(4) == nil {
		t.Error("ResultCode 3 e 4 precisam gerar erro")
	}
}
