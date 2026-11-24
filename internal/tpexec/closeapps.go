package tpexec

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/platform"
)

// Fechar o programa com aviso (opção da campanha de terceiros, no horário dela): com o
// programa aberto, quem está na sessão vê o aviso e tem o prazo para salvar; depois o agente
// pede para o programa fechar (como o X da janela) e, se ele não fechar (uma pergunta de
// salvar sem resposta), fecha à força. Em sessão desconectada, ninguém vê o aviso: pede e
// força sem esperar.

// AppCloser são as operações do sistema (platform, no Windows).
type AppCloser struct {
	// Sessions: as sessões com usuário e se cada uma está ativa (alguém conectado).
	Sessions func() (map[uint32]bool, error)
	// Notify mostra o aviso na sessão, sem esperar resposta; some depois de timeout.
	Notify func(session uint32, title, text string, timeout time.Duration) error
	// Ask pede para os processos da sessão fecharem (WM_CLOSE, de dentro da sessão).
	Ask func(session uint32, pids []uint32) error
	// Kill fecha à força o processo, se ele ainda for o executável name.
	Kill func(pid uint32, name string) error
	// UserSession: a sessão do usuário (pelo SID; sem SID, pela conta), para o "Fechar e
	// instalar" da Central fechar só o programa dele. nil: o close com user é recusado.
	UserSession func(sid, account string) (uint32, string, error)
}

// Prazo do "Fechar e instalar": quem pediu já confirmou; o programa tem um pouco para fechar
// sozinho (salvar o que dá) antes da força.
const userAskWait = 15 * time.Second

// Prazos de depois do aviso: quanto o programa tem para fechar sozinho e a conferência final.
const (
	closeAskWait  = 2 * time.Minute
	closePoll     = 5 * time.Second
	closeKillWait = 3 * time.Second
)

func (e *Executor) wait(ctx context.Context, d time.Duration) error {
	if e.Sleep != nil {
		return e.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (e *Executor) running(p jobs.ThirdPartyParams) ([]platform.Process, error) {
	list, err := e.Processes()
	if err != nil {
		return nil, err
	}
	return platform.Running(list, p.Processes), nil
}

// closeNotice: o texto do aviso.
func closeNotice(p jobs.ThirdPartyParams, names []string, at time.Time, minutes int) string {
	return fmt.Sprintf("O programa %s (%s) será fechado às %s, em %d minuto(s), para instalar a atualização de segurança %s.\n\n"+
		"Salve seu trabalho agora. Depois da instalação, você pode abrir o programa de novo.",
		p.App, strings.Join(names, ", "), at.Format("15:04"), minutes, p.Version)
}

// closeRunning fecha o programa aberto, com aviso. note: o que aconteceu, para o detalhe do
// job. Erro com ErrRetry: não fechou (o job volta adiado, nada foi executado).
func (e *Executor) closeRunning(ctx context.Context, p jobs.ThirdPartyParams) (string, error) {
	notStarted := func(format string, a ...any) error {
		return fmt.Errorf("%w: %w: %w: "+format, append([]any{jobs.ErrNotStarted, ErrRetry, jobs.ErrProgramOpen}, a...)...)
	}
	run, err := e.running(p)
	if err != nil {
		return "", notStarted("não foi possível listar os processos: %v", err)
	}
	if len(run) == 0 {
		return "", nil
	}
	if p.Close.User != "" {
		return e.closeForUser(ctx, p, notStarted)
	}
	active, err := e.Closer.Sessions()
	if err != nil {
		active = map[uint32]bool{} // sem saber quem está conectado: avisa todas as sessões com o programa
		for _, r := range run {
			active[r.Session] = r.Session != 0
		}
	}
	var names []string
	bySession := map[uint32][]uint32{}
	for _, r := range run {
		if !slices.Contains(names, r.Name) {
			names = append(names, r.Name)
		}
		bySession[r.Session] = append(bySession[r.Session], r.PID)
	}
	slices.Sort(names)
	sessions := slices.Sorted(func(yield func(uint32) bool) {
		for s := range bySession {
			if !yield(s) {
				return
			}
		}
	})

	minutes := p.Close.NoticeMinutes
	notice := time.Duration(minutes) * time.Minute
	notified := 0
	for _, s := range sessions {
		if s == 0 || !active[s] {
			continue // sessão desconectada (ou de serviço): ninguém vê o aviso
		}
		text := closeNotice(p, names, time.Now().Add(notice), minutes)
		if err := e.Closer.Notify(s, "Atualização de segurança", text, notice); err == nil {
			notified++
		}
	}
	if notified > 0 {
		if err := e.wait(ctx, notice); err != nil {
			return "", notStarted("interrompido durante o aviso")
		}
		if run, err = e.running(p); err == nil && len(run) == 0 {
			return fmt.Sprintf("programa aberto (%s): aviso de %d min em %d sessão(ões); fechado por quem estava nele", strings.Join(names, ", "), minutes, notified), nil
		}
	}

	// Pedir para fechar, de dentro de cada sessão.
	if run, err = e.running(p); err != nil {
		return "", notStarted("não foi possível listar os processos: %v", err)
	}
	asked := map[uint32][]uint32{}
	for _, r := range run {
		if r.Session != 0 {
			asked[r.Session] = append(asked[r.Session], r.PID)
		}
	}
	for s, pids := range asked {
		_ = e.Closer.Ask(s, pids) // falhou: o prazo passa e a força resolve
	}
	how := "fechado normalmente"
	if len(asked) > 0 {
		for waited := time.Duration(0); waited < closeAskWait; waited += closePoll {
			if err := e.wait(ctx, closePoll); err != nil {
				return "", notStarted("interrompido ao fechar o programa")
			}
			if run, err = e.running(p); err == nil && len(run) == 0 {
				break
			}
		}
	}
	if run, err = e.running(p); err != nil {
		return "", notStarted("não foi possível listar os processos: %v", err)
	}
	if len(run) > 0 {
		how = "fechado à força (não fechou sozinho em 2 min)"
		var errs []error
		for _, r := range run {
			if err := e.Closer.Kill(r.PID, r.Name); err != nil {
				errs = append(errs, fmt.Errorf("%s (pid %d): %w", r.Name, r.PID, err))
			}
		}
		_ = e.wait(ctx, closeKillWait)
		if run, err = e.running(p); err != nil || len(run) > 0 {
			return "", notStarted("o programa não fechou nem à força: %v", errors.Join(errs...))
		}
	}
	return fmt.Sprintf("programa aberto (%s): aviso de %d min em %d sessão(ões); %s", strings.Join(names, ", "), minutes, notified, how), nil
}

// closeForUser: o "Fechar e instalar" da Central. Fecha só o programa aberto na sessão de quem
// pediu (pede para fechar e, em 15 s, força); aberto também na sessão de outra pessoa, não
// mexe em nada e o job volta adiado.
func (e *Executor) closeForUser(ctx context.Context, p jobs.ThirdPartyParams, notStarted func(format string, a ...any) error) (string, error) {
	if e.Closer.UserSession == nil {
		return "", fmt.Errorf("%w: este agente não fecha o programa a pedido do usuário", jobs.ErrRefused)
	}
	sess, _, err := e.Closer.UserSession(p.Close.UserSID, p.Close.User)
	if err != nil {
		return "", notStarted("%s não está conectado nesta máquina: o programa não foi fechado", p.Close.User)
	}
	// No programa por usuário (job com user), o de outro usuário é outra instalação: não conta.
	running := func() ([]platform.Process, error) {
		list, err := e.running(p)
		if err == nil && p.User != "" {
			list = slices.DeleteFunc(list, func(r platform.Process) bool { return r.Session != sess })
		}
		return list, err
	}
	run, err := running()
	if err != nil {
		return "", notStarted("não foi possível listar os processos: %v", err)
	}
	var mine []uint32
	var names []string
	for _, r := range run {
		if r.Session != sess {
			return "", notStarted("o programa (%s) está aberto também na sessão de outro usuário (sessão %d): nada foi fechado; feche-o lá, ou fale com a TI", r.Name, r.Session)
		}
		mine = append(mine, r.PID)
		if !slices.Contains(names, r.Name) {
			names = append(names, r.Name)
		}
	}
	slices.Sort(names)
	_ = e.Closer.Ask(sess, mine)
	how := "fechado normalmente"
	for waited := time.Duration(0); waited < userAskWait; waited += closePoll {
		if err := e.wait(ctx, closePoll); err != nil {
			return "", notStarted("interrompido ao fechar o programa")
		}
		if run, err = running(); err == nil && len(run) == 0 {
			break
		}
	}
	if run, err = running(); err != nil {
		return "", notStarted("não foi possível listar os processos: %v", err)
	}
	if len(run) > 0 {
		how = "fechado à força (não fechou sozinho em 15 s)"
		var errs []error
		for _, r := range run {
			if r.Session != sess {
				return "", notStarted("o programa (%s) abriu na sessão de outro usuário (sessão %d) enquanto fechava: o job volta adiado", r.Name, r.Session)
			}
			if err := e.Closer.Kill(r.PID, r.Name); err != nil {
				errs = append(errs, fmt.Errorf("%s (pid %d): %w", r.Name, r.PID, err))
			}
		}
		_ = e.wait(ctx, closeKillWait)
		if run, err = running(); err != nil || len(run) > 0 {
			return "", notStarted("o programa não fechou nem à força: %v", errors.Join(errs...))
		}
	}
	return fmt.Sprintf("programa aberto (%s) fechado a pedido de %s, pela Central: %s", strings.Join(names, ", "), p.Close.User, how), nil
}
