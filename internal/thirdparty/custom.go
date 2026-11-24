package thirdparty

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/tasantiago/patchd/internal/choco"
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
	// UninstallFirst: a atualização remove a instalação de agora antes de instalar.
	UninstallFirst bool
	// PerUser: o instalador instala no perfil de quem o roda; só a Central o instala, como o
	// usuário que pediu (Install.PerUser).
	PerUser bool
	// NoVersion: o programa não informa a versão (Install.NoVersion).
	NoVersion bool
	// Arguments (winget): os argumentos da instalação silenciosa no lugar dos do winget (o
	// "/S /allusers" que faz o instalador Electron de escopo usuário instalar para todos).
	Arguments string
	// Upload: o instalador próprio, enviado pelo painel (sem winget). O id vem do nome, ou é
	// ID (a edição mantém o id de quando foi incluído).
	Upload bool
	ID     string
	// Chocolatey: o pacote do Chocolatey de onde vem o instalador (e a versão de referência),
	// sem winget. O id da regra vem do pacote.
	Chocolatey string
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
	}{{"o nome", in.Name, 80, true}, {"o pacote do winget", in.WingetID, 128, !in.Upload && in.Chocolatey == ""}, {"o nome no inventário", in.InventoryName, 160, true},
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
	case in.Chocolatey != "":
		if in.WingetID != "" {
			return App{}, errors.New("o programa do Chocolatey não tem pacote do winget")
		}
		if !choco.ValidID(in.Chocolatey) {
			return App{}, fmt.Errorf("o pacote do Chocolatey %q: o id da página do pacote (ex.: firefox, adobereader)", in.Chocolatey)
		}
		id = CustomID(in.Chocolatey)
		if !customIDPattern.MatchString(id) {
			return App{}, errors.New("o pacote do Chocolatey não dá um identificador válido")
		}
		if in.InstallType == "" {
			return App{}, errors.New("escolha o tipo do instalador que o pacote do Chocolatey baixa (msi, nullsoft, inno ou exe)")
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
		Source: &Source{Type: SourceWinget, ID: in.WingetID},
		Install: &Install{Winget: in.WingetID, Type: in.InstallType, Signer: in.Signer, UnsignedReason: in.UnsignedReason, Processes: in.Processes,
			UninstallFirst: in.UninstallFirst, PerUser: in.PerUser, NoVersion: in.NoVersion}}
	if in.Chocolatey != "" {
		a.Source = &Source{Type: SourceChocolatey, ID: in.Chocolatey}
		a.Install.Chocolatey = in.Chocolatey
	}
	if !in.Upload && strings.TrimSpace(in.Arguments) != "" {
		if in.InstallType == "" {
			return App{}, fmt.Errorf("com argumentos próprios, escolha o tipo do instalador (os argumentos são de um tipo)")
		}
		a.Install.Arguments = strings.TrimSpace(in.Arguments)
	}
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

// SignerOrganization: o campo "organização que assina" aceita o Subject inteiro, como o
// PowerShell mostra (Get-AuthenticodeSignature … .SignerCertificate.Subject: "C=US, O=Vault
// Id …, OU=…, CN=…"), e fica só com o valor de O= (sem aspas). Sem O= no texto, volta igual.
func SignerOrganization(s string) string {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "=") {
		return s
	}
	var parts []string
	var cur strings.Builder
	quoted := false
	for _, r := range s {
		switch {
		case r == '"':
			quoted = !quoted
			cur.WriteRune(r)
		case (r == ',' || r == ';') && !quoted:
			parts = append(parts, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	parts = append(parts, cur.String())
	for _, p := range parts {
		k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), "O") {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
			v = strings.ReplaceAll(v[1:len(v)-1], `""`, `"`)
		}
		return v
	}
	return s
}

// WithTryOpen devolve o manifesto com o "tentar com o programa aberto" de cada programa do
// Windows que o painel escolheu (id → ligado). O programa sem processes fica como está (não há
// o que conferir). O manifesto de entrada não muda: a regra trocada é uma cópia.
func WithTryOpen(m Manifest, set map[string]bool) Manifest {
	if len(set) == 0 {
		return m
	}
	out := Manifest{Version: m.Version, Apps: slices.Clone(m.Apps)}
	for i, a := range out.Apps {
		on, ok := set[a.ID]
		if !ok || a.OS != "windows" || a.Install == nil || len(a.Install.Processes) == 0 || a.Install.TryOpen == on {
			continue
		}
		in := *a.Install
		in.TryOpen = on
		out.Apps[i].Install = &in
	}
	return out
}
