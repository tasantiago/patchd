package jobs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/tasantiago/patchd/internal/bundle"
)

// TypeScript (Aula 23.8) executa um conjunto de arquivos que o admin subiu no painel: um ZIP
// com o executável, o .ini e o script que começa a execução e chama os outros.
//
// Como todo tipo desde a Aula 8.1, o job NÃO leva linha de comando. Leva o que identifica o
// ZIP (SHA-256 e tamanho), o nome do arquivo de dentro dele por onde a execução começa, e
// uma lista de argumentos conferida aqui, dos dois lados — os mesmos tokens do thirdparty:
// nada de aspas, espaços, barras invertidas ou caminhos. Quem monta o comando é o agente, a
// partir do runner, e passa tudo como argv, nunca por um shell.
//
// O ponto de entrada é um nome de arquivo DENTRO do ZIP conferido, não um caminho da
// máquina: não há como apontar para C:\Windows\System32\cmd.exe.
const TypeScript = "script"

// ScriptParams são os parâmetros do job script.
type ScriptParams struct {
	Bundle  string   `json:"bundle"` // nome do bundle no painel, só para o log e o resultado
	SHA256  string   `json:"sha256"`
	Size    int64    `json:"size"`
	Entry   string   `json:"entry"`  // arquivo dentro do ZIP: instalar.ps1, bin/setup.sh
	Runner  string   `json:"runner"` // powershell, cmd, exe ou bash — combina com a extensão
	Args    []string `json:"args,omitempty"`
	Timeout int      `json:"timeout_s"` // prazo da execução, em segundos
}

// Limites da execução.
const (
	MinScriptTimeout = 30
	MaxScriptTimeout = 3600
	MaxScriptArgs    = 32
	maxBundleName    = 64
)

// ParseScriptParams lê e confere os parâmetros.
func ParseScriptParams(raw json.RawMessage) (ScriptParams, error) {
	var p ScriptParams
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return p, fmt.Errorf("parâmetros do script ilegíveis: %w", err)
	}
	return p, p.Validate()
}

// Validate confere os parâmetros.
func (p ScriptParams) Validate() error {
	switch {
	case p.Bundle == "" || len(p.Bundle) > maxBundleName || !bundle.ValidEntry(p.Bundle):
		return fmt.Errorf("bundle %q inválido", p.Bundle)
	case !sha256Hex.MatchString(p.SHA256):
		return errors.New("sha256 inválido (64 dígitos hexadecimais minúsculos)")
	case p.Size <= 0 || p.Size > bundle.MaxZipBytes:
		return fmt.Errorf("tamanho %d fora de 1 byte a %d MiB", p.Size, bundle.MaxZipBytes>>20)
	case !bundle.ValidEntry(p.Entry):
		return fmt.Errorf("ponto de entrada %q inválido (um arquivo de dentro do ZIP, caminho relativo)", p.Entry)
	case bundle.RunnerFor(p.Entry) == "":
		return fmt.Errorf("o ponto de entrada %q não é executável (use .ps1, .bat, .cmd, .exe ou .sh)", p.Entry)
	case p.Runner != bundle.RunnerFor(p.Entry):
		return fmt.Errorf("runner %q não combina com o ponto de entrada %q", p.Runner, p.Entry)
	case len(p.Args) > MaxScriptArgs:
		return fmt.Errorf("mais de %d argumentos", MaxScriptArgs)
	case p.Timeout < MinScriptTimeout || p.Timeout > MaxScriptTimeout:
		return fmt.Errorf("prazo de %d s fora de %d a %d", p.Timeout, MinScriptTimeout, MaxScriptTimeout)
	}
	for _, a := range p.Args {
		if !argToken.MatchString(a) {
			return fmt.Errorf("argumento %q fora do formato (sem aspas, espaços, barras invertidas ou caminhos)", a)
		}
	}
	return nil
}

// OS: o sistema em que este bundle roda, pelo runner.
func (p ScriptParams) OS() string { return bundle.OSForRunner(p.Runner) }
