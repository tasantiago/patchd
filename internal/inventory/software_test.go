package inventory

import "testing"

var maquina64 = uninstallLocation{
	Path:  `HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`,
	Scope: "machine",
	Arch:  "x64",
}

func TestSoftwareFromUninstallMSI(t *testing.T) {
	e := uninstallEntry{
		KeyName:          "{23170F69-40C1-2702-2409-000001000000}",
		DisplayName:      " 7-Zip 24.09 (x64 edition) ",
		DisplayVersion:   "24.09.00.0",
		Publisher:        "Igor Pavlov",
		InstallDate:      "20260915",
		WindowsInstaller: 1,
	}
	s, ok := softwareFromUninstall(e, maquina64)
	if !ok {
		t.Fatal("entrada válida descartada")
	}
	if s.Name != "7-Zip 24.09 (x64 edition)" || s.Version != "24.09.00.0" || s.InstallDate != "2026-09-15" {
		t.Errorf("campos básicos errados: %+v", s)
	}
	if s.ProductCode != "{23170F69-40C1-2702-2409-000001000000}" {
		t.Errorf("ProductCode = %q", s.ProductCode)
	}
	if s.Source != `HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\{23170F69-40C1-2702-2409-000001000000}` {
		t.Errorf("Source = %q", s.Source)
	}
	if s.Scope != "machine" || s.Arch != "x64" || s.IsUpdate || s.SystemComponent {
		t.Errorf("escopo ou marcações erradas: %+v", s)
	}
}

func TestSoftwareFromUninstallDescartaSemNome(t *testing.T) {
	if _, ok := softwareFromUninstall(uninstallEntry{KeyName: "Sem nome", DisplayName: "   "}, maquina64); ok {
		t.Error("entrada sem DisplayName deveria ser descartada")
	}
}

func TestSoftwareFromUninstallMarcacoes(t *testing.T) {
	casos := []struct {
		nome       string
		e          uninstallEntry
		querUpdate bool
		querSystem bool
		querCodigo string
	}{
		{"atualização por ParentKeyName",
			uninstallEntry{KeyName: "KB0000001", DisplayName: "Atualização do Produto X", ParentKeyName: "ProdutoX"},
			true, false, ""},
		{"atualização por ReleaseType",
			uninstallEntry{KeyName: "KB0000002", DisplayName: "Correção do Produto Y", ReleaseType: "Security Update"},
			true, false, ""},
		{"componente de sistema",
			uninstallEntry{KeyName: "Componente", DisplayName: "Runtime Z", SystemComponent: 1},
			false, true, ""},
		{"MSI com nome de chave que não é GUID",
			uninstallEntry{KeyName: "ProdutoW", DisplayName: "Produto W", WindowsInstaller: 1},
			false, false, ""},
		{"GUID sem ser MSI",
			uninstallEntry{KeyName: "{23170F69-40C1-2702-2409-000001000000}", DisplayName: "Produto V"},
			false, false, ""},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			s, ok := softwareFromUninstall(c.e, maquina64)
			if !ok {
				t.Fatal("entrada válida descartada")
			}
			if s.IsUpdate != c.querUpdate || s.SystemComponent != c.querSystem || s.ProductCode != c.querCodigo {
				t.Errorf("update=%t system=%t código=%q, esperado %t %t %q",
					s.IsUpdate, s.SystemComponent, s.ProductCode, c.querUpdate, c.querSystem, c.querCodigo)
			}
		})
	}
}

func TestNormalizeInstallDate(t *testing.T) {
	casos := map[string]string{
		"20260928":   "2026-09-28",
		" 20260101 ": "2026-01-01",
		"20261332":   "", // mês e dia inválidos
		"28/09/2026": "", // formato regional gravado por alguns instaladores
		"":           "",
	}
	for entrada, quer := range casos {
		if got := normalizeInstallDate(entrada); got != quer {
			t.Errorf("normalizeInstallDate(%q) = %q, esperado %q", entrada, got, quer)
		}
	}
}

func TestIsGUID(t *testing.T) {
	validos := []string{"{23170F69-40C1-2702-2409-000001000000}", "{abcdef01-2345-6789-abcd-ef0123456789}"}
	invalidos := []string{"23170F69-40C1-2702-2409-000001000000", "{23170F69-40C1-2702-2409-00000100000}", "{23170F69X40C1-2702-2409-000001000000}", "{GGGGGGGG-40C1-2702-2409-000001000000}", "7-Zip"}
	for _, s := range validos {
		if !isGUID(s) {
			t.Errorf("isGUID(%q) = false", s)
		}
	}
	for _, s := range invalidos {
		if isGUID(s) {
			t.Errorf("isGUID(%q) = true", s)
		}
	}
}

func TestIsUserSID(t *testing.T) {
	casos := map[string]bool{
		"S-1-5-21-1111111111-2222222222-3333333333-1001":         true,
		"S-1-5-21-1111111111-2222222222-3333333333-1001_Classes": false,
		"S-1-5-18": false, // SYSTEM
		".DEFAULT": false,
	}
	for sid, quer := range casos {
		if got := isUserSID(sid); got != quer {
			t.Errorf("isUserSID(%q) = %t, esperado %t", sid, got, quer)
		}
	}
}

func TestSortSoftwareEstavel(t *testing.T) {
	lista := []Software{
		{Name: "zeta", Source: "b"},
		{Name: "Alfa", Version: "2", Source: "a"},
		{Name: "alfa", Version: "1", Source: "c"},
	}
	sortSoftware(lista)
	if lista[0].Version != "1" || lista[1].Version != "2" || lista[2].Name != "zeta" {
		t.Errorf("ordem inesperada: %+v", lista)
	}
}
