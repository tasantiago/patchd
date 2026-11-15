package sensor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
)

// As perguntas que olham cada usuário da máquina (jobs.ListSensor): o agente roda como SYSTEM
// (ou root) e alcança a pasta de todos os perfis, com sessão aberta ou não. A resposta é uma
// lista (QueryAnswer.Items), um item por achado.
//
// A pasta do usuário é dele: tudo o que se lê dali é dado, nunca caminho a seguir. Por isso
// nada aqui atravessa link simbólico, junção ou outro ponto de nova análise (um atalho para
// \\servidor\pasta faria o SYSTEM se autenticar na rede), e todo arquivo tem teto de tamanho.

// Profile é a pasta de um usuário da máquina.
type Profile struct {
	User string // o nome da pasta do perfil (o usuário, sem consultar o domínio)
	Home string
}

const (
	maxProfiles        = 500              // perfis de usuário por máquina
	maxBrowserProfiles = 300              // pastas olhadas por navegador (a User Data tem muitas que não são perfil)
	maxManifest        = 1 << 20          // manifest.json
	maxMessages        = 4 << 20          // _locales/<idioma>/messages.json
	maxPrefs           = 48 << 20         // Preferences e Secure Preferences do Chromium
	maxFirefoxList     = 32 << 20         // extensions.json do Firefox
	maxUserHash        = 32 << 20         // arquivo no perfil: até aqui vai o SHA-256
	userScanTime       = 90 * time.Second // teto de uma pergunta por usuário
)

type browserDir struct{ name, dir string } // dir: relativo à pasta do usuário, com "/"

// chromiumDirs: a pasta de dados de cada navegador de base Chromium, por sistema.
var chromiumDirs = map[string][]browserDir{
	"windows": {
		{"Chrome", "AppData/Local/Google/Chrome/User Data"},
		{"Edge", "AppData/Local/Microsoft/Edge/User Data"},
		{"Brave", "AppData/Local/BraveSoftware/Brave-Browser/User Data"},
		{"Vivaldi", "AppData/Local/Vivaldi/User Data"},
		{"Chromium", "AppData/Local/Chromium/User Data"},
		{"Opera", "AppData/Roaming/Opera Software/Opera Stable"},
		{"Opera GX", "AppData/Roaming/Opera Software/Opera GX Stable"},
	},
	"linux": {
		{"Chrome", ".config/google-chrome"},
		{"Chromium", ".config/chromium"},
		{"Chromium (snap)", "snap/chromium/common/chromium"},
		{"Edge", ".config/microsoft-edge"},
		{"Brave", ".config/BraveSoftware/Brave-Browser"},
		{"Vivaldi", ".config/vivaldi"},
		{"Opera", ".config/opera"},
	},
	"darwin": {
		{"Chrome", "Library/Application Support/Google/Chrome"},
		{"Edge", "Library/Application Support/Microsoft Edge"},
		{"Brave", "Library/Application Support/BraveSoftware/Brave-Browser"},
		{"Vivaldi", "Library/Application Support/Vivaldi"},
		{"Chromium", "Library/Application Support/Chromium"},
		{"Opera", "Library/Application Support/com.operasoftware.Opera"},
	},
}

// firefoxDirs: a pasta que guarda os perfis do Firefox, por sistema.
var firefoxDirs = map[string][]browserDir{
	"windows": {{"Firefox", "AppData/Roaming/Mozilla/Firefox/Profiles"}},
	"linux": {
		{"Firefox", ".mozilla/firefox"},
		{"Firefox (snap)", "snap/firefox/common/.mozilla/firefox"},
		{"Firefox (flatpak)", ".var/app/org.mozilla.firefox/.mozilla/firefox"},
	},
	"darwin": {{"Firefox", "Library/Application Support/Firefox/Profiles"}},
}

// plain: pasta ou arquivo de verdade (nem link, nem junção, nem arquivo só na nuvem, nem
// dispositivo).
func plain(m fs.FileMode) bool {
	return m&(fs.ModeSymlink|fs.ModeIrregular|fs.ModeDevice|fs.ModeCharDevice|fs.ModeNamedPipe|fs.ModeSocket) == 0
}

// errNotPlain: no caminho há um link (ou junção, ou arquivo só na nuvem); o agente não segue.
var errNotPlain = errors.New("link ou arquivo só na nuvem (não seguido)")

// descend anda de base para dentro, um pedaço por vez, sem seguir link: devolve o caminho e
// o que há no fim. fs.ErrNotExist: não existe; errNotPlain: havia um link no caminho.
func descend(base string, rel ...string) (string, fs.FileInfo, error) {
	path := base
	var info fs.FileInfo
	for i, part := range rel {
		path = filepath.Join(path, part)
		var err error
		if info, err = os.Lstat(path); err != nil {
			return path, nil, err
		}
		if !plain(info.Mode()) {
			return path, info, errNotPlain
		}
		if i < len(rel)-1 && !info.IsDir() {
			return path, nil, fs.ErrNotExist
		}
	}
	return path, info, nil
}

// subdirs lista as pastas de verdade dentro de dir (até max).
func subdirs(dir string, max int) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && plain(e.Type()) {
			if out = append(out, e.Name()); len(out) >= max {
				break
			}
		}
	}
	return out
}

// readSmall lê o arquivo name dentro de dir (sem seguir link), até max bytes.
func readSmall(dir, name string, max int64) ([]byte, error) {
	path, info, err := descend(dir, name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > max {
		return nil, fmt.Errorf("%s: não é arquivo comum de até %d bytes", name, max)
	}
	f, err := os.Open(path) // #nosec G304 -- nome fixo dentro de pasta conferida (descend); só leitura, com teto
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, max))
}

// userScan junta os itens de uma pergunta por usuário.
type userScan struct {
	items     []protocol.AnswerItem
	users     map[string]bool // usuários com pelo menos um item
	profiles  int             // perfis de usuário olhados
	unread    int             // pastas que o agente não conseguiu ler
	truncated bool
	needle    string // filtro, em minúsculas ("" : tudo)
}

func (u *userScan) add(it protocol.AnswerItem) {
	if u.needle != "" && !strings.Contains(strings.ToLower(it.Name), u.needle) && !strings.Contains(strings.ToLower(it.Key), u.needle) {
		return
	}
	if len(u.items) >= protocol.MaxAnswerItems {
		u.truncated = true
		return
	}
	u.items = append(u.items, it)
	u.users[it.User] = true
}

// eachProfile chama fn para cada perfil de usuário, até o prazo.
func (s Sensors) eachProfile(ctx context.Context, u *userScan, fn func(Profile)) error {
	list, err := s.Profiles()
	if err != nil {
		return err
	}
	if len(list) > maxProfiles {
		list, u.truncated = list[:maxProfiles], true
	}
	ctx, cancel := context.WithTimeout(ctx, userScanTime)
	defer cancel()
	for _, p := range list {
		if ctx.Err() != nil {
			u.truncated = true
			break
		}
		if info, err := os.Lstat(p.Home); err != nil || !info.IsDir() || !plain(info.Mode()) {
			continue
		}
		u.profiles++
		fn(p)
	}
	return nil
}

func (s Sensors) goos() string {
	if s.OS != "" {
		return s.OS
	}
	return "linux"
}

// extensions lista as extensões dos navegadores de todos os usuários (filter: um pedaço do
// nome ou do ID; vazio: todas).
func (s Sensors) extensions(ctx context.Context, filter string) jobs.QueryAnswer {
	if s.Profiles == nil {
		return jobs.QueryAnswer{Value: NotApplicable}
	}
	u := &userScan{users: map[string]bool{}, needle: strings.ToLower(filter)}
	withBrowser := 0
	err := s.eachProfile(ctx, u, func(p Profile) {
		found := false
		for _, b := range chromiumDirs[s.goos()] {
			root, info, err := descend(p.Home, strings.Split(b.dir, "/")...)
			if err != nil || !info.IsDir() {
				if err != nil && !errors.Is(err, fs.ErrNotExist) {
					u.unread++
				}
				continue
			}
			// O Opera antigo guarda o perfil na própria pasta; os outros, em subpastas.
			for _, name := range append([]string{""}, subdirs(root, maxBrowserProfiles)...) {
				dir := filepath.Join(root, name)
				if name == "" {
					name = "(único)"
				}
				if chromiumProfile(u, p.User, b.name, name, dir) {
					found = true
				}
			}
		}
		for _, b := range firefoxDirs[s.goos()] {
			root, info, err := descend(p.Home, strings.Split(b.dir, "/")...)
			if err != nil || !info.IsDir() {
				if err != nil && !errors.Is(err, fs.ErrNotExist) {
					u.unread++
				}
				continue
			}
			for _, name := range subdirs(root, maxBrowserProfiles) {
				if firefoxProfile(u, p.User, b.name, name, filepath.Join(root, name)) {
					found = true
				}
			}
		}
		if found {
			withBrowser++
		}
	})
	if err != nil {
		return jobs.QueryAnswer{Value: "erro ao listar os usuários", Extra: err.Error()}
	}
	extra := fmt.Sprintf("%d extensão(ões) de %d usuário(s); %d perfil(is) de usuário na máquina, %d com navegador",
		len(u.items), len(u.users), u.profiles, withBrowser)
	if u.unread > 0 {
		extra += fmt.Sprintf("; %d pasta(s) sem leitura", u.unread)
	}
	value := "com extensões"
	switch {
	case u.truncated:
		value = "lista incompleta (cortada)"
	case len(u.items) == 0 && filter != "":
		value = "não encontrada"
	case len(u.items) == 0:
		value = "nenhuma extensão"
	case filter != "":
		value = "encontrada"
	}
	return jobs.QueryAnswer{Value: value, Extra: extra, Items: u.items}
}

// --- Chromium --------------------------------------------------------------------------

var chromiumID = regexp.MustCompile(`^[a-p]{32}$`)

// chromiumPrefs: o pedaço do Preferences (e do Secure Preferences) que conta sobre as extensões.
type chromiumPrefs struct {
	Extensions struct {
		Settings map[string]chromiumSetting `json:"settings"`
	} `json:"extensions"`
}

type chromiumSetting struct {
	State          any    `json:"state"`           // 0: desativada (versões antigas)
	DisableReasons any    `json:"disable_reasons"` // número ou lista; vazio: ativa
	Location       any    `json:"location"`
	Path           any    `json:"path"`
	FromWebstore   any    `json:"from_webstore"`
	Manifest       any    `json:"manifest"`
	present        bool   // havia a entrada
	path           string // já como texto
}

type chromiumManifest struct {
	Name            string `json:"name"`
	Version         string `json:"version"`
	DefaultLocale   string `json:"default_locale"`
	Permissions     []any  `json:"permissions"`
	HostPermissions []any  `json:"host_permissions"`
	ContentScripts  []struct {
		Matches []any `json:"matches"`
	} `json:"content_scripts"`
}

// Onde a extensão veio (ManifestLocation do Chromium).
const (
	locInternal       = 1 // instalada pelo usuário (loja ou .crx)
	locExternalPref   = 2
	locExternalReg    = 3
	locUnpacked       = 4 // "carregar sem compactação", no modo de desenvolvedor
	locComponent      = 5 // parte do navegador
	locExternalPrefDL = 6
	locPolicyDownload = 7
	locCommandLine    = 8
	locPolicy         = 9
	locExternalComp   = 10
)

func asInt(v any) (int, bool) {
	f, ok := v.(float64)
	return int(f), ok
}

// readChromiumPrefs junta as entradas dos dois arquivos de preferências do perfil.
func readChromiumPrefs(dir string) map[string]chromiumSetting {
	out := map[string]chromiumSetting{}
	for _, name := range []string{"Preferences", "Secure Preferences"} { // o segundo vence
		raw, err := readSmall(dir, name, maxPrefs)
		if err != nil {
			continue
		}
		var p chromiumPrefs
		if json.Unmarshal(raw, &p) != nil {
			continue
		}
		for id, st := range p.Extensions.Settings {
			st.present = true
			st.path, _ = st.Path.(string)
			out[id] = st
		}
	}
	return out
}

func (st chromiumSetting) disabled() bool {
	if n, ok := asInt(st.State); ok && n == 0 {
		return true
	}
	switch r := st.DisableReasons.(type) {
	case float64:
		return r != 0
	case []any:
		return len(r) > 0
	}
	return false
}

// notes: o que a preferência conta de uma extensão.
func (st chromiumSetting) notes() []string {
	if !st.present {
		return nil
	}
	var out []string
	if st.disabled() {
		out = append(out, "desativada")
	}
	loc, _ := asInt(st.Location)
	switch loc {
	case locInternal:
		if from, ok := st.FromWebstore.(bool); ok && !from {
			out = append(out, "fora da loja")
		}
	case locExternalPref, locExternalReg, locExternalPrefDL:
		out = append(out, "instalada por outro programa")
	case locUnpacked:
		out = append(out, "sem compactação (modo de desenvolvedor)")
	case locPolicyDownload, locPolicy:
		out = append(out, "instalada por política")
	case locCommandLine:
		out = append(out, "carregada pela linha de comando")
	}
	return out
}

// allSites: o padrão dá acesso a todas as páginas.
func allSites(patterns ...[]any) bool {
	for _, list := range patterns {
		for _, v := range list {
			switch s, _ := v.(string); s {
			case "<all_urls>", "*://*/*", "http://*/*", "https://*/*", "*://*/":
				return true
			}
		}
	}
	return false
}

func (m chromiumManifest) allSites() bool {
	lists := [][]any{m.Permissions, m.HostPermissions}
	for _, c := range m.ContentScripts {
		lists = append(lists, c.Matches)
	}
	return allSites(lists...)
}

// localized troca o "__MSG_chave__" do manifesto pelo texto do idioma padrão da extensão.
func localized(dir, text, locale string) string {
	if !strings.HasPrefix(text, "__MSG_") || !strings.HasSuffix(text, "__") || len(text) < 9 {
		return text
	}
	key := strings.ToLower(text[6 : len(text)-2])
	for _, loc := range []string{locale, "pt_BR", "en", "en_US"} {
		if loc == "" || strings.ContainsAny(loc, `/\.`) {
			continue
		}
		localeDir, info, err := descend(dir, "_locales", loc)
		if err != nil || !info.IsDir() {
			continue
		}
		raw, err := readSmall(localeDir, "messages.json", maxMessages)
		if err != nil {
			continue
		}
		var msgs map[string]struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(raw, &msgs) != nil {
			continue
		}
		for k, v := range msgs {
			if strings.ToLower(k) == key && strings.TrimSpace(v.Message) != "" {
				return v.Message
			}
		}
	}
	return text
}

// readManifest lê o manifest.json de dir.
func readManifest(dir string) (chromiumManifest, bool) {
	var m chromiumManifest
	raw, err := readSmall(dir, "manifest.json", maxManifest)
	if err != nil {
		return m, false
	}
	// Um campo de tipo inesperado não invalida o resto do manifesto.
	var typeErr *json.UnmarshalTypeError
	if err := json.Unmarshal(raw, &m); err != nil && !errors.As(err, &typeErr) {
		return m, false
	}
	m.Name = localized(dir, m.Name, m.DefaultLocale)
	return m, true
}

// chromiumProfile lê um perfil de navegador de base Chromium; diz se era mesmo um perfil.
func chromiumProfile(u *userScan, user, browser, profile, dir string) bool {
	extDir, info, err := descend(dir, "Extensions")
	hasExt := err == nil && info.IsDir()
	if _, pinfo, perr := descend(dir, "Preferences"); !hasExt && (perr != nil || !pinfo.Mode().IsRegular()) {
		return false
	}
	prefs := readChromiumPrefs(dir)
	seen := map[string]bool{}
	if hasExt {
		for _, id := range subdirs(extDir, protocol.MaxAnswerItems) {
			if !chromiumID.MatchString(id) {
				continue
			}
			// Uma pasta por versão; durante a atualização há duas: fica a mais nova.
			var best chromiumManifest
			var bestTime time.Time
			ok := false
			for _, v := range subdirs(filepath.Join(extDir, id), 8) {
				vdir := filepath.Join(extDir, id, v)
				m, found := readManifest(vdir)
				if !found {
					continue
				}
				mod := time.Time{}
				if vi, err := os.Lstat(vdir); err == nil {
					mod = vi.ModTime()
				}
				if !ok || mod.After(bestTime) {
					best, bestTime, ok = m, mod, true
				}
			}
			if !ok {
				continue
			}
			seen[id] = true
			st := prefs[id]
			if loc, _ := asInt(st.Location); loc == locComponent || loc == locExternalComp {
				continue // parte do navegador
			}
			notes := st.notes()
			if best.allSites() {
				notes = append(notes, "acesso a todos os sites")
			}
			u.add(protocol.AnswerItem{Key: id, Name: best.Name, Version: best.Version, User: user, Source: browser, Where: profile,
				Note: strings.Join(notes, "; ")})
		}
	}
	// As carregadas de uma pasta qualquer (modo de desenvolvedor, linha de comando) só
	// aparecem nas preferências: são justamente as que não passaram pela loja.
	ids := make([]string, 0, len(prefs))
	for id := range prefs {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		st := prefs[id]
		loc, _ := asInt(st.Location)
		if seen[id] || !chromiumID.MatchString(id) || (loc != locUnpacked && loc != locCommandLine) || st.path == "" {
			continue
		}
		it := protocol.AnswerItem{Key: id, Name: "(pasta) " + st.path, User: user, Source: browser, Where: profile}
		notes := st.notes()
		if m, ok := unpackedManifest(st.path); ok {
			if m.Name != "" {
				it.Name = m.Name
			}
			it.Version = m.Version
			if m.allSites() {
				notes = append(notes, "acesso a todos os sites")
			}
		}
		notes = append(notes, "pasta "+st.path)
		it.Note = strings.Join(notes, "; ")
		u.add(it)
	}
	return true
}

// unpackedManifest lê o manifesto de uma extensão carregada de uma pasta do disco. O caminho
// veio de um arquivo do usuário: só vale caminho local e absoluto, e cada pedaço dele é
// conferido (descend) para o agente não seguir um link.
func unpackedManifest(path string) (chromiumManifest, bool) {
	vol := filepath.VolumeName(path)
	if !filepath.IsAbs(path) || strings.HasPrefix(vol, `\\`) || strings.HasPrefix(path, "//") {
		return chromiumManifest{}, false
	}
	root := vol + string(filepath.Separator)
	rest := strings.FieldsFunc(path[len(vol):], func(r rune) bool { return r == '/' || r == filepath.Separator })
	if len(rest) == 0 || slices.Contains(rest, "..") {
		return chromiumManifest{}, false
	}
	dir, info, err := descend(root, rest...)
	if err != nil || !info.IsDir() {
		return chromiumManifest{}, false
	}
	return readManifest(dir)
}

// --- Firefox ---------------------------------------------------------------------------

type firefoxList struct {
	Addons []struct {
		ID            string `json:"id"`
		Version       string `json:"version"`
		Type          string `json:"type"`
		Location      string `json:"location"`
		Active        *bool  `json:"active"`
		UserDisabled  bool   `json:"userDisabled"`
		SignedState   *int   `json:"signedState"`
		DefaultLocale struct {
			Name string `json:"name"`
		} `json:"defaultLocale"`
		UserPermissions struct {
			Origins []any `json:"origins"`
		} `json:"userPermissions"`
	} `json:"addons"`
}

// firefoxProfile lê o extensions.json de um perfil do Firefox; diz se era mesmo um perfil.
func firefoxProfile(u *userScan, user, browser, profile, dir string) bool {
	raw, err := readSmall(dir, "extensions.json", maxFirefoxList)
	if err != nil {
		_, _, perr := descend(dir, "prefs.js")
		return perr == nil
	}
	var list firefoxList
	var typeErr *json.UnmarshalTypeError
	if err := json.Unmarshal(raw, &list); err != nil && !errors.As(err, &typeErr) {
		return true
	}
	for _, a := range list.Addons {
		// Temas, idiomas e dicionários não rodam código; os "app-builtin" e "app-system" são
		// parte do Firefox.
		if a.Type != "extension" || a.ID == "" || strings.HasPrefix(a.Location, "app-builtin") ||
			a.Location == "app-system-defaults" || a.Location == "app-system-addons" {
			continue
		}
		var notes []string
		if a.UserDisabled || a.Active != nil && !*a.Active {
			notes = append(notes, "desativada")
		}
		if a.SignedState != nil && *a.SignedState <= 0 {
			notes = append(notes, "sem assinatura da Mozilla")
		}
		if a.Location != "" && a.Location != "app-profile" {
			notes = append(notes, "instalada fora do perfil ("+a.Location+")")
		}
		if allSites(a.UserPermissions.Origins) {
			notes = append(notes, "acesso a todos os sites")
		}
		name := a.DefaultLocale.Name
		if name == "" {
			name = a.ID
		}
		u.add(protocol.AnswerItem{Key: a.ID, Name: name, Version: a.Version, User: user, Source: browser, Where: profile,
			Note: strings.Join(notes, "; ")})
	}
	return true
}

// --- Arquivo na pasta de cada usuário --------------------------------------------------

// userFileParts troca o começo do caminho (jobs.UserFileVars) pelos pedaços dentro do perfil.
func (s Sensors) userFileParts(arg string) ([]string, bool) {
	rel, ok := jobs.UserFileRel(arg)
	if !ok {
		return nil, false
	}
	var head []string
	switch prefix := strings.ToUpper(arg[:len(arg)-len(rel)]); {
	case strings.HasPrefix(prefix, "~"), strings.HasPrefix(prefix, "%USERPROFILE%"):
	case s.goos() != "windows":
		return nil, false // %APPDATA% e %LOCALAPPDATA% só existem no Windows
	case strings.HasPrefix(prefix, "%APPDATA%"):
		head = []string{"AppData", "Roaming"}
	default:
		head = []string{"AppData", "Local"}
	}
	parts := strings.FieldsFunc(rel, func(r rune) bool { return r == '/' || r == '\\' })
	if len(parts) == 0 || slices.Contains(parts, "..") {
		return nil, false
	}
	return append(head, parts...), true
}

// userFile procura o arquivo (ou a pasta) dentro da pasta de cada usuário.
func (s Sensors) userFile(ctx context.Context, arg string) jobs.QueryAnswer {
	if s.Profiles == nil {
		return jobs.QueryAnswer{Value: NotApplicable}
	}
	parts, ok := s.userFileParts(arg)
	if !ok {
		return jobs.QueryAnswer{Value: NotApplicable, Extra: "pasta desconhecida neste sistema: " + arg}
	}
	u := &userScan{users: map[string]bool{}}
	err := s.eachProfile(ctx, u, func(p Profile) {
		path, info, err := descend(p.Home, parts...)
		it := protocol.AnswerItem{User: p.User, Where: path}
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return
		case errors.Is(err, errNotPlain):
			it.Name = errNotPlain.Error()
		case err != nil:
			u.unread++
			return
		case info.IsDir():
			it.Name = "é uma pasta"
		default:
			it.Name = "existe"
			if s.FileVersion != nil {
				if v := s.FileVersion(path); v != "" {
					it.Name = v
				}
			}
			notes := []string{fmt.Sprintf("%d bytes", info.Size()), "modificado em " + info.ModTime().UTC().Format("2006-01-02 15:04") + " UTC"}
			if info.Size() <= maxUserHash {
				if sum, err := hashFile(path); err == nil {
					notes = append(notes, "sha256 "+sum)
				}
			}
			it.Note = strings.Join(notes, "; ")
		}
		it.Key = it.Name
		u.add(it)
	})
	if err != nil {
		return jobs.QueryAnswer{Value: "erro ao listar os usuários", Extra: err.Error()}
	}
	extra := fmt.Sprintf("em %d de %d usuário(s)", len(u.users), u.profiles)
	if u.unread > 0 {
		extra += fmt.Sprintf("; %d pasta(s) sem leitura", u.unread)
	}
	value := "encontrado"
	switch {
	case u.truncated:
		value = "lista incompleta (cortada)"
	case len(u.items) == 0:
		value = "não encontrado"
	}
	return jobs.QueryAnswer{Value: value, Extra: extra, Items: u.items}
}
