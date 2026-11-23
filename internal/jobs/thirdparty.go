package jobs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// TypeThirdParty (Aula 14.4) atualiza um programa de terceiros no Windows com o instalador
// que o servidor baixou e conferiu (Aula 14.2), escolhido pelo plano da máquina (14.3).
//
// O job não leva linha de comando. Leva o que identifica o arquivo (SHA-256 e tamanho), o
// tipo do instalador e uma lista de argumentos conferida aqui, dos dois lados: nada de
// aspas, espaços, barras invertidas ou caminhos; no MSI, só as opções de instalação
// silenciosa e PROPRIEDADE=valor. O agente monta o comando: o msiexec do sistema com
// /i e o arquivo baixado, ou o próprio arquivo baixado.
const TypeThirdParty = "thirdparty"

// ErrRefused: o agente se recusou a executar (hash, assinatura, sistema). Vira "rejeitado".
var ErrRefused = errors.New("o agente recusou executar")

// ErrNotStarted (Aula 14.5): o instalador não chegou a rodar (o download falhou). Vira
// "falhou"; como nada mudou na máquina, o agente não reenvia o inventário.
var ErrNotStarted = errors.New("o instalador não chegou a rodar")

// ThirdPartyParams são os parâmetros do job thirdparty.
type ThirdPartyParams struct {
	App         string            `json:"app"`     // regra do manifesto: 7zip
	Version     string            `json:"version"` // versão que o instalador leva
	SHA256      string            `json:"sha256"`
	Size        int64             `json:"size"`
	Installer   string            `json:"installer"` // msi, wix, nullsoft, inno, burn ou exe
	Args        []string          `json:"args,omitempty"`
	ReturnCodes map[string]string `json:"return_codes,omitempty"` // "3010": "rebootRequiredToFinish"
	// Signer: a organização (O=) do certificado que tem de assinar o instalador. Vazio: o
	// agente confere e informa a assinatura, mas não exige (o SHA-256 é a garantia).
	Signer string `json:"signer,omitempty"`
	// Processes (Aula 14.6b): executáveis do programa (Code.exe). Com algum deles rodando,
	// em qualquer sessão, o agente não executa o instalador: substituir arquivo em uso
	// termina em Abort (o código 5 do Inno no job 33), e fechar o programa do usuário
	// perderia o trabalho dele. Vazio: não confere (agentes até a v0.14.1 recusam o campo).
	Processes []string `json:"processes,omitempty"`
	// Close (fechar o programa com aviso, opção da campanha): com o programa aberto, o agente
	// avisa quem estiver na sessão, espera o prazo, pede para o programa fechar e só força se
	// ele não fechar. nil: não fecha (adia, como sempre). Só vai a agentes que anunciam
	// protocol.CapabilityCloseApps: os anteriores recusam o campo.
	Close *CloseApps `json:"close,omitempty"`
	// Replace (regularização): a instalação de outro tipo que sai antes da nova (o exe que o
	// MSI aprovado substitui, o bundle). O agente baixa e confere o instalador novo, remove
	// esta entrada em silêncio e só então instala. nil: nada sai. Só vai a agentes que
	// anunciam protocol.CapabilityRegularize: os anteriores recusam o campo.
	Replace *ReplaceEntry `json:"replace,omitempty"`
	// TryOpen (tentar com o programa aberto, opção do programa): com o programa aberto, o
	// agente roda o instalador mesmo assim; só se a instalação não der certo o job volta
	// adiado (e o agente vigia o programa fechar). Para os programas que se atualizam abertos
	// (o Chrome, o Firefox). Só vai a agentes que anunciam protocol.CapabilityTryOpen: os
	// anteriores recusam o campo.
	TryOpen bool `json:"try_open,omitempty"`
}

// ReplaceEntry é a entrada de Programas e Recursos que a regularização remove.
type ReplaceEntry struct {
	Name    string `json:"name"`    // o DisplayName, conferido na hora
	Version string `json:"version"` // o DisplayVersion, conferido na hora
	// Key: a subchave Uninstall da entrada, no HKLM (o Source do inventário).
	Key string `json:"key"`
	// Method: msi (msiexec /x ProductCode), quiet (QuietUninstallString) ou inno.
	Method      string `json:"method"`
	ProductCode string `json:"product_code,omitempty"` // msi
}

// As chaves Uninstall do HKLM em que a regularização pode remover (as de máquina).
var replaceRoots = []string{
	`HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\`,
	`HKLM\SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\`,
}

var guidPattern = regexp.MustCompile(`^\{[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}\}$`)

// ReplaceSubkey: a subchave de Key (o que vem depois da chave Uninstall); "" se Key não é
// de uma das chaves permitidas.
func (r ReplaceEntry) ReplaceSubkey() string {
	for _, root := range replaceRoots {
		if strings.HasPrefix(r.Key, root) {
			return r.Key[len(root):]
		}
	}
	return ""
}

// Validate confere a entrada.
func (r ReplaceEntry) Validate() error {
	printable := func(s string, max int) bool {
		return s != "" && len(s) <= max && strings.IndexFunc(s, func(c rune) bool { return !unicode.IsPrint(c) }) < 0
	}
	sub := r.ReplaceSubkey()
	switch {
	case !printable(r.Name, 260):
		return errors.New("regularização: nome da entrada inválido")
	case r.Version != "" && !printable(r.Version, 64):
		return errors.New("regularização: versão da entrada inválida")
	case sub == "" || len(sub) > 200 || strings.ContainsAny(sub, `\/`) || !printable(sub, 200):
		return errors.New("regularização: a chave da entrada tem de ser uma subchave Uninstall do HKLM")
	}
	switch r.Method {
	case "msi":
		if !guidPattern.MatchString(r.ProductCode) || !strings.EqualFold(sub, r.ProductCode) {
			return errors.New("regularização: o msi pede o ProductCode, o mesmo da chave")
		}
	case "quiet", "inno":
		if r.ProductCode != "" {
			return errors.New("regularização: ProductCode só no msi")
		}
	default:
		return fmt.Errorf("regularização: método %q (use msi, quiet ou inno)", r.Method)
	}
	return nil
}

// CloseApps: o aviso antes de fechar o programa aberto.
type CloseApps struct {
	NoticeMinutes int `json:"notice_minutes"`
}

// Limites do aviso (os mesmos do reinício).
const (
	MinCloseNotice = 2
	MaxCloseNotice = 60
)

const (
	maxArgs          = 32
	maxInstallerSize = 2 << 30
	maxReturnCodes   = 64
	// MaxProcesses: quantos executáveis uma regra pode listar.
	MaxProcesses = 8
)

var (
	appID        = regexp.MustCompile(`^[a-z0-9][a-z0-9.+-]{0,63}$`)
	tpVersion    = regexp.MustCompile(`^[0-9][0-9A-Za-z.+-]{0,63}$`)
	sha256Hex    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	argToken     = regexp.MustCompile(`^[A-Za-z0-9/_.=!+-]{1,128}$`)
	msiProperty  = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_.]{0,63})=([A-Za-z0-9_.!+-]{0,64})$`)
	responseName = regexp.MustCompile(`^[A-Za-z]{1,40}$`)
	processName  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._+-]{0,59}\.[Ee][Xx][Ee]$`)
)

// ValidThirdPartyVersion: a versão cabe no job de terceiros (começa por dígito; letras, dígitos, . + -).
func ValidThirdPartyVersion(v string) bool { return tpVersion.MatchString(v) }

// ValidProcess: o nome de um executável, sem pasta (Code.exe, soffice.bin não serve).
func ValidProcess(name string) bool { return processName.MatchString(name) }

// msiSwitches: as únicas opções do msiexec que o job pode pedir. O /i, o arquivo e o log
// são do agente; /x, /a, /f, /p, /j, /y fariam outra coisa que não instalar este arquivo.
var msiSwitches = map[string]bool{"/quiet": true, "/qn": true, "/norestart": true, "/passive": true}

// msiForbidden: propriedades que trariam outro arquivo para a instalação.
var msiForbidden = map[string]bool{"TRANSFORMS": true, "PATCH": true}

// MSIInstaller: o msiexec executa (msi e wix).
func MSIInstaller(t string) bool { return t == "msi" || t == "wix" }

// InstallerInstallomator (macOS): o arquivo do job é o Installomator.sh (preso pelo SHA-256 e
// pelo tamanho, como um instalador), e o único argumento é o rótulo do programa
// (googlechromepkg). O Installomator baixa do fabricante e confere o Team ID da assinatura da
// Apple; o agente o roda como root, sem aviso na tela e sem fechar programa aberto. Só vai a
// agentes que anunciam protocol.CapabilityInstallomator.
const InstallerInstallomator = "installomator"

var installomatorLabel = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// ValidInstallomatorLabel: o nome de um rótulo do Installomator (minúsculas, dígitos, - e _).
func ValidInstallomatorLabel(l string) bool { return installomatorLabel.MatchString(l) }

// ParseThirdPartyParams lê e confere os parâmetros.
func ParseThirdPartyParams(raw json.RawMessage) (ThirdPartyParams, error) {
	var p ThirdPartyParams
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return p, fmt.Errorf("parâmetros do thirdparty ilegíveis: %w", err)
	}
	return p, p.Validate()
}

// Validate confere os parâmetros.
func (p ThirdPartyParams) Validate() error {
	switch {
	case !appID.MatchString(p.App):
		return fmt.Errorf("programa %q inválido", p.App)
	case !tpVersion.MatchString(p.Version):
		return fmt.Errorf("versão %q inválida", p.Version)
	case !sha256Hex.MatchString(p.SHA256):
		return errors.New("sha256 inválido (64 dígitos hexadecimais minúsculos)")
	case p.Size <= 0 || p.Size > maxInstallerSize:
		return fmt.Errorf("tamanho %d fora de 1 byte a 2 GiB", p.Size)
	case !MSIInstaller(p.Installer) && p.Installer != "nullsoft" && p.Installer != "inno" && p.Installer != "burn" && p.Installer != "exe" &&
		p.Installer != InstallerInstallomator:
		return fmt.Errorf("tipo de instalador %q (use msi, wix, nullsoft, inno, burn, exe ou installomator)", p.Installer)
	case p.Installer == InstallerInstallomator && (len(p.Args) != 1 || !ValidInstallomatorLabel(p.Args[0])):
		return errors.New("o installomator leva um argumento só: o rótulo do programa (googlechromepkg)")
	case p.Installer == InstallerInstallomator && (len(p.ReturnCodes) > 0 || p.Signer != "" || len(p.Processes) > 0 || p.Close != nil):
		return errors.New("o installomator não leva códigos de saída, assinante, processos nem o fechar com aviso: ele confere o Team ID e adia com o programa aberto")
	case len(p.Args) > maxArgs:
		return fmt.Errorf("mais de %d argumentos", maxArgs)
	case len(p.ReturnCodes) > maxReturnCodes:
		return fmt.Errorf("mais de %d códigos de saída", maxReturnCodes)
	case len(p.Signer) > 128 || strings.IndexFunc(p.Signer, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0:
		return errors.New("assinante inválido")
	case len(p.Processes) > MaxProcesses:
		return fmt.Errorf("mais de %d processos", MaxProcesses)
	case p.Close != nil && (p.Close.NoticeMinutes < MinCloseNotice || p.Close.NoticeMinutes > MaxCloseNotice):
		return fmt.Errorf("aviso antes de fechar o programa fora de %d a %d minutos", MinCloseNotice, MaxCloseNotice)
	case p.Close != nil && len(p.Processes) == 0:
		return errors.New("fechar o programa precisa da lista de processos da regra")
	case p.TryOpen && (len(p.Processes) == 0 || p.Installer == InstallerInstallomator):
		return errors.New("tentar com o programa aberto precisa da lista de processos da regra (e é do Windows)")
	case p.Replace != nil && p.Installer == InstallerInstallomator:
		return errors.New("a regularização é do Windows: o installomator não leva replace")
	}
	if p.Replace != nil {
		if err := p.Replace.Validate(); err != nil {
			return err
		}
	}
	for _, n := range p.Processes {
		if !ValidProcess(n) {
			return fmt.Errorf("processo %q inválido (o nome do executável, sem pasta: Code.exe)", n)
		}
	}
	for _, a := range p.Args {
		if p.Installer == InstallerInstallomator {
			break // o rótulo já foi conferido
		}
		if err := checkArg(p.Installer, a); err != nil {
			return err
		}
	}
	for code, resp := range p.ReturnCodes {
		if _, err := strconv.Atoi(code); err != nil {
			return fmt.Errorf("código de saída %q não é número", code)
		}
		if !responseName.MatchString(resp) {
			return fmt.Errorf("resposta %q do código %s inválida", resp, code)
		}
	}
	return nil
}

func checkArg(installer, a string) error {
	if !argToken.MatchString(a) {
		return fmt.Errorf("argumento %q fora do formato (sem aspas, espaços, barras invertidas ou caminhos)", a)
	}
	if !MSIInstaller(installer) {
		return nil
	}
	if msiSwitches[strings.ToLower(a)] {
		return nil
	}
	m := msiProperty.FindStringSubmatch(a)
	if m == nil {
		return fmt.Errorf("argumento %q não é permitido ao msiexec (só /quiet, /qn, /norestart, /passive e PROPRIEDADE=valor)", a)
	}
	if msiForbidden[strings.ToUpper(m[1])] {
		return fmt.Errorf("a propriedade %s não é permitida (traria outro arquivo para a instalação)", m[1])
	}
	return nil
}

// SplitArgs separa os argumentos do manifesto em tokens e confere cada um. O winget guarda
// uma linha ("/S /PreventRebootRequired=true"); o job leva a lista.
func SplitArgs(installer, line string) ([]string, error) {
	if strings.ContainsAny(line, `"'`) {
		return nil, fmt.Errorf("argumentos com aspas não são aceitos pelo job: %s", line)
	}
	args := strings.Fields(line)
	for _, a := range args {
		if err := checkArg(installer, a); err != nil {
			return nil, err
		}
	}
	return args, nil
}
