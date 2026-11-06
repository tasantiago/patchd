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
}

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
