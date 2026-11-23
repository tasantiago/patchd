// Package thirdparty avalia programas de terceiros (Chrome, Firefox, 7-Zip...) contra um
// manifesto: para cada programa e SO, como reconhecê-lo no inventário, em que categoria ele
// cai e de onde vem a versão de referência (Aula 6.5).
//
// O manifesto padrão vai embutido no binário (manifesto.json) e é genérico. Programas de
// cada organização (sistemas internos, versões mínimas definidas pela política) ficam num
// manifesto local, fora do repositório, que acrescenta regras ou substitui as do padrão.
package thirdparty

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/tasantiago/patchd/internal/choco"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/winget"
)

//go:embed manifesto.json
var embedded []byte

// Categorias de uma regra.
const (
	CategoryFeed     = "feed"      // versão de referência buscada numa fonte (fabricante, winget, Homebrew)
	CategoryMinimum  = "minimum"   // versão mínima definida no manifesto (política da organização)
	CategoryOSVendor = "os-vendor" // as correções vêm pelo catálogo do fabricante do SO (MSRC)
	CategoryEOL      = "eol"       // fora de suporte: não há versão alvo, há troca
	CategoryIgnore   = "ignore"    // entrada auxiliar de outro programa (não é avaliada)
	// CategoryLifecycle: a referência é a versão mais nova do ciclo instalado (3.12, 22, 8.0),
	// do endoflife.date; o ciclo sem suporte fica FORA DE SUPORTE. Para os programas com várias
	// linhas mantidas ao mesmo tempo (Python, Java, Node.js, .NET), em que "a mais nova" de
	// todas não é a versão certa para quem está numa linha ainda suportada.
	CategoryLifecycle = "lifecycle"
)

// Tipos de fonte da versão de referência.
const (
	SourceChrome   = "chrome"   // versionhistory.googleapis.com; ID é a plataforma (win64, mac)
	SourceFirefox  = "firefox"  // product-details.mozilla.org; ID é a chave (LATEST_FIREFOX_VERSION, FIREFOX_ESR)
	SourceWinget   = "winget"   // github.com/microsoft/winget-pkgs; ID é o identificador do pacote
	SourceHomebrew = "homebrew" // formulae.brew.sh; ID é o cask
	// SourceEvergreen: a API do Evergreen (evergreen-api.stealthpuppy.com), que lê a versão
	// no próprio fabricante; ID é o aplicativo (AdobeAcrobatReaderDC), e o filtro escolhe as
	// linhas da resposta (Architecture, Channel, Release...).
	SourceEvergreen = "evergreen"
	// SourceUpload: o instalador próprio, enviado pelo painel (Patch › Programas de
	// terceiros › Incluir programa › instalador próprio). A referência é a versão do último
	// instalador enviado: nenhuma consulta externa. ID é o próprio programa.
	SourceUpload = "upload"
	// SourceChocolatey: a versão do pacote no repositório da comunidade do Chocolatey (sem a
	// data da correção do pacote); ID é o pacote (firefox, adobereader).
	SourceChocolatey = "chocolatey"
)

var (
	evergreenID    = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	filterKey      = regexp.MustCompile(`^[A-Za-z]{1,40}$`)
	lifecycleSlug  = regexp.MustCompile(`^[a-z0-9][a-z0-9.+-]{0,63}$`)
	maxFilterPairs = 4
)

// Manifest é o conjunto de regras.
type Manifest struct {
	Version int   `json:"version"`
	Apps    []App `json:"apps"`
}

// App é a regra de um programa num SO.
type App struct {
	ID          string `json:"id"`        // google-chrome
	OS          string `json:"os"`        // windows ou macos
	Name        string `json:"name"`      // rótulo
	Category    string `json:"category"`  // feed, minimum, os-vendor, eol, ignore
	Publisher   string `json:"publisher"` // expressão regular sobre o fabricante; vazia: qualquer
	Match       string `json:"match"`     // expressão regular sobre o nome
	VersionFrom string `json:"version_from,omitempty"`
	// VersionMatch: expressão regular sobre a versão do inventário (o campo, não a do nome).
	// Separa as linhas de um programa que tem o mesmo nome em todas (o Node.js: "^22\\.").
	VersionMatch string `json:"version_match,omitempty"`
	// Arch: só as instalações dessa arquitetura (x64 ou x86, a visão do registro). Separa o
	// programa de 64 bits do de 32, quando o instalador da regra só serve a um: o MSI de
	// outra arquitetura instalaria outro produto ao lado. Vazio: qualquer uma.
	Arch       string  `json:"arch,omitempty"`
	Source     *Source `json:"source,omitempty"`
	MinVersion string  `json:"min_version,omitempty"`
	Note       string  `json:"note,omitempty"`
	// Check: uma segunda fonte da mesma versão (o Evergreen, que lê no fabricante), para saber
	// quando a fonte principal (o winget-pkgs) está atrasada. A referência é a maior das duas.
	Check *Source `json:"check,omitempty"`
	// Lifecycle: o produto no endoflife.date (python, nodejs, dotnet). Com ele, o ciclo da
	// versão instalada sem suporte vira FORA DE SUPORTE, em qualquer categoria; o fim próximo
	// aparece na origem.
	Lifecycle string `json:"lifecycle,omitempty"`
	// Install: como o patchd instala a atualização (Aula 14.1). Sem ele, o programa só é
	// avaliado; a regra é a lista do que o patchd pode instalar.
	Install *Install `json:"install,omitempty"`
	// Shortcut (só Windows): o atalho na área de trabalho de todos os usuários que o agente
	// mantém enquanto o executável existir (protocol.DesktopShortcut).
	Shortcut *Shortcut `json:"shortcut,omitempty"`

	publisher, match, versionMatch *regexp.Regexp
}

// Install diz de onde vem o instalador: um pacote do winget-pkgs, que não precisa ser o
// mesmo da versão de referência (a do Chrome vem da Google; o instalador, do winget).
type Install struct {
	Winget string `json:"winget,omitempty"` // Google.Chrome, Mozilla.Firefox.pt-BR (Windows)
	// Installomator (macOS): o rótulo do programa no Installomator (googlechromepkg). O
	// Installomator baixa do fabricante e confere o Team ID da assinatura; o patchd leva o
	// script, preso pelo SHA-256 (internal/installomator).
	Installomator string `json:"installomator,omitempty"`
	// Type exige um tipo de instalador: o mesmo do que está nas máquinas. O 7-Zip publica
	// exe e MSI; instalar o outro deixa duas entradas no Desinstalar. Vazio: o primeiro que
	// serve, na ordem do manifesto (como o winget).
	Type string `json:"type,omitempty"`
	// Signer exige que o instalador seja assinado por esta organização (O= do certificado,
	// Aula 14.4).
	Signer string `json:"signer,omitempty"`
	// UnsignedReason: por que o instalador não tem assinatura que se possa exigir (Aula
	// 20.5, achado F2). Uma regra de instalação tem Signer ou UnsignedReason, nunca nenhum
	// dos dois: sem assinatura, o que garante o arquivo é o SHA-256 do manifesto do
	// winget-pkgs, que vem do mesmo lugar que o endereço do download. Fica escrito e à vista.
	UnsignedReason string `json:"unsigned_reason,omitempty"`
	// Processes lista os executáveis do programa (Aula 14.6b). Com algum deles aberto, o
	// agente não instala e o job falha dizendo qual: o instalador não substitui arquivo em
	// uso, e o patchd não fecha o programa de quem está trabalhando. Só os medidos entram.
	Processes []string `json:"processes,omitempty"`
	// Arguments (Windows, winget): os argumentos da instalação silenciosa no lugar dos do
	// manifesto do winget. Para quando o winget acrescenta o que a atualização não deve fazer
	// (o ADDLOCAL=ALL do Temurin liga JAVA_HOME e as chaves do Java da Oracle). Vazio: os do
	// winget.
	Arguments string `json:"arguments,omitempty"`
	// Regularize (Windows): como a regularização troca uma instalação de outro tipo pela do
	// instalador aprovado. Vazio: remove a antiga e instala; "over": instala por cima, sem
	// remover (o MSI corporativo do Chrome assume a instalação feita pelo exe).
	Regularize string `json:"regularize,omitempty"`
	// UninstallFirst (Windows): a atualização remove a instalação de agora (em silêncio, como a
	// regularização) e só então instala a nova. Para os instaladores que não atualizam por
	// cima: o GlobalProtect instalado de um MSI que não está mais no disco (o Windows Installer
	// pede o MSI original e falha com 1603), ou o fabricante que pede "uninstallPrevious" no
	// winget.
	UninstallFirst bool `json:"uninstall_first,omitempty"`
	// Upload (Windows): o instalador não vem do winget, é enviado pelo painel (o arquivo
	// .msi ou .exe que o fabricante distribui, como o do IRPF). Exige o tipo e a fonte
	// "upload"; os argumentos ficam em cada instalador enviado.
	Upload bool `json:"upload,omitempty"`
	// Chocolatey (Windows): o instalador vem do pacote do Chocolatey (o link do fabricante e o
	// SHA-256 que o script do pacote baixaria, internal/choco), sem winget. Exige o tipo; o
	// exe genérico, os argumentos.
	Chocolatey string `json:"chocolatey,omitempty"`
	// TryOpen (Windows): com o programa aberto, o agente tenta instalar mesmo assim e só adia
	// se não der certo (os que se atualizam abertos: o Chrome, o Firefox). Exige processes.
	// O painel liga e desliga por programa (WithTryOpen).
	TryOpen bool `json:"try_open,omitempty"`
}

// RegularizeOver: a regularização instala por cima, sem remover a instalação antiga.
const RegularizeOver = "over"

// UploadTypes: os tipos de instalador que o painel aceita enviar.
var UploadTypes = []string{"msi", "nullsoft", "inno", "exe"}

// Shortcut é o atalho da regra: o nome, o executável e o ícone (opcional).
type Shortcut struct {
	Name   string `json:"name"`
	Target string `json:"target"`
	Icon   string `json:"icon,omitempty"`
}

// DesktopShortcut: o atalho da regra como vai ao agente.
func (a App) DesktopShortcut() *protocol.DesktopShortcut {
	if a.Shortcut == nil || a.OS != "windows" {
		return nil
	}
	return &protocol.DesktopShortcut{App: a.ID, Name: a.Shortcut.Name, Target: a.Shortcut.Target, Icon: a.Shortcut.Icon}
}

// Uploaded: o instalador é o próprio, enviado pelo painel (sem winget).
func (a App) Uploaded() bool { return a.Install != nil && a.Install.Upload }

// FromChocolatey: o instalador vem do pacote do Chocolatey (sem winget).
func (a App) FromChocolatey() bool { return a.Install != nil && a.Install.Chocolatey != "" }

// Source diz de onde vem a versão de referência.
type Source struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	// Filter (só evergreen): as linhas da resposta que contam, por campo e valor, sem
	// diferença de maiúsculas ({"Architecture": "x64", "Channel": "Stable"}).
	Filter map[string]string `json:"filter,omitempty"`
}

func (s Source) String() string {
	out := s.Type + ":" + s.ID
	if len(s.Filter) > 0 {
		keys := make([]string, 0, len(s.Filter))
		for k := range s.Filter {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for i, k := range keys {
			keys[i] = k + "=" + s.Filter[k]
		}
		out += "[" + strings.Join(keys, ",") + "]"
	}
	return out
}

// validate confere a fonte.
func (s *Source) validate() error {
	if s.ID == "" {
		return fmt.Errorf("a fonte exige type e id")
	}
	switch s.Type {
	case SourceChrome, SourceFirefox, SourceWinget, SourceHomebrew, SourceUpload:
	case SourceChocolatey:
		if !choco.ValidID(s.ID) {
			return fmt.Errorf("chocolatey: o id %q é o pacote do Chocolatey (firefox, adobereader)", s.ID)
		}
	case SourceEvergreen:
		if !evergreenID.MatchString(s.ID) {
			return fmt.Errorf("evergreen: o id %q é o nome do aplicativo no Evergreen (AdobeAcrobatReaderDC)", s.ID)
		}
	default:
		return fmt.Errorf("fonte %q desconhecida (use chrome, firefox, winget, homebrew, evergreen, chocolatey ou upload)", s.Type)
	}
	if len(s.Filter) > 0 && s.Type != SourceEvergreen {
		return fmt.Errorf("filter só vale para a fonte evergreen")
	}
	if len(s.Filter) > maxFilterPairs {
		return fmt.Errorf("filter: no máximo %d campos", maxFilterPairs)
	}
	for k, v := range s.Filter {
		if !filterKey.MatchString(k) || v == "" || len(v) > 60 || strings.ContainsAny(v, "\r\n\t[],=") {
			return fmt.Errorf("filter %q=%q inválido (campo em letras; valor de uma linha, até 60 caracteres)", k, v)
		}
	}
	return nil
}

// Key identifica a regra: o mesmo programa tem uma regra por SO.
func (a App) Key() string { return a.ID + "/" + a.OS }

// Default devolve o manifesto embutido.
func Default() (Manifest, error) { return Parse(embedded) }

// Load devolve o manifesto embutido, acrescido do manifesto local quando path não é vazio.
func Load(path string) (Manifest, error) {
	m, err := Default()
	if err != nil || path == "" {
		return m, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return m, fmt.Errorf("manifesto local: %w", err)
	}
	local, err := Parse(data)
	if err != nil {
		return m, fmt.Errorf("manifesto local %s: %w", path, err)
	}
	return m.Merge(local), nil
}

// Parse lê e valida um manifesto.
func Parse(data []byte) (Manifest, error) {
	var m Manifest
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields() // campo com erro de digitação é erro, não regra ignorada
	if err := dec.Decode(&m); err != nil {
		return m, fmt.Errorf("manifesto ilegível: %w", err)
	}
	if m.Version != 1 {
		return m, fmt.Errorf("manifesto na versão %d; este binário lê a versão 1", m.Version)
	}
	seen := map[string]bool{}
	for i := range m.Apps {
		a := &m.Apps[i]
		if err := a.compile(); err != nil {
			return m, fmt.Errorf("regra %d (%s): %w", i+1, a.Key(), err)
		}
		if seen[a.Key()] {
			return m, fmt.Errorf("regra %s repetida", a.Key())
		}
		seen[a.Key()] = true
	}
	return m, nil
}

func (a *App) compile() error {
	switch {
	case a.ID == "" || a.Match == "":
		return fmt.Errorf("id e match são obrigatórios")
	case a.OS != "windows" && a.OS != "macos":
		return fmt.Errorf("os %q: use windows ou macos", a.OS)
	case a.VersionFrom != "" && a.VersionFrom != "name":
		return fmt.Errorf("version_from %q: use \"name\" ou deixe vazio", a.VersionFrom)
	}
	switch a.Category {
	case CategoryFeed:
		if a.Source == nil {
			return fmt.Errorf("categoria feed exige source com type e id")
		}
		if err := a.Source.validate(); err != nil {
			return err
		}
	case CategoryMinimum:
		if _, ok := Normalize(a.MinVersion); !ok {
			return fmt.Errorf("categoria minimum exige min_version numérica (recebido %q)", a.MinVersion)
		}
	case CategoryLifecycle:
		if a.Lifecycle == "" {
			return fmt.Errorf("categoria lifecycle exige lifecycle (o produto no endoflife.date)")
		}
		if a.Source != nil {
			return fmt.Errorf("categoria lifecycle não tem source: a referência é a versão mais nova do ciclo instalado")
		}
	case CategoryOSVendor, CategoryEOL, CategoryIgnore:
	default:
		return fmt.Errorf("categoria %q desconhecida", a.Category)
	}
	if a.Check != nil {
		if a.Category != CategoryFeed {
			return fmt.Errorf("check (a segunda fonte) só vale na categoria feed")
		}
		if err := a.Check.validate(); err != nil {
			return fmt.Errorf("check: %w", err)
		}
	}
	if a.Lifecycle != "" && !lifecycleSlug.MatchString(a.Lifecycle) {
		return fmt.Errorf("lifecycle %q: o nome do produto no endoflife.date, em minúsculas (python, nodejs)", a.Lifecycle)
	}
	if a.Install != nil && a.OS == "macos" {
		in := a.Install
		switch {
		case a.Category != CategoryFeed && a.Category != CategoryMinimum && a.Category != CategoryLifecycle:
			return fmt.Errorf("install exige categoria feed, minimum ou lifecycle (há versão a alcançar)")
		case !jobs.ValidInstallomatorLabel(in.Installomator):
			return fmt.Errorf("no macOS, install leva installomator: o rótulo do programa no Installomator (googlechromepkg)")
		case in.Winget != "" || in.Chocolatey != "" || in.Type != "" || in.Signer != "" || in.UnsignedReason != "" || len(in.Processes) > 0:
			return fmt.Errorf("no macOS, install leva só installomator: o Installomator confere o Team ID e adia com o programa aberto")
		}
	}
	if a.Install != nil && a.OS == "windows" {
		switch {
		case a.Install.Installomator != "":
			return fmt.Errorf("installomator só vale para macos")
		case a.Category != CategoryFeed && a.Category != CategoryMinimum && a.Category != CategoryLifecycle:
			return fmt.Errorf("install exige categoria feed, minimum ou lifecycle (há versão a alcançar)")
		case a.Install.Upload && a.Install.Winget != "", a.Install.Chocolatey != "" && (a.Install.Upload || a.Install.Winget != ""):
			return fmt.Errorf("install com mais de uma origem (winget, chocolatey, upload): o instalador vem de um só lugar")
		case a.Install.Chocolatey != "" && !choco.ValidID(a.Install.Chocolatey):
			return fmt.Errorf("install.chocolatey %q: o id do pacote do Chocolatey (firefox, adobereader)", a.Install.Chocolatey)
		case a.Install.Chocolatey != "" && a.Install.Type == "":
			return fmt.Errorf("install.chocolatey exige type (o tipo do instalador que o pacote baixa)")
		case a.Install.Chocolatey != "" && a.Install.Type == "exe" && strings.TrimSpace(a.Install.Arguments) == "":
			return fmt.Errorf("install.chocolatey com o exe genérico exige arguments (a instalação silenciosa, o silentArgs do pacote)")
		case a.Install.Upload && !slices.Contains(UploadTypes, a.Install.Type):
			return fmt.Errorf("install.upload exige type: %s", strings.Join(UploadTypes, ", "))
		case a.Install.Upload && (a.Category != CategoryFeed || a.Source == nil || a.Source.Type != SourceUpload || a.Source.ID != a.ID):
			return fmt.Errorf("install.upload exige a categoria feed com a fonte upload do próprio programa (a referência é o último instalador enviado)")
		case !a.Install.Upload && a.Install.Chocolatey == "" && !winget.ValidID(a.Install.Winget):
			return fmt.Errorf("install.winget %q fora da forma Fabricante.Programa", a.Install.Winget)
		case a.Install.Type != "" && !winget.Supported(a.Install.Type):
			return fmt.Errorf("install.type %q: use msi, wix, nullsoft, inno, burn ou exe", a.Install.Type)
		case len(a.Install.Signer) > 128 || strings.ContainsAny(a.Install.Signer, "\r\n\t"):
			return fmt.Errorf("install.signer inválido")
		case a.Install.TryOpen && len(a.Install.Processes) == 0:
			return fmt.Errorf("install.try_open exige processes (os executáveis que o agente confere)")
		case len(a.Install.Processes) > jobs.MaxProcesses:
			return fmt.Errorf("install.processes: no máximo %d", jobs.MaxProcesses)
		}
		for _, n := range a.Install.Processes {
			if !jobs.ValidProcess(n) {
				return fmt.Errorf("install.processes: %q não é o nome de um executável (Code.exe)", n)
			}
		}
		switch { // Aula 20.5 (F2): depois das conferências de forma
		case a.Install.Signer == "" && strings.TrimSpace(a.Install.UnsignedReason) == "":
			return fmt.Errorf("install exige signer (a organização O= da assinatura do instalador) ou unsigned_reason " +
				"(por que ele não tem assinatura): sem nenhum dos dois, o SHA-256 do winget-pkgs seria a única garantia (Aula 20.5)")
		case a.Install.Signer != "" && a.Install.UnsignedReason != "":
			return fmt.Errorf("install com signer e unsigned_reason: escolha um")
		case len(a.Install.UnsignedReason) > 300 || strings.ContainsAny(a.Install.UnsignedReason, "\r\n\t"):
			return fmt.Errorf("install.unsigned_reason: uma linha, até 300 caracteres")
		}
	}
	if a.Source != nil && a.Source.Type == SourceUpload && !a.Uploaded() {
		return fmt.Errorf("a fonte upload só vale com install.upload (o instalador enviado pelo painel)")
	}
	if a.Check != nil && a.Source != nil && a.Source.Type == SourceUpload {
		return fmt.Errorf("check não vale com a fonte upload")
	}
	if a.Shortcut != nil {
		if a.OS != "windows" {
			return fmt.Errorf("shortcut só vale no Windows")
		}
		if err := a.DesktopShortcut().Validate(); err != nil {
			return fmt.Errorf("shortcut: %w", err)
		}
	}
	var err error
	if a.match, err = regexp.Compile(a.Match); err != nil {
		return fmt.Errorf("match: %w", err)
	}
	if a.VersionFrom == "name" && a.match.NumSubexp() < 1 {
		return fmt.Errorf("version_from name exige um grupo no match, com a versão")
	}
	if a.Publisher != "" {
		if a.publisher, err = regexp.Compile(a.Publisher); err != nil {
			return fmt.Errorf("publisher: %w", err)
		}
	}
	if a.VersionMatch != "" {
		if a.versionMatch, err = regexp.Compile(a.VersionMatch); err != nil {
			return fmt.Errorf("version_match: %w", err)
		}
	}
	switch a.Arch {
	case "", "x64", "x86", "arm64":
	default:
		return fmt.Errorf("arch %q: use x64, x86 ou arm64", a.Arch)
	}
	if in := a.Install; in != nil && in.Regularize != "" && in.Regularize != RegularizeOver {
		return fmt.Errorf("install.regularize %q: use \"over\" ou deixe vazio", in.Regularize)
	}
	if in := a.Install; in != nil && in.Arguments != "" {
		if (in.Winget == "" && in.Chocolatey == "") || in.Type == "" {
			return fmt.Errorf("install.arguments vale com install.winget (ou chocolatey) e install.type (os argumentos são de um tipo de instalador)")
		}
		if _, err := jobs.SplitArgs(in.Type, in.Arguments); err != nil {
			return fmt.Errorf("install.arguments: %w", err)
		}
	}
	return nil
}

// Merge devolve o manifesto com as regras de local: a regra com o mesmo id e SO substitui a
// do padrão; as outras são acrescentadas.
func (m Manifest) Merge(local Manifest) Manifest {
	out := Manifest{Version: m.Version}
	override := map[string]App{}
	for _, a := range local.Apps {
		override[a.Key()] = a
	}
	for _, a := range m.Apps {
		if o, ok := override[a.Key()]; ok {
			out.Apps = append(out.Apps, o)
			delete(override, a.Key())
			continue
		}
		out.Apps = append(out.Apps, a)
	}
	for _, a := range local.Apps {
		if _, ok := override[a.Key()]; ok {
			out.Apps = append(out.Apps, a)
		}
	}
	return out
}

// For devolve as regras de um SO, na ordem do manifesto.
func (m Manifest) For(osID string) []App {
	var out []App
	for _, a := range m.Apps {
		if a.OS == osID {
			out = append(out, a)
		}
	}
	return out
}

// Matches diz se a regra reconhece a entrada do inventário (nome e fabricante).
func (a App) Matches(name, publisher string) bool {
	if a.match == nil || !a.match.MatchString(name) {
		return false
	}
	return a.publisher == nil || a.publisher.MatchString(publisher)
}

// MatchesName: o nome da entrada é o da regra, sem olhar o fabricante (para dizer, quando o
// programa não é reconhecido, que o nome bate e o fabricante não).
func (a App) MatchesName(name string) bool { return a.match != nil && a.match.MatchString(name) }

// MatchesSoftware diz se a regra reconhece a entrada: o nome e o fabricante (Matches), a
// versão (version_match) e a arquitetura (arch). Sem a arquitetura no inventário (agente
// anterior), a regra com arch não reconhece: a entrada fica para a regra geral do programa.
func (a App) MatchesSoftware(s protocol.Software) bool {
	if !a.Matches(s.Name, s.Publisher) {
		return false
	}
	if a.versionMatch != nil && !a.versionMatch.MatchString(s.Version) {
		return false
	}
	return a.Arch == "" || a.Arch == s.Arch
}

// versionNumber: o começo numérico de uma versão ("140.17.0esr" → "140.17.0").
var versionNumber = regexp.MustCompile(`^[0-9]+(\.[0-9]+)*`)

// Normalize reduz uma versão à parte numérica com pontos. Recusa o que não começa por um
// número ("Auto Updatable") e o que continua com outro número depois de um separador que
// não é ponto ("2022-12-02 15:42", uma data; "3.14-64", o "-64" do Python).
func Normalize(v string) (string, bool) {
	v = strings.TrimSpace(v)
	n := versionNumber.FindString(v)
	if n == "" {
		return "", false
	}
	rest := v[len(n):]
	if len(rest) > 1 && (rest[0] == '-' || rest[0] == ' ' || rest[0] == '_') && rest[1] >= '0' && rest[1] <= '9' {
		return "", false
	}
	return n, true
}

// InstalledVersion devolve a versão a comparar de uma entrada do inventário: a do campo de
// versão, ou a capturada do nome (version_from: name).
func (a App) InstalledVersion(name, version string) (string, bool) {
	if a.VersionFrom == "name" {
		m := a.match.FindStringSubmatch(name)
		if len(m) < 2 {
			return "", false
		}
		return Normalize(m[1])
	}
	return Normalize(version)
}
