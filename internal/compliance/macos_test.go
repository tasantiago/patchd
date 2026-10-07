package compliance

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func ymd(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

// As majors do catálogo do laboratório (consulta 1 da Aula 6.4, parte 4a).
func majorsDoLab() []MacOSMajor {
	return []MacOSMajor{
		{Number: 27, Name: "Golden Gate 27", Launched: ymd("2026-09-14"), Latest: "27.0.1", LatestDate: ymd("2026-09-28")},
		{Number: 26, Name: "Tahoe 26", Launched: ymd("2025-09-15"), Latest: "26.7.1", LatestDate: ymd("2026-09-28")},
		{Number: 15, Name: "Sequoia 15", Launched: ymd("2024-09-16"), Latest: "15.8.1", LatestDate: ymd("2026-09-28")},
		{Number: 14, Name: "Sonoma 14", Launched: ymd("2023-09-26"), Latest: "14.8.9", LatestDate: ymd("2026-08-06")},
		{Number: 13, Name: "Ventura 13", Launched: ymd("2022-10-24"), Latest: "13.7.8", LatestDate: ymd("2025-08-20")},
		{Number: 12, Name: "Monterey 12", Launched: ymd("2021-09-20"), Latest: "12.7.6", LatestDate: ymd("2024-07-29")},
	}
}

func TestMacOSSupportOf(t *testing.T) {
	hoje := ymd("2026-10-07")
	for major, want := range map[int]SupportState{
		27: Supported, 26: Supported, 15: Supported,
		14: SupportEnded, 13: SupportEnded, 12: SupportEnded,
		11: SupportEnded,   // anterior ao catálogo
		28: SupportUnknown, // mais nova que o catálogo
	} {
		if got := MacOSSupportOf(majorsDoLab(), major, hoje); got.State != want {
			t.Errorf("macOS %d: %v, quero %v (%s)", major, got.State, want, got.Note)
		}
	}
	s := MacOSSupportOf(majorsDoLab(), 14, hoje)
	if !strings.Contains(s.Note, "a última versão de segurança é a 14.8.9, de 2026-08-06, anterior ao lançamento do macOS 27 (2026-09-14)") {
		t.Errorf("nota do Sonoma: %q", s.Note)
	}
	if s := MacOSSupportOf(nil, 26, hoje); s.State != SupportUnknown || !strings.Contains(s.Note, "vazio") {
		t.Errorf("catálogo vazio: %+v", s)
	}

	// Logo depois do 27.0, o Sequoia ainda sem a versão do ciclo: a confirmar por 30 dias,
	// depois encerrado. O Sonoma (três majors atrás) não ganha esse prazo.
	atrasado := majorsDoLab()
	atrasado[2].Latest, atrasado[2].LatestDate = "15.7.9", ymd("2026-08-06")
	if s := MacOSSupportOf(atrasado, 15, ymd("2026-09-20")); s.State != SupportUnconfirmed {
		t.Errorf("15 no prazo: %+v", s)
	}
	if s := MacOSSupportOf(atrasado, 15, ymd("2026-10-20")); s.State != SupportEnded {
		t.Errorf("15 depois do prazo: %+v", s)
	}
	if s := MacOSSupportOf(atrasado, 14, ymd("2026-09-20")); s.State != SupportEnded {
		t.Errorf("14 no prazo: %+v", s)
	}
}

// As correções do Tahoe 26 no formato do catálogo. As versões e datas são as do laboratório;
// as CVEs não exploradas são ilustrativas. A CVE-2026-65400 aparece no 26.6.1 e de novo no
// 26.7, como no SOFA; o 26.5.1 não corrige CVE e não aparece.
func correcoesDoTahoe() []MacOSFix {
	return []MacOSFix{
		{Version: "26.6", Released: ymd("2026-07-27"), CVE: "CVE-2026-60003"},
		{Version: "26.6.1", Released: ymd("2026-08-06"), CVE: "CVE-2026-65400", Exploited: true, KEV: true},
		{Version: "26.6.2", Released: ymd("2026-08-17"), CVE: "CVE-2026-60002", Severity: "High"},
		{Version: "26.7", Released: ymd("2026-09-14"), CVE: "CVE-2026-65400", Exploited: true, KEV: true},
		{Version: "26.7", Released: ymd("2026-09-14"), CVE: "CVE-2026-60001", Severity: "Critical"},
		{Version: "26.7.1", Released: ymd("2026-09-28"), CVE: "CVE-2026-86950", Exploited: true, KEV: true},
	}
}

func TestEvaluateMacOS(t *testing.T) {
	ids := func(l []MacOSCVE) []string {
		var out []string
		for _, c := range l {
			out = append(out, c.CVE)
		}
		return out
	}

	// 26.6.2: a 65400 já foi corrigida no 26.6.1 (a menor correção); faltam as de setembro.
	r := EvaluateMacOS("26.6.2", "26.7.1", correcoesDoTahoe())
	if r.CVEs != 5 || r.Target != "26.7.1" || r.Ahead || !slices.Equal(ids(r.Pending), []string{"CVE-2026-86950", "CVE-2026-60001"}) {
		t.Errorf("26.6.2: %+v", r)
	}

	// 26.6: KEV primeiro (por ID), depois a Critical e a High; sem severidade por último.
	r = EvaluateMacOS("26.6", "26.7.1", correcoesDoTahoe())
	if !slices.Equal(ids(r.Pending), []string{"CVE-2026-65400", "CVE-2026-86950", "CVE-2026-60001", "CVE-2026-60002"}) {
		t.Errorf("26.6: %v", ids(r.Pending))
	}
	if c := r.Pending[0]; c.Min != "26.6.1" || c.Max != "26.7" || !c.Released.Equal(ymd("2026-08-06")) {
		t.Errorf("65400: %+v", c)
	}

	// Em dia, à frente do catálogo e versão de correção ilegível.
	if r = EvaluateMacOS("26.7.1", "26.7.1", correcoesDoTahoe()); len(r.Pending) != 0 || r.Target != "" {
		t.Errorf("26.7.1: %+v", r)
	}
	if r = EvaluateMacOS("26.8", "26.7.1", correcoesDoTahoe()); !r.Ahead || len(r.Pending) != 0 {
		t.Errorf("26.8: %+v", r)
	}
	ruim := append(correcoesDoTahoe(), MacOSFix{Version: "26.7-beta", CVE: "CVE-2026-1"})
	if r = EvaluateMacOS("26.7.1", "26.7.1", ruim); len(r.Invalid) != 1 {
		t.Errorf("versão ilegível: %+v", r)
	}
}

func TestMacOSHardware(t *testing.T) {
	hoje := ymd("2026-10-07")
	modelos := map[string]MacModel{
		"Mac14,14":       {ID: "Mac14,14", Majors: []int{27, 26, 15, 14, 13}},
		"Mac16,1":        {ID: "Mac16,1", Majors: []int{27, 26, 15}},
		"MacBookPro16,1": {ID: "MacBookPro16,1", Majors: []int{26, 15, 14, 13, 12}},
		"MacBookPro15,1": {ID: "MacBookPro15,1", Majors: []int{15, 14, 13, 12}},
		"iMac19,1":       {ID: "iMac19,1", Majors: []int{15, 14, 13, 12}},
		"MacBookAir8,1":  {ID: "MacBookAir8,1", Majors: []int{14, 13, 12}},
	}
	casos := []struct {
		modelo string
		major  int
		rotulo string
		trecho string
	}{
		{"Mac14,14", 14, "ATUALIZAR A MAJOR", "aceita até o macOS 27"},
		{"MacBookPro15,1", 13, "ATUALIZAR A MAJOR", "aceita até o macOS 15"},
		{"MacBookAir8,1", 14, "HARDWARE FORA DE SUPORTE", "(macOS 14) não recebe mais"},
		{"iMac19,1", 15, "AVISO", "não passa do macOS 15"},
		{"MacBookPro16,1", 15, "", "aceita até o macOS 26 (atualizar a major é opcional"},
		{"Mac16,1", 27, "", "aceita até o macOS 27"},
	}
	for _, c := range casos {
		sup := MacOSSupportOf(majorsDoLab(), c.major, hoje)
		rotulo, texto := MacOSHardware(modelos[c.modelo], c.major, sup, majorsDoLab(), hoje)
		if rotulo != c.rotulo || !strings.Contains(texto, c.trecho) {
			t.Errorf("%s no %d: %q %q", c.modelo, c.major, rotulo, texto)
		}
	}
	if _, texto := MacOSHardware(MacModel{ID: "Mac1,1"}, 26, MacOSSupportOf(majorsDoLab(), 26, hoje), majorsDoLab(), hoje); !strings.Contains(texto, "não informa") {
		t.Errorf("modelo sem majors: %q", texto)
	}
}

func TestMacOSMajorOf(t *testing.T) {
	for in, want := range map[string]int{"26.6.2": 26, "27": 27, " 14.8.9 ": 14} {
		if got, ok := MacOSMajorOf(in); !ok || got != want {
			t.Errorf("%q: %d %t", in, got, ok)
		}
	}
	for _, in := range []string{"", "Tahoe", "x.1", "0.1"} {
		if _, ok := MacOSMajorOf(in); ok {
			t.Errorf("%q deveria ser recusada", in)
		}
	}
}
