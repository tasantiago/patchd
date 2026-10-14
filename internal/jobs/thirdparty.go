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
}

const (
	maxArgs          = 32
	maxInstallerSize = 2 << 30
	maxReturnCodes   = 64
)

var (
	appID        = regexp.MustCompile(`^[a-z0-9][a-z0-9.+-]{0,63}$`)
	tpVersion    = regexp.MustCompile(`^[0-9][0-9A-Za-z.+-]{0,63}$`)
	sha256Hex    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	argToken     = regexp.MustCompile(`^[A-Za-z0-9/_.=!+-]{1,128}$`)
	msiProperty  = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_.]{0,63})=([A-Za-z0-9_.!+-]{0,64})$`)
	responseName = regexp.MustCompile(`^[A-Za-z]{1,40}$`)
)

// msiSwitches: as únicas opções do msiexec que o job pode pedir. O /i, o arquivo e o log
// são do agente; /x, /a, /f, /p, /j, /y fariam outra coisa que não instalar este arquivo.
var msiSwitches = map[string]bool{"/quiet": true, "/qn": true, "/norestart": true, "/passive": true}

// msiForbidden: propriedades que trariam outro arquivo para a instalação.
var msiForbidden = map[string]bool{"TRANSFORMS": true, "PATCH": true}

// MSIInstaller: o msiexec executa (msi e wix).
func MSIInstaller(t string) bool { return t == "msi" || t == "wix" }

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
	case !MSIInstaller(p.Installer) && p.Installer != "nullsoft" && p.Installer != "inno" && p.Installer != "burn" && p.Installer != "exe":
		return fmt.Errorf("tipo de instalador %q (use msi, wix, nullsoft, inno, burn ou exe)", p.Installer)
	case len(p.Args) > maxArgs:
		return fmt.Errorf("mais de %d argumentos", maxArgs)
	case len(p.ReturnCodes) > maxReturnCodes:
		return fmt.Errorf("mais de %d códigos de saída", maxReturnCodes)
	case len(p.Signer) > 128 || strings.IndexFunc(p.Signer, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0:
		return errors.New("assinante inválido")
	}
	for _, a := range p.Args {
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
