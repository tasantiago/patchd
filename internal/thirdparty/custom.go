package thirdparty

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/tasantiago/patchd/internal/winget"
)

// Programas incluídos pelo painel (Patch › Programas de terceiros › Incluir programa): a regra
// sai de um formulário, não de um JSON escrito à mão. Só o caso simples, que é o comum: um
// programa do Windows que o winget-pkgs publica, com a versão de referência e o instalador do
// mesmo pacote. O nome no inventário e o fabricante são texto (o painel monta a expressão
// regular); a regra passa pela mesma conferência das do manifesto (Parse).

// Modos de comparar o nome no inventário.
const (
	NameEquals = "igual"  // o nome inteiro
	NamePrefix = "comeca" // o começo, seguido de espaço ou do fim ("Zoom Workplace (64-bit)")
)

// CustomInput é o formulário.
type CustomInput struct {
	Name           string // o rótulo no painel e na Central
	WingetID       string // VideoLAN.VLC
	InventoryName  string // como aparece em Programas e Recursos
	NameMode       string // NameEquals ou NamePrefix
	Publisher      string // o fabricante em Programas e Recursos; vazio: qualquer
	InstallType    string // msi, wix, nullsoft, inno, burn, exe; vazio: o primeiro do winget
	Signer         string // a organização (O=) da assinatura do instalador
	UnsignedReason string // ou: por que o instalador não tem assinatura
	Processes      []string
	// Upload: o instalador próprio, enviado pelo painel (sem winget). O id vem do nome, ou é
	// ID (a edição mantém o id de quando foi incluído).
	Upload bool
	ID     string
	// O atalho na área de trabalho de todos os usuários (opcional): o nome, o executável e o
	// ícone. Sem o executável, não há atalho.
	ShortcutName, ShortcutTarget, ShortcutIcon string
}

// customID: o id da regra, derivado do pacote do winget ("VideoLAN.VLC" → "videolan-vlc").
var customIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// CustomID devolve o id da regra do pacote do winget.
func CustomID(wingetID string) string {
	id := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		}
		return '-'
	}, strings.TrimSpace(wingetID))
	for strings.Contains(id, "--") {
		id = strings.ReplaceAll(id, "--", "-")
	}
	id = strings.Trim(id, "-")
	if len(id) > 64 {
		id = strings.TrimRight(id[:64], "-")
	}
	return id
}

func oneLine(field, s string, max int, required bool) error {
	switch {
	case required && strings.TrimSpace(s) == "":
		return fmt.Errorf("%s é obrigatório", field)
	case len([]rune(s)) > max || strings.ContainsAny(s, "\r\n\t") || strings.TrimSpace(s) != s:
		return fmt.Errorf("%s: uma linha, até %d caracteres, sem espaço no começo ou no fim", field, max)
	}
	return nil
}

// BuildCustom monta e confere a regra do formulário. A regra devolvida já passou pela
// conferência do manifesto (com as expressões compiladas).
func BuildCustom(in CustomInput) (App, error) {
	for _, c := range []struct {
		field, v string
		max      int
		required bool
	}{{"o nome", in.Name, 80, true}, {"o pacote do winget", in.WingetID, 128, !in.Upload}, {"o nome no inventário", in.InventoryName, 160, true},
		{"o fabricante", in.Publisher, 160, false}, {"o assinante", in.Signer, 128, false}, {"o motivo sem assinatura", in.UnsignedReason, 300, false}} {
		if err := oneLine(c.field, c.v, c.max, c.required); err != nil {
			return App{}, err
		}
	}
	id := ""
	switch {
	case in.Upload:
		if in.WingetID != "" {
			return App{}, errors.New("o instalador próprio não tem pacote do winget")
		}
		id = CustomID(in.Name)
		if in.ID != "" {
			id = in.ID
		}
		if !customIDPattern.MatchString(id) {
			return App{}, errors.New("o nome não dá um identificador válido: use letras e números (ex.: IRPF 2026)")
		}
	case !winget.ValidID(in.WingetID):
		return App{}, fmt.Errorf("o pacote do winget %q fora da forma Fabricante.Programa (ex.: VideoLAN.VLC)", in.WingetID)
	default:
		id = CustomID(in.WingetID)
		if !customIDPattern.MatchString(id) {
			return App{}, errors.New("o pacote do winget não dá um identificador válido")
		}
	}
	match := "^" + regexp.QuoteMeta(in.InventoryName)
	switch in.NameMode {
	case NameEquals, "":
		match += "$"
	case NamePrefix:
		match += "( |$)"
	default:
		return App{}, fmt.Errorf("modo de comparar o nome %q: use %s ou %s", in.NameMode, NameEquals, NamePrefix)
	}
	a := App{ID: id, OS: "windows", Name: in.Name, Category: CategoryFeed, Match: match,
		Source:  &Source{Type: SourceWinget, ID: in.WingetID},
		Install: &Install{Winget: in.WingetID, Type: in.InstallType, Signer: in.Signer, UnsignedReason: in.UnsignedReason, Processes: in.Processes}}
	if in.Upload {
		if !slices.Contains(UploadTypes, in.InstallType) {
			return App{}, fmt.Errorf("o tipo do instalador próprio: %s", strings.Join(UploadTypes, ", "))
		}
		a.Source = &Source{Type: SourceUpload, ID: id}
		a.Install.Upload = true
	}
	switch {
	case in.ShortcutTarget != "":
		name := in.ShortcutName
		if name == "" {
			name = in.Name
		}
		a.Shortcut = &Shortcut{Name: name, Target: in.ShortcutTarget, Icon: in.ShortcutIcon}
	case in.ShortcutName != "" || in.ShortcutIcon != "":
		return App{}, errors.New("o atalho precisa do caminho do executável")
	}
	if in.Publisher != "" {
		a.Publisher = "^" + regexp.QuoteMeta(in.Publisher) + "$"
	}
	m, err := ParseCustom([]App{a})
	if err != nil {
		return App{}, err
	}
	return m.Apps[0], nil
}

// ParseCustom confere as regras guardadas (como as do manifesto) e as devolve num manifesto.
func ParseCustom(apps []App) (Manifest, error) {
	data, err := json.Marshal(Manifest{Version: 1, Apps: apps})
	if err != nil {
		return Manifest{}, err
	}
	m, err := Parse(data)
	if err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// Has: o manifesto tem a regra (id/SO).
func (m Manifest) Has(key string) bool {
	for _, a := range m.Apps {
		if a.Key() == key {
			return true
		}
	}
	return false
}

// WithCustom soma ao manifesto as regras guardadas pelo painel (JSON de App). A regra
// ilegível, que não passa na conferência ou que repete um programa do manifesto vai para os
// erros e fica de fora: um programa estragado no banco não derruba os outros.
func WithCustom(base Manifest, rules []json.RawMessage) (Manifest, []error) {
	var ok []App
	var errs []error
	for _, raw := range rules {
		var a App
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&a); err != nil {
			errs = append(errs, fmt.Errorf("programa incluído ilegível: %w", err))
			continue
		}
		if a.OS != "windows" || base.Has(a.Key()) || slices.ContainsFunc(ok, func(x App) bool { return x.Key() == a.Key() }) {
			errs = append(errs, fmt.Errorf("programa incluído %s: repete um programa do manifesto ou não é do Windows", a.ID))
			continue
		}
		m, err := ParseCustom([]App{a})
		if err != nil {
			errs = append(errs, fmt.Errorf("programa incluído %s: %w", a.ID, err))
			continue
		}
		ok = append(ok, m.Apps[0])
	}
	out := Manifest{Version: base.Version, Apps: append(slices.Clone(base.Apps), ok...)}
	return out, errs
}
