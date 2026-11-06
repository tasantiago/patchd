package jobs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// TypeQuery (perguntas ao parque): o agente responde a uma pergunta de um catálogo fechado de
// sensores, só de leitura. Nada de código livre: o job leva o sensor e o argumento, conferidos
// aqui dos dois lados; o agente lê e devolve a resposta curta (QueryAnswer) no detalhe do
// resultado. Com o canal em tempo real, a resposta volta em segundos.
const TypeQuery = "pergunta"

// Sensores do catálogo.
const (
	SensorFile     = "arquivo"  // existe? versão (Windows), tamanho, data, SHA-256
	SensorProcess  = "processo" // rodando? quantos, em que sessões
	SensorService  = "servico"  // estado e início (Windows; systemd no Linux)
	SensorRegistry = "registro" // valor de uma chave do HKLM (Windows)
	SensorProgram  = "programa" // instalado? versões (a lista de software do inventário)
)

// Sensors: o catálogo, na ordem da tela.
var Sensors = []string{SensorFile, SensorProcess, SensorService, SensorRegistry, SensorProgram}

// QueryParams: a pergunta.
type QueryParams struct {
	Sensor string `json:"sensor"`
	Arg    string `json:"arg"`            // o caminho, o processo, o serviço, a chave, o programa
	Name   string `json:"name,omitempty"` // registro: o nome do valor (vazio: o padrão)
}

// QueryAnswer: a resposta, no detalhe do resultado (JSON). Value agrupa as máquinas na tela
// ("2.1.3", "não existe", "rodando"); Extra é o detalhe da máquina.
type QueryAnswer struct {
	Value string `json:"value"`
	Extra string `json:"extra,omitempty"`
}

// Limites da resposta (o detalhe do job tem 2000 caracteres).
const (
	MaxAnswerValue = 200
	MaxAnswerExtra = 1200
)

var (
	queryProcess = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._+-]{0,63}$`)
	queryService = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._@-]{0,79}$`)
	// Caminho: absoluto no Windows (C:\...) ou no Unix (/...), ou começando por uma pasta
	// conhecida do Windows. Nada de UNC (\\servidor: o SYSTEM se autenticaria na rede), nem
	// curingas, nem "..".
	queryFileWin  = regexp.MustCompile(`^[A-Za-z]:\\`)
	queryFileVars = []string{`%ProgramFiles%\`, `%ProgramFiles(x86)%\`, `%ProgramData%\`, `%SystemRoot%\`, `%SystemDrive%\`}
	// Registro: só o HKLM, e fora do SAM e do SECURITY (o agente é SYSTEM e leria segredos).
	queryRegPrefix = regexp.MustCompile(`(?i)^(HKLM|HKEY_LOCAL_MACHINE)\\`)
	queryRegDenied = regexp.MustCompile(`(?i)^(HKLM|HKEY_LOCAL_MACHINE)\\(SAM|SECURITY)(\\|$)`)
)

// RegistryPath tira o "HKLM\" do começo: o caminho dentro do HKEY_LOCAL_MACHINE.
func RegistryPath(arg string) string {
	return queryRegPrefix.ReplaceAllString(arg, "")
}

// SecretName: o nome do valor parece de senha ou chave; o agente não devolve o conteúdo.
func SecretName(name string) bool {
	n := strings.ToLower(name)
	for _, w := range []string{"password", "passwd", "senha", "secret", "token", "credential", "privatekey", "private_key", "apikey", "api_key"} {
		if strings.Contains(n, w) {
			return true
		}
	}
	return false
}

func printable(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return !unicode.IsPrint(r) }) < 0
}

// ParseQueryParams lê e confere a pergunta.
func ParseQueryParams(raw json.RawMessage) (QueryParams, error) {
	var p QueryParams
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return p, fmt.Errorf("parâmetros da pergunta ilegíveis: %w", err)
	}
	return p, p.Validate()
}

// Validate confere a pergunta.
func (p QueryParams) Validate() error {
	arg := p.Arg
	if arg == "" || strings.TrimSpace(arg) != arg || !printable(arg) || !printable(p.Name) {
		return errors.New("o argumento da pergunta é obrigatório, sem espaços nas pontas nem caracteres de controle")
	}
	if p.Name != "" && p.Sensor != SensorRegistry {
		return errors.New("só a pergunta de registro leva o nome do valor")
	}
	switch p.Sensor {
	case SensorFile:
		ok := len(arg) <= 260 && (queryFileWin.MatchString(arg) || strings.HasPrefix(arg, "/"))
		for _, v := range queryFileVars {
			ok = ok || len(arg) <= 260 && strings.HasPrefix(strings.ToLower(arg), strings.ToLower(v))
		}
		switch {
		case !ok:
			return errors.New(`o caminho do arquivo é absoluto (C:\..., /..., ou %ProgramFiles%\..., %SystemRoot%\...), até 260 caracteres`)
		case strings.HasPrefix(arg, `\\`) || strings.HasPrefix(arg, "//"):
			return errors.New("caminho de rede (UNC) não: o agente não sai da máquina")
		case strings.ContainsAny(arg, "*?\"<>|"):
			return errors.New("o caminho não leva curingas nem aspas")
		case strings.Contains(strings.ReplaceAll(arg, `\`, "/"), "/../") || strings.HasSuffix(arg, ".."):
			return errors.New(`o caminho não leva ".."`)
		}
	case SensorProcess:
		if !queryProcess.MatchString(arg) {
			return errors.New("o processo é o nome do executável (firefox.exe, sshd), até 64 caracteres")
		}
	case SensorService:
		if !queryService.MatchString(arg) {
			return errors.New("o serviço é o nome curto (Spooler, ssh), até 80 caracteres")
		}
	case SensorRegistry:
		switch {
		case len(arg) > 255 || !queryRegPrefix.MatchString(arg):
			return errors.New(`a chave começa por HKLM\ (só o HKEY_LOCAL_MACHINE), até 255 caracteres`)
		case queryRegDenied.MatchString(arg):
			return errors.New(`HKLM\SAM e HKLM\SECURITY não: guardam as senhas do Windows`)
		case strings.Contains(arg, `\\`) || strings.HasSuffix(arg, `\`):
			return errors.New("a chave não termina em barra nem tem barras repetidas")
		case len(p.Name) > 128:
			return errors.New("o nome do valor tem até 128 caracteres")
		}
	case SensorProgram:
		if len(arg) < 2 || len(arg) > 100 {
			return errors.New("o programa é um pedaço do nome como aparece em Software (2 a 100 caracteres)")
		}
	default:
		return fmt.Errorf("sensor desconhecido %q", p.Sensor)
	}
	return nil
}

// Clip encurta a resposta aos limites.
func (a QueryAnswer) Clip() QueryAnswer {
	return QueryAnswer{Value: clipRunes(a.Value, MaxAnswerValue), Extra: clipRunes(a.Extra, MaxAnswerExtra)}
}

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// ParseAnswer lê a resposta do detalhe do resultado; um detalhe que não é resposta (o erro do
// agente) vira o valor, encurtado.
func ParseAnswer(detail string) QueryAnswer {
	var a QueryAnswer
	if err := json.Unmarshal([]byte(detail), &a); err != nil || a.Value == "" {
		return QueryAnswer{Value: clipRunes(detail, MaxAnswerValue)}
	}
	return a.Clip()
}
