package tpexec

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/platform"
)

// fechaFalso: o Windows de mentira. Os processos abertos mudam conforme o que o agente faz
// (o aviso, o pedido de fechar, a força) e conforme o tempo passa.
type fechaFalso struct {
	mu       sync.Mutex
	abertos  []platform.Process
	ativas   map[uint32]bool
	avisos   []uint32
	textos   []string
	pedidos  []uint32
	mortos   []uint32
	esperado time.Duration
	// O que acontece: o usuário fecha durante o aviso; o programa atende o pedido; nada fecha.
	fechaNoAviso, atendePedido, naoMorre bool
}

func (f *fechaFalso) lista() ([]platform.Process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]platform.Process{{Name: "explorer.exe", PID: 9, Session: 1}}, f.abertos...), nil
}

func (f *fechaFalso) closer() *AppCloser {
	return &AppCloser{
		Sessions: func() (map[uint32]bool, error) { return f.ativas, nil },
		Notify: func(s uint32, _, text string, _ time.Duration) error {
			f.avisos, f.textos = append(f.avisos, s), append(f.textos, text)
			return nil
		},
		Ask: func(s uint32, pids []uint32) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.pedidos = append(f.pedidos, s)
			if f.atendePedido {
				f.abertos = slices.DeleteFunc(f.abertos, func(p platform.Process) bool { return p.Session == s })
			}
			return nil
		},
		Kill: func(pid uint32, _ string) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.mortos = append(f.mortos, pid)
			if f.naoMorre {
				return errors.New("acesso negado")
			}
			f.abertos = slices.DeleteFunc(f.abertos, func(p platform.Process) bool { return p.PID == pid })
			return nil
		},
	}
}

func (f *fechaFalso) sleep(_ context.Context, d time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.esperado += d
	if f.fechaNoAviso && d >= 5*time.Minute {
		f.abertos = nil
	}
	return nil
}

func TestFecharProgramaComAviso(t *testing.T) {
	inst := []byte("MZ instalador do firefox")
	ctx := context.Background()
	firefox := func() []platform.Process {
		return []platform.Process{{Name: "firefox.exe", PID: 41, Session: 4}, {Name: "firefox.exe", PID: 42, Session: 4},
			{Name: "firefox.exe", PID: 61, Session: 6}}
	}
	prepara := func(f *fechaFalso, close bool) (*Executor, *runnerFalso, *string, jobs.ThirdPartyParams) {
		run := &runnerFalso{}
		e, pedido := executor(t, inst, run, google, nil)
		e.Processes, e.Closer, e.Sleep = f.lista, f.closer(), f.sleep
		p := params(inst, "nullsoft", "/S")
		p.App, p.Version, p.Processes = "firefox", "145.0", []string{"firefox.exe"}
		if close {
			p.Close = &jobs.CloseApps{NoticeMinutes: 15}
		}
		return e, run, pedido, p
	}

	// Sem close no job: adia, como sempre, mesmo com o Closer no agente.
	f := &fechaFalso{abertos: firefox(), ativas: map[uint32]bool{4: true, 6: false}}
	e, run, pedido, p := prepara(f, false)
	if _, err := e.Install(ctx, 1, p); !errors.Is(err, jobs.ErrProgramOpen) || *pedido != "" || run.prog != "" || len(f.avisos) != 0 {
		t.Fatalf("sem close: %v", err)
	}

	// Sessão 4 ativa, 6 desconectada: o aviso só na 4; quem estava nela fechou durante o aviso.
	f = &fechaFalso{abertos: firefox(), ativas: map[uint32]bool{4: true, 6: false}, fechaNoAviso: true}
	e, run, pedido, p = prepara(f, true)
	d, err := e.Install(ctx, 2, p)
	if err != nil || run.prog == "" || *pedido == "" || !slices.Equal(f.avisos, []uint32{4}) || f.esperado < 15*time.Minute ||
		!strings.Contains(d, "aviso de 15 min em 1 sessão(ões); fechado por quem estava nele") || len(f.mortos) != 0 {
		t.Fatalf("fechou no aviso: %q %v (avisos %v, esperou %v)", d, err, f.avisos, f.esperado)
	}
	if !strings.Contains(f.textos[0], "O programa firefox (firefox.exe) será fechado às") || !strings.Contains(f.textos[0], "atualização de segurança 145.0") ||
		!strings.Contains(f.textos[0], "Salve seu trabalho agora") {
		t.Errorf("texto do aviso: %q", f.textos[0])
	}

	// Só sessões desconectadas: ninguém vê o aviso, então não espera; o pedido de fechar basta.
	f = &fechaFalso{abertos: firefox(), ativas: map[uint32]bool{4: false, 6: false}, atendePedido: true}
	e, run, _, p = prepara(f, true)
	d, err = e.Install(ctx, 3, p)
	if err != nil || run.prog == "" || len(f.avisos) != 0 || f.esperado > time.Minute || !strings.Contains(d, "fechado normalmente") ||
		len(f.pedidos) != 2 || len(f.mortos) != 0 {
		t.Fatalf("desconectadas: %q %v (pedidos %v, esperou %v)", d, err, f.pedidos, f.esperado)
	}

	// Não fecha sozinho (uma pergunta de salvar sem resposta): à força, depois de 2 minutos.
	f = &fechaFalso{abertos: firefox(), ativas: map[uint32]bool{4: true}}
	e, run, _, p = prepara(f, true)
	d, err = e.Install(ctx, 4, p)
	if err != nil || run.prog == "" || !strings.Contains(d, "fechado à força (não fechou sozinho em 2 min)") || len(f.mortos) != 3 ||
		f.esperado < 17*time.Minute {
		t.Fatalf("à força: %q %v (mortos %v, esperou %v)", d, err, f.mortos, f.esperado)
	}

	// Nem à força: adia, sem executar (o instalador foi baixado e conferido, mas não roda).
	f = &fechaFalso{abertos: firefox(), ativas: map[uint32]bool{}, naoMorre: true}
	e, run, _, p = prepara(f, true)
	if _, err := e.Install(ctx, 5, p); !errors.Is(err, ErrRetry) || !errors.Is(err, jobs.ErrNotStarted) || run.prog != "" ||
		!strings.Contains(err.Error(), "não fechou nem à força") {
		t.Fatalf("nem à força: %v", err)
	}
}
