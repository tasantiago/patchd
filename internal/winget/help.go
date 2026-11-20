package winget

import (
	"cmp"
	"maps"
	"slices"
)

// A ajuda dos parâmetros: o que o manifesto do winget declara de cada instalador (os
// InstallerSwitches, os modos, o escopo, os códigos de saída) e o que vale para o tipo,
// para o admin escolher os argumentos da regra sem abrir o winget-pkgs.

// Help: um instalador do manifesto, para a ajuda.
type Help struct {
	Type, Arch, Scope, UpgradeBehavior string
	Modes                              []string
	Switches                           []HelpSwitch
	Default                            string // os argumentos que o patchd usa sem os da regra
	Codes                              []HelpCode
	Unusable                           string // por que o patchd não baixa este instalador ("" se baixa)
}

// HelpSwitch: um dos InstallerSwitches.
type HelpSwitch struct{ Name, Value, Meaning string }

// HelpCode: um código de saída declarado.
type HelpCode struct {
	Code    int
	Meaning string
}

var switchMeaning = map[string]string{
	"Silent":             "instalação silenciosa: é a que o patchd usa",
	"SilentWithProgress": "silenciosa com barra de progresso (o winget usa por padrão; o patchd, não)",
	"Interactive":        "com as janelas do instalador (o patchd nunca usa)",
	"Custom":             "somados sempre (o patchd soma)",
	"Upgrade":            "somados na atualização (o patchd soma)",
	"InstallLocation":    "pasta de instalação; <INSTALLPATH> é o caminho",
	"Log":                "arquivo de log; <LOGPATH> é o caminho",
	"Repair":             "reparar a instalação",
}

var codeMeaning = map[string]string{
	"success":                   "sucesso",
	"packageInUse":              "programa em uso (o patchd tenta depois)",
	"packageInUseByApplication": "programa em uso por outro (o patchd tenta depois)",
	"installInProgress":         "outro instalador rodando (o patchd tenta depois)",
	"fileInUse":                 "arquivo em uso (o patchd tenta depois)",
	"missingDependency":         "falta uma dependência",
	"diskFull":                  "disco cheio",
	"insufficientMemory":        "memória insuficiente",
	"invalidParameter":          "parâmetro inválido",
	"noNetwork":                 "sem rede",
	"contactSupport":            "erro: falar com o fabricante",
	"rebootRequiredToFinish":    "reinício para concluir",
	"rebootRequiredForInstall":  "reinício antes de instalar",
	"rebootInitiated":           "o instalador reiniciou a máquina",
	"cancelledByUser":           "cancelado",
	"alreadyInstalled":          "já instalado",
	"downgrade":                 "versão mais nova já instalada",
	"blockedByPolicy":           "bloqueado por política",
	"systemNotSupported":        "sistema não suportado",
	"custom":                    "código próprio do fabricante",
}

// TypeTips: os argumentos conhecidos de cada tipo, além do que o manifesto declara.
var TypeTips = map[string][]string{
	"msi": {"/quiet /norestart: silencioso, sem reiniciar (o padrão do patchd)",
		"PROPRIEDADE=valor: as propriedades públicas do MSI (as do fabricante; ex.: o PORTAL= do GlobalProtect)",
		"ALLUSERS=1: para todos os usuários (o padrão da maioria)", "REBOOT=ReallySuppress: nunca reiniciar",
		"TRANSFORMS=arquivo.mst: não serve (o job leva só o MSI)"},
	"nullsoft": {"/S: silencioso (o padrão do patchd; maiúsculo)", "/allusers: para todos os usuários, nos instaladores de programas Electron (Canva, BirdID…)",
		"/D=C:\\caminho: a pasta (sempre o último argumento, sem aspas)", "--updated: o que os instaladores Electron pedem na atualização"},
	"inno": {"/SP- /VERYSILENT /SUPPRESSMSGBOXES /NORESTART: o padrão do patchd", "/ALLUSERS: para todos os usuários",
		"/DIR=\"C:\\caminho\": a pasta", "/TASKS=\"a,b\" ou /MERGETASKS=\"!desktopicon\": as tarefas (atalhos, associações)",
		"/COMPONENTS=\"x,y\": os componentes", "/LOG=arquivo: log (o patchd já põe)"},
	"burn": {"/quiet /norestart: silencioso (o padrão do patchd)", "PROPRIEDADE=valor: as variáveis do pacote (as do fabricante)"},
	"exe":  {"cada fabricante tem os seus: o manifesto do winget diz o Silent; sem ele, o patchd não baixa"},
}

func init() { TypeTips["wix"] = TypeTips["msi"] }

// HelpFor: a ajuda de cada instalador x64 (ou neutro) do manifesto, na ordem dele.
func HelpFor(m Manifest) []Help {
	var out []Help
	for _, in := range m.Installers {
		if in.Architecture != "x64" && in.Architecture != "neutral" {
			continue
		}
		h := Help{Type: in.Type, Arch: in.Architecture, Scope: cmp.Or(in.Scope, "não declarado"), UpgradeBehavior: cmp.Or(in.UpgradeBehavior, "install"),
			Modes: in.InstallModes, Default: in.Arguments()}
		for _, k := range slices.Sorted(maps.Keys(in.Switches)) {
			h.Switches = append(h.Switches, HelpSwitch{Name: k, Value: in.Switches[k], Meaning: switchMeaning[k]})
		}
		for _, c := range slices.Sorted(maps.Keys(in.ReturnCodes)) {
			h.Codes = append(h.Codes, HelpCode{Code: c, Meaning: cmp.Or(codeMeaning[in.ReturnCodes[c]], in.ReturnCodes[c])})
		}
		h.Unusable = in.unusable(Want{Arch: "x64", Scope: "machine", UninstallFirst: true}, archOrder["x64"])
		if in.Scope == "user" {
			h.Unusable = "escopo user: só com argumentos que instalem para todos"
		}
		out = append(out, h)
		if len(out) == 8 {
			break
		}
	}
	return out
}
