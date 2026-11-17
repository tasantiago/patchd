package thirdparty

import (
	"encoding/json"
	"strings"
	"testing"
)

// O formulário do painel vira uma regra do manifesto: o id do pacote, o nome e o fabricante
// como texto (sem virar expressão por engano), e a mesma conferência das regras do manifesto.
func TestProgramaDoPainel(t *testing.T) {
	in := CustomInput{Name: "VLC", WingetID: "VideoLAN.VLC", InventoryName: "VLC media player (3.0)", NameMode: NameEquals,
		Publisher: "VideoLAN", InstallType: "nullsoft", Signer: "VideoLAN", Processes: []string{"vlc.exe"}}
	a, err := BuildCustom(in)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != "videolan-vlc" || a.OS != "windows" || a.Category != CategoryFeed || a.Source.ID != "VideoLAN.VLC" || a.Install.Winget != "VideoLAN.VLC" {
		t.Errorf("regra: %+v", a)
	}
	// O parêntese do nome é texto, não grupo.
	if !a.Matches("VLC media player (3.0)", "VideoLAN") || a.Matches("VLC media player 3.0", "VideoLAN") || a.Matches("VLC media player (3.0)", "Outro") {
		t.Errorf("nome igual: %q", a.Match)
	}
	in.NameMode, in.InventoryName = NamePrefix, "Zoom Workplace"
	a, _ = BuildCustom(in)
	if !a.Matches("Zoom Workplace", "VideoLAN") || !a.Matches("Zoom Workplace (64-bit)", "VideoLAN") || a.Matches("Zoom WorkplaceX", "VideoLAN") {
		t.Errorf("começa com: %q", a.Match)
	}
	if CustomID("Notepad++.Notepad++") != "notepad-notepad" || CustomID("Mozilla.Firefox.pt-BR") != "mozilla-firefox-pt-br" {
		t.Error("CustomID")
	}
	for nome, ruim := range map[string]CustomInput{
		"sem nome":                     {WingetID: "A.B", InventoryName: "x", Signer: "s"},
		"pacote fora da forma":         {Name: "x", WingetID: "semponto", InventoryName: "x", Signer: "s"},
		"sem assinatura nem motivo":    {Name: "x", WingetID: "A.B", InventoryName: "x"},
		"os dois":                      {Name: "x", WingetID: "A.B", InventoryName: "x", Signer: "s", UnsignedReason: "r"},
		"tipo que o patchd não usa":    {Name: "x", WingetID: "A.B", InventoryName: "x", Signer: "s", InstallType: "msix"},
		"processo que não é exe":       {Name: "x", WingetID: "A.B", InventoryName: "x", Signer: "s", Processes: []string{"../x"}},
		"duas linhas":                  {Name: "x\ny", WingetID: "A.B", InventoryName: "x", Signer: "s"},
		"modo desconhecido":            {Name: "x", WingetID: "A.B", InventoryName: "x", Signer: "s", NameMode: "regex"},
		"espaço no começo do nome":     {Name: " x", WingetID: "A.B", InventoryName: "x", Signer: "s"},
		"nome no inventário em branco": {Name: "x", WingetID: "A.B", InventoryName: "", Signer: "s"},
	} {
		if _, err := BuildCustom(ruim); err == nil {
			t.Errorf("%s: aceito", nome)
		}
	}
}

// As regras do banco somadas ao manifesto: a estragada, a repetida e a que cobre um programa
// do manifesto ficam de fora, sem derrubar as outras.
func TestRegrasDoBancoSomadas(t *testing.T) {
	base, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	vlc, _ := BuildCustom(CustomInput{Name: "VLC", WingetID: "VideoLAN.VLC", InventoryName: "VLC media player", Signer: "VideoLAN"})
	boa, _ := json.Marshal(vlc)
	chrome := App{ID: "google-chrome", OS: "windows", Name: "Chrome falso", Category: CategoryFeed, Match: "^x$",
		Source: &Source{Type: SourceWinget, ID: "Google.Chrome"}}
	repetida, _ := json.Marshal(chrome)
	m, errs := WithCustom(base, []json.RawMessage{boa, repetida, json.RawMessage(`{"id":"x","campo_errado":1}`), boa})
	if len(m.Apps) != len(base.Apps)+1 || !m.Has("videolan-vlc/windows") || len(errs) != 3 {
		t.Fatalf("%d regras (base %d), erros %v", len(m.Apps), len(base.Apps), errs)
	}
	if got := m.For("windows"); !got[len(got)-1].Matches("VLC media player", "") {
		t.Error("a regra do banco não ficou compilada")
	}
	if len(base.Apps) == len(m.Apps) || strings.Contains(m.Apps[0].ID, "videolan") {
		t.Error("o manifesto base mudou")
	}
	// O manifesto vivo: troca inteira, geração contada.
	l := NewLive(base)
	g := l.Gen()
	l.Set(m)
	if l.Gen() != g+1 || len(l.For("windows")) != len(m.For("windows")) {
		t.Error("Live")
	}
	var vazio Live
	if len(vazio.Get().Apps) != 0 {
		t.Error("Live sem manifesto")
	}
}
