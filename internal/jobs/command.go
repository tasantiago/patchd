package jobs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// TypeCommand (docs/terminal.md): o terminal do administrador. Um comando, digitado no
// painel por um admin com a sessão aberta (motivo obrigatório), roda na máquina com a conta
// do agente (SYSTEM ou root), sem entrada pelo teclado, com tempo máximo e saída limitada.
// Tudo fica registrado: quem, quando, o texto exato, o código de saída e a saída.
const TypeCommand = "comando"

// Os interpretadores do terminal.
const (
	ShellPowerShell = "powershell"
	ShellCmd        = "cmd"
	ShellBash       = "bash"
)

// CommandShells: os interpretadores, na ordem da tela.
var CommandShells = []string{ShellPowerShell, ShellCmd, ShellBash}

// Limites do comando.
const (
	MaxCommandLen         = 4000
	MinCommandTimeout     = 5
	MaxCommandTimeout     = 600
	DefaultCommandTimeout = 60
	// MaxCommandOutput: o fim da saída que o agente manda e o servidor guarda (64 KiB).
	MaxCommandOutput = 64 << 10
	// CommandTTL: o comando que a máquina não pega nesse tempo expira (ela está desligada
	// ou fora do canal): um comando velho não roda de surpresa horas depois.
	CommandTTL = 10 * time.Minute
)

// CommandParams: o comando.
type CommandParams struct {
	Shell      string `json:"shell"`
	Command    string `json:"command"`
	TimeoutSec int    `json:"timeout_sec"`
}

// Timeout: o tempo máximo como duração.
func (p CommandParams) Timeout() time.Duration { return time.Duration(p.TimeoutSec) * time.Second }

// ParseCommandParams lê e confere o comando.
func ParseCommandParams(raw json.RawMessage) (CommandParams, error) {
	var p CommandParams
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return p, fmt.Errorf("parâmetros do comando ilegíveis: %w", err)
	}
	return p, p.Validate()
}

// Validate confere o comando: uma linha, sem caracteres de controle.
func (p CommandParams) Validate() error {
	switch {
	case !slices.Contains(CommandShells, p.Shell):
		return fmt.Errorf("interpretador desconhecido %q (powershell, cmd ou bash)", p.Shell)
	case strings.TrimSpace(p.Command) == "":
		return errors.New("o comando está vazio")
	case len(p.Command) > MaxCommandLen:
		return fmt.Errorf("o comando tem até %d bytes", MaxCommandLen)
	case !utf8.ValidString(p.Command) || strings.ContainsFunc(p.Command, func(r rune) bool { return unicode.IsControl(r) && r != '\t' }):
		return errors.New("o comando é uma linha só, sem caracteres de controle")
	case p.TimeoutSec < MinCommandTimeout || p.TimeoutSec > MaxCommandTimeout:
		return fmt.Errorf("o tempo máximo fica entre %d e %d segundos", MinCommandTimeout, MaxCommandTimeout)
	}
	return nil
}
