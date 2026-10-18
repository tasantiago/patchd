package jobs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

// TypeReboot (Aula 8.6) reinicia a máquina com aviso ao usuário, só se ela tiver reinício
// pendente (na avaliação do próprio agente, na hora). É o job separado da decisão da Aula
// 8.4: o update nunca reinicia. O único parâmetro é o aviso, em minutos; o texto do aviso
// é do agente, não vem do servidor.
const TypeReboot = "reboot"

// Limites do aviso. O teto cabe na tolerância de entrega (ResultGrace): o resultado só
// chega depois do reinício.
const (
	MinRebootDelay = 2 * time.Minute
	MaxRebootDelay = 60 * time.Minute
)

// ResultGrace: um job entregue ainda aceita resultado até esse tempo depois do prazo
// (o reinício e a busca depois dele levam alguns minutos além do aviso).
const ResultGrace = 2 * time.Hour

// RebootNothingPending é o motivo da recusa do agente quando, na hora do job, a máquina já
// não tem reinício pendente. O servidor lê esse texto exato (Aula 16.5): não é falha, é
// "nada a fazer", e o job vira dispensado (numa campanha, a máquina sai do anel resolvida).
const RebootNothingPending = "nenhum reinício pendente nesta máquina; nada a fazer"

// MaxScheduledReboot (Aula 17.6): o usuário escolhe a hora do reinício até 12 h à frente.
const MaxScheduledReboot = 12 * time.Hour

// RebootAlreadyScheduled (Aula 17.6): a máquina já tem um reinício agendado para antes da
// hora deste job; ele atende os dois. O servidor lê o texto exato: o job vira dispensado.
const RebootAlreadyScheduled = "já há um reinício agendado nesta máquina para antes desta hora; ele atende este pedido"

// RebootParams são os parâmetros do job reboot.
type RebootParams struct {
	DelayMinutes int `json:"delay_minutes"`
	// At (Aula 17.6): a hora escolhida pelo usuário. Com ela, o agente reinicia nessa hora
	// (o aviso do Windows fica aberto até lá); DelayMinutes vale como o aviso mínimo, se a
	// hora já tiver passado quando o job chegar. Agentes até a v0.17.2 recusam o campo.
	At *time.Time `json:"at,omitempty"`
}

// Delay devolve o aviso como duração.
func (p RebootParams) Delay() time.Duration { return time.Duration(p.DelayMinutes) * time.Minute }

// DelayAt é a espera até o reinício, contada de now: até At (no mínimo o aviso, no máximo
// MaxScheduledReboot) ou, sem At, o aviso.
func (p RebootParams) DelayAt(now time.Time) time.Duration {
	if p.At == nil {
		return p.Delay()
	}
	d := p.At.Sub(now)
	switch {
	case d < p.Delay():
		return p.Delay()
	case d > MaxScheduledReboot:
		return MaxScheduledReboot
	}
	return d.Round(time.Second)
}

// ParseRebootParams lê e confere os parâmetros.
func ParseRebootParams(raw json.RawMessage) (RebootParams, error) {
	var p RebootParams
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return p, fmt.Errorf("parâmetros do reboot ilegíveis: %w", err)
	}
	if d := p.Delay(); d < MinRebootDelay || d > MaxRebootDelay {
		return p, fmt.Errorf("aviso do reinício fora de %v a %v", MinRebootDelay, MaxRebootDelay)
	}
	return p, nil
}

// RebootAtParamsJSON monta os parâmetros do reinício na hora escolhida (Aula 17.6).
func RebootAtParamsJSON(at time.Time) (json.RawMessage, error) {
	at = at.UTC().Truncate(time.Second)
	p := RebootParams{DelayMinutes: int(MinRebootDelay / time.Minute), At: &at}
	raw, _ := json.Marshal(p)
	if _, err := ParseRebootParams(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// RebootParamsJSON monta os parâmetros para um aviso.
func RebootParamsJSON(delay time.Duration) (json.RawMessage, error) {
	p := RebootParams{DelayMinutes: int(delay / time.Minute)}
	raw, _ := json.Marshal(p)
	if _, err := ParseRebootParams(raw); err != nil {
		return nil, err
	}
	return raw, nil
}
