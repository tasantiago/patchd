// Package bioswrite grava a senha de administrador da BIOS (Aula 21.5; desenho em
// docs/bios-senha.md): o job bios_password no agente. É a primeira escrita no firmware do
// patchd, e as regras são estas:
//
//   - nada roda sem conferir antes: a chave da caixa é a mesma que fechou os envelopes, os
//     envelopes abrem, a interface do fabricante é a pedida e o HP Sure Admin está desligado.
//     Qualquer falha aqui é recusa (jobs.ErrRefused): a BIOS não foi tocada;
//   - o método do fabricante é chamado UMA vez. Senha errada repetida trava a BIOS em vários
//     modelos, então não há nova tentativa: o código do fabricante volta ao servidor;
//   - a chamada que quebra no meio (erro de COM, tempo esgotado) é ErrUncertain: a BIOS pode
//     ter gravado ou não, e o servidor guarda as duas senhas até alguém conferir;
//   - nenhuma senha vai para o log, o resultado ou o disco. Os bytes abertos são zerados no
//     fim (as cópias que o COM faz para a chamada, o Go não alcança).
//
// A parte que fala com a BIOS (Vendor) é do Windows (hp_windows.go); o resto é testável em
// qualquer sistema, com um Vendor falso.
package bioswrite

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"

	"github.com/tasantiago/patchd/internal/biospw"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/machinebox"
)

// Vendor é a interface do fabricante nesta máquina.
type Vendor interface {
	Name() string // "hp"
	// SureAdmin diz se o modo de autenticação reforçado está ligado (só HP).
	SureAdmin(ctx context.Context) (bool, error)
	// SetAdminPassword troca (ou define, com current vazio) a senha de administrador e
	// devolve o código do fabricante. called = false: a chamada nem chegou a ser feita.
	SetAdminPassword(ctx context.Context, newPW, current []byte) (code uint32, called bool, err error)
}

// ErrNoVendor: a máquina não tem a interface de nenhum fabricante conhecido.
var ErrNoVendor = errors.New("nenhuma interface de fabricante respondeu nesta máquina")

// ErrUncertain: a chamada quebrou no meio; não se sabe se a BIOS gravou.
var ErrUncertain = jobs.ErrBIOSUncertain

// Runner executa o job bios_password.
type Runner struct {
	BoxKeyID string                                              // a impressão da chave da caixa desta máquina
	Open     func(purpose string, sealed []byte) ([]byte, error) // abre um envelope da caixa
	Detect   func(ctx context.Context) (Vendor, error)           // a interface do fabricante (ErrNoVendor: nenhuma)
	Logger   *slog.Logger
}

// hpCodes: os códigos de retorno do SetBIOSSetting da HP (HP Client Management Interface).
// uncertain: o código não garante que nada mudou.
var hpCodes = map[uint32]struct {
	text      string
	uncertain bool
}{
	1: {"não suportado neste modelo", false},
	2: {"erro não especificado do firmware", true},
	3: {"tempo esgotado no firmware", true},
	4: {"o firmware recusou a troca", false},
	5: {"parâmetro inválido (a senha nova fora das regras desta BIOS?)", false},
	6: {"acesso negado: a senha atual não confere. Não repita: a BIOS pode travar novas tentativas até reiniciar", false},
}

// Run grava a senha. O detalhe nunca tem senha; o erro diz o que o servidor faz com o job.
func (r Runner) Run(ctx context.Context, jobID int64, p jobs.BIOSPasswordParams) (string, error) {
	refuse := func(format string, a ...any) (string, error) {
		return "", fmt.Errorf("%w: "+format+"; a BIOS não foi tocada", append([]any{jobs.ErrRefused}, a...)...)
	}
	if p.BoxKey != r.BoxKeyID {
		return refuse("os envelopes foram fechados para a chave %s, e a caixa desta máquina é %s (o servidor fecha de novo no próximo pedido)",
			p.BoxKey, r.BoxKeyID)
	}
	newPW, err := r.openPassword(machinebox.PurposeBIOS, p.New)
	if err != nil {
		return refuse("o envelope da senha nova não abre (%v)", err)
	}
	defer clear(newPW)
	if biospw.Check(string(newPW)) != nil {
		return refuse("a senha nova está fora do formato da guarda")
	}
	var current []byte
	if p.Current != "" {
		if current, err = r.openPassword(machinebox.PurposeBIOSCurrent, p.Current); err != nil {
			return refuse("o envelope da senha atual não abre (%v)", err)
		}
		defer clear(current)
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
	sure, err := v.SureAdmin(ctx)
	switch {
	case err != nil:
		return refuse("não deu para ler o modo HP Sure Admin (%v)", err)
	case sure:
		return refuse("HP Sure Admin ligado: esta BIOS só aceita mudanças por pacote assinado")
	}
	r.Logger.Warn("gravando a senha de administrador da BIOS", "job_id", jobID, "guarda", p.Guard, "fabricante", v.Name(),
		"troca", current != nil)
	code, called, err := v.SetAdminPassword(ctx, newPW, current)
	switch {
	case err != nil && !called:
		return refuse("a chamada ao fabricante não chegou a ser feita (%v)", err)
	case err != nil:
		return "", fmt.Errorf("%w: a chamada ao fabricante quebrou no meio (%v)", ErrUncertain, err)
	case code == 0:
		what := "definida"
		if current != nil {
			what = "trocada"
		}
		return fmt.Sprintf("senha de administrador da BIOS %s (%s, código 0)", what, v.Name()), nil
	}
	c, known := hpCodes[code]
	if !known {
		c.text, c.uncertain = "código desconhecido", true
	}
	if c.uncertain {
		return "", fmt.Errorf("%w: %s respondeu %d (%s)", ErrUncertain, v.Name(), code, c.text)
	}
	return "", fmt.Errorf("%s respondeu %d (%s); a senha anterior continua valendo", v.Name(), code, c.text)
}

func (r Runner) openPassword(purpose, b64 string) ([]byte, error) {
	sealed, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, err
	}
	return r.Open(purpose, sealed)
}
