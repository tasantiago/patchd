package bioswrite

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/tasantiago/patchd/internal/biosconfig"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/machinebox"
	"github.com/tasantiago/patchd/internal/protocol"
)

// O job bios_settings (docs/bios-config.md, seção 4). As regras da senha valem aqui também:
// recusa antes de qualquer escrita deixa a BIOS intacta; uma chamada ao fabricante por
// configuração; senha errada para tudo na hora; nada de senha no log, no resultado ou no disco.

// SettingsVendor é a interface do fabricante para ler e alterar configurações.
type SettingsVendor interface {
	Name() string
	SureAdmin(ctx context.Context) (bool, error)
	// Settings lê as configurações de escolha como estão agora na BIOS.
	Settings(ctx context.Context) ([]protocol.BIOSSetting, error)
	// SetSetting grava uma configuração e devolve o código do fabricante. called = false: a
	// chamada nem chegou a ser feita.
	SetSetting(ctx context.Context, name, value string, password []byte) (reply Reply, called bool, err error)
	// Commit confirma as gravações, nos fabricantes que pedem (a Lenovo só aplica depois do
	// SaveBiosSettings). Nos outros: Reply{OK: true}, sem chamada.
	Commit(ctx context.Context, password []byte) (reply Reply, called bool, err error)
}

// BitLocker é a proteção do disco do sistema.
type BitLocker interface {
	// Protected diz se a proteção está ligada na unidade do sistema (falso: sem BitLocker).
	Protected(ctx context.Context) (bool, error)
	// SuspendOneReboot suspende a proteção até o próximo reinício, quando ela volta sozinha.
	SuspendOneReboot(ctx context.Context) error
	// Resume religa a proteção na hora.
	Resume(ctx context.Context) error
}

// SettingsRunner executa o job bios_settings.
type SettingsRunner struct {
	BoxKeyID  string
	Open      func(purpose string, sealed []byte) ([]byte, error)
	Detect    func(ctx context.Context) (SettingsVendor, error)
	BitLocker BitLocker
	Logger    *slog.Logger
}

// Resultados de cada configuração, no detalhe do job.
const (
	outWritten    = "gravada"
	outUnchanged  = "já estava assim"
	outRefused    = "recusada pela BIOS"
	outUncertain  = "resultado incerto"
	outNotTried   = "não tentada"
	outNotAllowed = "recusada pelo agente"
)

type outcome struct {
	name, from, to, status, why string
}

func (o outcome) String() string {
	s := fmt.Sprintf("%s: %s", o.name, o.status)
	if o.status == outWritten {
		s += fmt.Sprintf(" (%s → %s)", o.from, o.to)
	}
	if o.why != "" {
		s += " (" + o.why + ")"
	}
	return s
}

// Run grava as alterações. O detalhe tem uma linha por configuração; o erro diz o que o
// servidor faz com o job (jobs.ErrRefused: a BIOS não foi tocada).
func (r SettingsRunner) Run(ctx context.Context, jobID int64, p jobs.BIOSSettingsParams) (string, error) {
	refuse := func(format string, a ...any) (string, error) {
		return "", fmt.Errorf("%w: "+format+"; a BIOS não foi tocada", append([]any{jobs.ErrRefused}, a...)...)
	}
	if p.BoxKey != r.BoxKeyID {
		return refuse("o envelope foi fechado para a chave %s, e a caixa desta máquina é %s", p.BoxKey, r.BoxKeyID)
	}
	var password []byte
	if p.Password != "" {
		var err error
		if password, err = r.openPassword(machinebox.PurposeBIOSConfig, p.Password); err != nil {
			return refuse("o envelope da senha não abre (%v)", err)
		}
		defer clear(password)
	}
	v, err := r.Detect(ctx)
	switch {
	case errors.Is(err, ErrNoVendor):
		return refuse("nenhuma interface de fabricante respondeu")
	case err != nil:
		return refuse("interface do fabricante: %v", err)
	case v.Name() != p.Vendor:
		return refuse("o pedido é para a interface %q, e esta máquina tem a %q", p.Vendor, v.Name())
	}
	switch sure, err := v.SureAdmin(ctx); {
	case err != nil:
		return refuse("não deu para ler o modo HP Sure Admin (%v)", err)
	case sure:
		return refuse("HP Sure Admin ligado: esta BIOS só aceita mudanças por pacote assinado")
	}

	// A lista fechada e os valores possíveis, conferidos com a BIOS de agora.
	live, err := v.Settings(ctx)
	if err != nil {
		return refuse("não deu para ler as configurações da BIOS (%v)", err)
	}
	byName := make(map[string]protocol.BIOSSetting, len(live))
	for _, s := range live {
		byName[s.Name] = s
	}
	outs := make([]outcome, len(p.Changes))
	var todo []int
	for i, c := range p.Changes {
		outs[i] = outcome{name: c.Name, to: c.Value}
		s, found := byName[c.Name]
		switch {
		case !found:
			outs[i].status, outs[i].why = outNotAllowed, "a configuração não existe nesta BIOS"
		case biosconfig.CheckChange(s, c) != nil:
			outs[i].status, outs[i].why = outNotAllowed, biosconfig.CheckChange(s, c).Error()
		case s.Value == c.Value:
			outs[i].status = outUnchanged
		default:
			outs[i].from = s.Value
			todo = append(todo, i)
		}
	}
	if len(todo) == 0 {
		return finish(outs, nil)
	}

	// BitLocker (decisão B1): suspenso por um reinício antes de qualquer escrita.
	suspended := false
	if r.BitLocker != nil {
		on, err := r.BitLocker.Protected(ctx)
		if err != nil {
			return refuse("não deu para ler o BitLocker da unidade do sistema (%v)", err)
		}
		if on {
			if err := r.BitLocker.SuspendOneReboot(ctx); err != nil {
				return refuse("não deu para suspender o BitLocker (%v)", err)
			}
			suspended = true
			r.Logger.Warn("BitLocker suspenso até o próximo reinício para alterar a BIOS", "job_id", jobID)
		}
	}

	r.Logger.Warn("alterando configurações da BIOS", "job_id", jobID, "fabricante", v.Name(), "alteracoes", len(todo))
	written, stopped, uncertain := 0, "", false
	for _, i := range todo {
		if stopped != "" {
			outs[i].status, outs[i].why = outNotTried, stopped
			continue
		}
		c := p.Changes[i]
		reply, called, err := v.SetSetting(ctx, c.Name, c.Value, password)
		switch {
		case err != nil && !called:
			outs[i].status, outs[i].why = outNotTried, fmt.Sprintf("a chamada não chegou a ser feita: %v", err)
			stopped = "a chamada anterior ao fabricante falhou"
		case err != nil:
			outs[i].status, outs[i].why = outUncertain, fmt.Sprintf("a chamada quebrou no meio: %v", err)
			stopped, uncertain = "resultado incerto na anterior", true
		case reply.OK:
			outs[i].status = outWritten
			written++
		case reply.Uncertain:
			outs[i].status, outs[i].why = outUncertain, reply.String()
			stopped, uncertain = "resultado incerto na anterior", true
		case reply.WrongPassword:
			// Senha não confere: outra tentativa pode travar a BIOS.
			outs[i].status, outs[i].why = outRefused, reply.String()
			stopped = "a senha não conferiu; nenhuma outra chamada para não travar a BIOS"
		default:
			outs[i].status, outs[i].why = outRefused, reply.String()
		}
	}
	if written > 0 {
		written, uncertain = r.commit(ctx, v, password, outs, written, uncertain)
	}

	var tail []string
	switch {
	case written > 0:
		tail = append(tail, "as alterações valem no próximo reinício")
		if suspended {
			tail = append(tail, "BitLocker suspenso até esse reinício")
		}
	case suspended && !uncertain:
		// Nada gravado: a proteção volta já, em vez de esperar um reinício.
		if err := r.BitLocker.Resume(ctx); err != nil {
			tail = append(tail, fmt.Sprintf("BitLocker continua suspenso até o próximo reinício (religar falhou: %v)", err))
		} else {
			tail = append(tail, "nada gravado; BitLocker religado")
		}
	case suspended:
		tail = append(tail, "BitLocker continua suspenso até o próximo reinício (resultado incerto)")
	}
	return finish(outs, tail)
}

// commit confirma as gravações (Lenovo: SaveBiosSettings). Se a confirmação não sai, as
// gravadas viram recusadas (nada foi salvo) ou incertas; devolve o que ficou gravado.
func (r SettingsRunner) commit(ctx context.Context, v SettingsVendor, password []byte, outs []outcome, written int, uncertain bool) (int, bool) {
	reply, called, err := v.Commit(ctx, password)
	status, why := "", ""
	switch {
	case err != nil && !called:
		status, why = outRefused, fmt.Sprintf("a confirmação (salvar) não chegou a ser feita: %v", err)
	case err != nil:
		status, why = outUncertain, fmt.Sprintf("a confirmação (salvar) quebrou no meio: %v", err)
	case reply.OK:
		return written, uncertain
	case reply.Uncertain:
		status, why = outUncertain, "a confirmação (salvar) respondeu "+reply.String()
	default:
		status, why = outRefused, "a confirmação (salvar) respondeu "+reply.String()
	}
	for i := range outs {
		if outs[i].status == outWritten {
			outs[i].status, outs[i].why = status, why
		}
	}
	r.Logger.Warn("a BIOS não confirmou as gravações", "fabricante", v.Name(), "resultado", why)
	return 0, uncertain || status == outUncertain
}

// finish monta o detalhe e decide o erro: qualquer configuração não gravada (fora as que já
// estavam assim) é falha, com a lista inteira no detalhe.
func finish(outs []outcome, tail []string) (string, error) {
	lines := make([]string, 0, len(outs)+len(tail))
	failed := false
	for _, o := range outs {
		lines = append(lines, o.String())
		failed = failed || (o.status != outWritten && o.status != outUnchanged)
	}
	lines = append(lines, tail...)
	detail := strings.Join(lines, "; ")
	if failed {
		return "", errors.New(detail)
	}
	return detail, nil
}

func (r SettingsRunner) openPassword(purpose, b64 string) ([]byte, error) {
	return Runner{Open: r.Open}.openPassword(purpose, b64)
}
