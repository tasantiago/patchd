package jobs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

// TypeShutdown (docs/wake-on-lan.md, Aula 22.3): desligar a máquina que o patchd ligou pela
// rede para a campanha, depois dos jobs dela. O agente só desliga sem ninguém conectado
// (nem sessão desconectada), com ShutdownDelay de prazo; com alguém, conta e não desliga.
const TypeShutdown = "desligar"

// ShutdownDelay: o prazo entre o job e o desligar (o resultado sai antes).
const ShutdownDelay = 2 * time.Minute

// ShutdownParams: sem parâmetros (o objeto vazio); a regra fica no agente.
type ShutdownParams struct{}

// ParseShutdownParams confere que não veio nada além do objeto vazio.
func ParseShutdownParams(raw json.RawMessage) (ShutdownParams, error) {
	var p ShutdownParams
	if len(raw) == 0 || string(raw) == "null" {
		return p, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return p, fmt.Errorf("parâmetros do desligar ilegíveis: %w", err)
	}
	return p, nil
}
