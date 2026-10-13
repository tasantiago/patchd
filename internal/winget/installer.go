// Package winget lê os manifestos de instalador do winget-pkgs (github.com/microsoft/winget-pkgs)
// e escolhe, para uma máquina, o instalador que o patchd pode executar sem ninguém na frente:
// de onde baixar, o SHA-256 esperado e a linha de comando silenciosa (Aula 14.1).
//
// O patchd não usa o winget das máquinas: o servidor lê o manifesto, baixa e confere o
// instalador, e o agente só executa o que veio num job assinado (Aulas 14.2 e 14.3).
package winget

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Manifest é o manifesto de instalador de uma versão de um pacote.
type Manifest struct {
	PackageIdentifier string
	PackageVersion    string
	ManifestVersion   string
	Installers        []Installer
}

// Installer é um instalador, já com os campos herdados da raiz do manifesto.
type Installer struct {
	Index        int    // posição no manifesto (o winget prefere o primeiro que serve)
	Architecture string // x64, x86, arm64, neutral...
	Type         string // msi, wix, nullsoft, inno, burn, exe, zip, msix...
	NestedType   string // o tipo de dentro de um zip
	Scope        string // machine, user ou "" (não declarado)
	URL          string
	SHA256       string // em minúsculas
	Locale       string
	ProductCode  string
	MinOS        string
	Platform     []string
	// UpgradeBehavior: install (padrão: instala por cima), uninstallPrevious (desinstala
	// antes) ou deny (não atualiza pelo winget).
	UpgradeBehavior string
	// Switches: Silent, SilentWithProgress, Custom, Upgrade, InstallLocation, Log...
	// Os da raiz valem quando o instalador não declara o mesmo campo.
	Switches map[string]string
	// ReturnCodes: código de saída → resposta do winget (rebootRequiredToFinish,
	// packageInUse...). Os InstallerSuccessCodes entram como "success".
	ReturnCodes map[int]string
}

// Campos que o instalador herda da raiz quando não os declara (o esquema do winget).
var inherited = []string{"InstallerType", "NestedInstallerType", "Scope", "InstallerLocale", "ProductCode",
	"MinimumOSVersion", "UpgradeBehavior"}

var sha256Hex = regexp.MustCompile(`^[0-9A-Fa-f]{64}$`)

// ParseInstaller lê um manifesto de instalador (ManifestType: installer).
func ParseInstaller(data []byte) (Manifest, error) {
	v, err := parseYAML(data)
	if err != nil {
		return Manifest{}, err
	}
	root, ok := v.(map[string]any)
	if !ok {
		return Manifest{}, fmt.Errorf("manifesto: a raiz não é um mapa")
	}
	if t := str(root, "ManifestType"); t != "installer" {
		return Manifest{}, fmt.Errorf("manifesto do tipo %q; esperava installer", t)
	}
	m := Manifest{PackageIdentifier: str(root, "PackageIdentifier"), PackageVersion: str(root, "PackageVersion"),
		ManifestVersion: str(root, "ManifestVersion")}
	if m.PackageIdentifier == "" || m.PackageVersion == "" {
		return m, fmt.Errorf("manifesto sem PackageIdentifier ou PackageVersion")
	}
	rootSwitches, err := switches(root)
	if err != nil {
		return m, err
	}
	rootCodes, err := returnCodes(root)
	if err != nil {
		return m, err
	}
	list, ok := root["Installers"].([]any)
	if !ok || len(list) == 0 {
		return m, fmt.Errorf("manifesto sem Installers")
	}
	for i, raw := range list {
		in, ok := raw.(map[string]any)
		if !ok {
			return m, fmt.Errorf("instalador %d: não é um mapa", i+1)
		}
		for _, k := range inherited {
			if _, has := in[k]; !has {
				if v, ok := root[k]; ok {
					in[k] = v
				}
			}
		}
		inst := Installer{Index: i, Architecture: str(in, "Architecture"), Type: str(in, "InstallerType"),
			NestedType: str(in, "NestedInstallerType"), Scope: str(in, "Scope"), URL: str(in, "InstallerUrl"),
			SHA256: strings.ToLower(str(in, "InstallerSha256")), Locale: str(in, "InstallerLocale"),
			ProductCode: str(in, "ProductCode"), MinOS: str(in, "MinimumOSVersion"),
			UpgradeBehavior: str(in, "UpgradeBehavior")}
		if p, ok := root["Platform"]; ok {
			if _, has := in["Platform"]; !has {
				in["Platform"] = p
			}
		}
		inst.Platform = strList(in, "Platform")
		if inst.Architecture == "" || inst.URL == "" || !sha256Hex.MatchString(inst.SHA256) {
			return m, fmt.Errorf("instalador %d: Architecture, InstallerUrl e InstallerSha256 (64 hex) são obrigatórios", i+1)
		}
		own, err := switches(in)
		if err != nil {
			return m, fmt.Errorf("instalador %d: %w", i+1, err)
		}
		inst.Switches = map[string]string{}
		for k, v := range rootSwitches {
			inst.Switches[k] = v
		}
		for k, v := range own {
			inst.Switches[k] = v
		}
		codes, err := returnCodes(in)
		if err != nil {
			return m, fmt.Errorf("instalador %d: %w", i+1, err)
		}
		inst.ReturnCodes = map[int]string{}
		for k, v := range rootCodes {
			inst.ReturnCodes[k] = v
		}
		for k, v := range codes {
			inst.ReturnCodes[k] = v
		}
		m.Installers = append(m.Installers, inst)
	}
	return m, nil
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return strings.TrimSpace(s)
}

func strList(m map[string]any, k string) []string {
	var out []string
	if l, ok := m[k].([]any); ok {
		for _, v := range l {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func switches(m map[string]any) (map[string]string, error) {
	raw, ok := m["InstallerSwitches"]
	if !ok || raw == "" {
		return nil, nil
	}
	sw, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("InstallerSwitches não é um mapa")
	}
	out := map[string]string{}
	for k, v := range sw {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("InstallerSwitches.%s não é texto", k)
		}
		out[k] = strings.TrimSpace(s)
	}
	return out, nil
}

func returnCodes(m map[string]any) (map[int]string, error) {
	out := map[int]string{}
	if l, ok := m["ExpectedReturnCodes"].([]any); ok {
		for _, raw := range l {
			e, ok := raw.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("ExpectedReturnCodes: item não é um mapa")
			}
			code, err := strconv.Atoi(str(e, "InstallerReturnCode"))
			if err != nil {
				return nil, fmt.Errorf("ExpectedReturnCodes: código %q inválido", str(e, "InstallerReturnCode"))
			}
			out[code] = str(e, "ReturnResponse")
		}
	}
	for _, s := range strList(m, "InstallerSuccessCodes") {
		code, err := strconv.Atoi(s)
		if err != nil {
			return nil, fmt.Errorf("InstallerSuccessCodes: código %q inválido", s)
		}
		out[code] = "success"
	}
	return out, nil
}

// --- Escolha ------------------------------------------------------------------------

// Tipos que o agente sabe executar em silêncio. O exe genérico só com o Silent declarado
// no manifesto: sem ele, ninguém sabe o que o instalador espera.
var silentDefaults = map[string]string{
	"msi":      "/quiet /norestart",
	"wix":      "/quiet /norestart",
	"burn":     "/quiet /norestart",
	"nullsoft": "/S",
	"inno":     "/SP- /VERYSILENT /SUPPRESSMSGBOXES /NORESTART",
}

// Supported diz se o patchd executa instaladores desse tipo.
func Supported(t string) bool {
	_, ok := silentDefaults[t]
	return ok || t == "exe"
}

// Want é o que a máquina pede.
type Want struct {
	Arch  string // arquitetura da máquina: x64, x86 ou arm64
	Scope string // machine (o agente roda como SYSTEM; o escopo usuário fica para depois)
	Type  string // tipo exigido pela regra (o mesmo do que está instalado); vazio: qualquer
}

// Skipped é um instalador que não foi o escolhido, com o motivo.
type Skipped struct {
	Installer Installer
	Reason    string
}

// archOrder: as arquiteturas que a máquina executa, da preferida para a última.
var archOrder = map[string][]string{
	"x64":   {"x64", "x86", "neutral"},
	"arm64": {"arm64", "x64", "x86", "neutral"},
	"x86":   {"x86", "neutral"},
}

// Select escolhe o instalador: a arquitetura mais próxima da máquina, o escopo declarado
// igual ao pedido antes do não declarado, e, no empate, o primeiro do manifesto (como o
// winget). Devolve também os outros, com o motivo de cada um.
func (m Manifest) Select(w Want) (Installer, []Skipped, error) {
	order, ok := archOrder[w.Arch]
	if !ok {
		return Installer{}, nil, fmt.Errorf("arquitetura %q: use x64, x86 ou arm64", w.Arch)
	}
	if w.Scope == "" {
		w.Scope = "machine"
	}
	var cands []Installer
	var skipped []Skipped
	for _, in := range m.Installers {
		if r := in.unusable(w, order); r != "" {
			skipped = append(skipped, Skipped{in, r})
			continue
		}
		cands = append(cands, in)
	}
	if len(cands) == 0 {
		return Installer{}, skipped, fmt.Errorf("%s %s: nenhum instalador serve (%s, escopo %s%s)",
			m.PackageIdentifier, m.PackageVersion, w.Arch, w.Scope, typeHint(w.Type))
	}
	rank := func(in Installer) (int, int) {
		scope := 0
		if in.Scope == "" {
			scope = 1
		}
		return slices.Index(order, in.Architecture), scope
	}
	best := slices.MinFunc(cands, func(a, b Installer) int {
		aa, as := rank(a)
		ba, bs := rank(b)
		if aa != ba {
			return aa - ba
		}
		if as != bs {
			return as - bs
		}
		return a.Index - b.Index
	})
	for _, in := range cands {
		if in.Index != best.Index {
			skipped = append(skipped, Skipped{in, "serve, mas outro vem antes na preferência"})
		}
	}
	slices.SortFunc(skipped, func(a, b Skipped) int { return a.Installer.Index - b.Installer.Index })
	return best, skipped, nil
}

func typeHint(t string) string {
	if t == "" {
		return ""
	}
	return ", tipo " + t
}

func (in Installer) unusable(w Want, order []string) string {
	_, known := silentDefaults[in.Type]
	switch {
	case !slices.Contains(order, in.Architecture):
		return "arquitetura " + in.Architecture + " não roda em " + w.Arch
	case w.Type != "" && in.Type != w.Type:
		return "tipo " + in.Type + "; a regra exige " + w.Type
	case in.Type == "exe" && in.Switches["Silent"] == "":
		return "exe sem o Silent no manifesto: não se sabe como instalar em silêncio"
	case !known && in.Type != "exe":
		return "tipo " + in.Type + " não é executado pelo patchd"
	case in.Scope != "" && in.Scope != w.Scope:
		return "escopo " + in.Scope + "; o agente instala para a máquina"
	case in.UpgradeBehavior == "deny":
		return "UpgradeBehavior deny: o fabricante não quer atualização por fora"
	case in.UpgradeBehavior != "" && in.UpgradeBehavior != "install":
		return "UpgradeBehavior " + in.UpgradeBehavior + ": exige desinstalar antes (ainda não)"
	case len(in.Platform) > 0 && !slices.Contains(in.Platform, "Windows.Desktop"):
		return "plataforma " + strings.Join(in.Platform, ", ")
	case !strings.HasPrefix(in.URL, "https://"):
		return "endereço sem https"
	}
	return ""
}

// --- Execução -----------------------------------------------------------------------

// Arguments devolve os argumentos da instalação silenciosa, como o winget monta: o Silent
// do manifesto (ou o padrão do tipo), mais o Custom, mais o Upgrade, que a documentação do
// winget manda passar na atualização (o patchd só atualiza o que já está instalado).
func (in Installer) Arguments() string {
	parts := []string{}
	if s := in.Switches["Silent"]; s != "" {
		parts = append(parts, s)
	} else if d := silentDefaults[in.Type]; d != "" {
		parts = append(parts, d)
	}
	for _, k := range []string{"Custom", "Upgrade"} {
		if s := in.Switches[k]; s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, " ")
}

// UsesMsiexec: MSI (e o MSI feito com WiX) é executado pelo msiexec, não diretamente.
func (in Installer) UsesMsiexec() bool { return in.Type == "msi" || in.Type == "wix" }

// CommandLine mostra a linha de comando completa para um arquivo, para conferência. O
// agente monta a mesma linha a partir do tipo e dos argumentos do job.
func (in Installer) CommandLine(file string) string {
	q := `"` + file + `"`
	if in.UsesMsiexec() {
		return "msiexec.exe /i " + q + " " + in.Arguments()
	}
	return strings.TrimSpace(q + " " + in.Arguments())
}

// Resultados de uma instalação, pelo código de saída.
const (
	OutcomeOK     = "ok"
	OutcomeReboot = "reiniciar"     // instalou; termina com o reboot
	OutcomeRetry  = "tentar_depois" // outro instalador rodando, programa aberto
	OutcomeFailed = "falhou"
)

// Códigos do Windows Installer, que valem para msi, wix e burn mesmo quando o manifesto
// não os declara.
var msiCodes = map[int]string{
	3010: "rebootRequiredToFinish",
	1641: "rebootInitiated",
	1618: "installInProgress",
	1602: "cancelledByUser",
	1603: "installError",
	1638: "alreadyInstalled",
}

// Outcome classifica o código de saída: 0 é sucesso; os demais, pela resposta que o
// manifesto (ou o Windows Installer) dá a eles.
func (in Installer) Outcome(code int) (string, string) {
	if code == 0 {
		return OutcomeOK, ""
	}
	resp, ok := in.ReturnCodes[code]
	if !ok && (in.UsesMsiexec() || in.Type == "burn") {
		resp = msiCodes[code]
	}
	switch resp {
	case "success":
		return OutcomeOK, resp
	case "rebootRequiredToFinish", "rebootRequiredForInstall", "rebootInitiated":
		return OutcomeReboot, resp
	case "installInProgress", "packageInUse", "packageInUseByApplication":
		return OutcomeRetry, resp
	case "":
		return OutcomeFailed, "código " + strconv.Itoa(code) + " sem significado no manifesto"
	}
	return OutcomeFailed, resp
}
