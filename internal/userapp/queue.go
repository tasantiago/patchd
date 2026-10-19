package userapp

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/localapi"
	"github.com/tasantiago/patchd/internal/protocol"
)

// "Atualizar tudo" (Aula 19.1b), como na loja do celular. O servidor só aceita uma instalação
// por vez em cada máquina (Aula 17.2: dois instaladores juntos brigam pelo Windows Installer),
// então a fila fica aqui, na Central: ela pede o primeiro e, quando a máquina fica livre, o
// próximo. Cada pedido continua sendo do usuário, pelo canal local, com a loja refeita pelo
// servidor a cada um. As atualizações que passam pela TI não esperam: viram pedidos à TI na
// hora. A fila vive enquanto a Central estiver aberta (ela fica perto do relógio desde o
// logon); fechar a Central ou sair da sessão para a fila, e o que já foi pedido continua.

// QueueCheck: a cada quanto a fila confere se a máquina já está livre.
const QueueCheck = 15 * time.Second

// queueLimitWait: o canal recusou por pedidos demais (5 em 10 minutos): espera e tenta de novo.
const queueLimitWait = time.Minute

// queueState é o que a página mostra da fila.
type queueState struct {
	Waiting []string // os programas que ainda vão ser pedidos, na ordem
	Current string   // o último pedido pela fila (em instalação, enquanto a máquina estiver ocupada)
	Failed  []string // os que o servidor recusou, com o motivo
}

// startQueue põe os programas na fila (sem repetir) e liga quem a anda.
func (a *App) startQueue(apps []string) {
	a.mu.Lock()
	for _, app := range apps {
		if !slices.Contains(a.queue, app) && app != a.queueCurrent {
			a.queue = append(a.queue, app)
		}
	}
	a.queueFailed = nil
	run := !a.queueRunning && len(a.queue) > 0
	if run {
		a.queueRunning = true
	}
	ctx := a.life
	a.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	if run {
		go a.runQueue(ctx)
	}
}

// cancelQueue tira da fila o que ainda não foi pedido. O que já virou job continua.
func (a *App) cancelQueue() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := len(a.queue)
	a.queue = nil
	return n
}

func (a *App) queueView() queueState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return queueState{Waiting: slices.Clone(a.queue), Current: a.queueCurrent, Failed: slices.Clone(a.queueFailed)}
}

func (a *App) queueEvery() time.Duration {
	if a.queueCheck > 0 {
		return a.queueCheck
	}
	return QueueCheck
}

// runQueue anda a fila: espera a máquina ficar livre, confere que o programa ainda tem
// atualização para o usuário e pede. Termina quando a fila esvazia.
func (a *App) runQueue(ctx context.Context) {
	defer func() {
		a.mu.Lock()
		a.queueRunning = false
		a.mu.Unlock()
	}()
	wait := time.Duration(0)
	for {
		if wait > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
		wait = a.queueEvery()
		cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		resp, err := a.Call(cctx, localapi.Request{Op: localapi.OpCatalog})
		cancel()
		a.mu.Lock()
		if len(a.queue) == 0 {
			if err == nil && resp.OK && resp.Catalog.OpenJob == 0 {
				a.queueCurrent = ""
			}
			done := a.queueCurrent == ""
			a.mu.Unlock()
			if done {
				return
			}
			continue // a última instalação ainda corre: a página mostra até terminar
		}
		a.mu.Unlock()
		if err != nil || !resp.OK || resp.Catalog.OpenJob > 0 {
			continue
		}
		a.mu.Lock()
		app := a.queue[0]
		a.queue = a.queue[1:]
		a.mu.Unlock()
		i := slices.IndexFunc(resp.Catalog.Items, func(it protocol.SelfServiceItem) bool { return it.App == app })
		if i < 0 || resp.Catalog.Items[i].Action != protocol.SelfServiceUpdate || resp.Catalog.Items[i].Pending > 0 {
			wait = 0 // já atualizado (ou saiu da loja): segue para o próximo
			continue
		}
		cctx, cancel = context.WithTimeout(ctx, 90*time.Second)
		r, err := a.Call(cctx, localapi.Request{Op: localapi.OpRequest, App: app, Action: protocol.SelfServiceUpdate})
		cancel()
		a.mu.Lock()
		switch {
		case err != nil || (!r.OK && r.Code != localapi.CodeLimit && r.Code != localapi.CodeInvalid && r.Code != localapi.CodeOff):
			a.queue = append([]string{app}, a.queue...) // canal ou servidor fora: tenta de novo
		case !r.OK && r.Code == localapi.CodeLimit:
			a.queue = append([]string{app}, a.queue...)
			wait = queueLimitWait
		case !r.OK:
			a.queueFailed = append(a.queueFailed, resp.Catalog.Items[i].Name+": "+userError(r))
		case r.Result.Status == protocol.SelfServiceCreated:
			a.queueCurrent = app
		case r.Result.Status == protocol.SelfServiceWait:
			a.queue = append([]string{app}, a.queue...) // outra instalação começou antes
		default:
			a.queueFailed = append(a.queueFailed, resp.Catalog.Items[i].Name+": "+r.Result.Message)
		}
		created := err == nil && r.OK && r.Result.Status == protocol.SelfServiceCreated
		a.mu.Unlock()
		if created {
			a.kickWatch() // o aviso de "terminou" vem do ícone, como num pedido avulso
		}
	}
}

// updateAll: POST atualizar-tudo. Refaz a loja, manda à TI o que passa por ela e põe o resto
// na fila.
func (a *App) updateAll(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	_ = r.ParseForm()
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	back := func(f flash) {
		a.setFlash(f)
		http.Redirect(w, r, a.base()+"?aba="+tabUpdates, http.StatusSeeOther)
	}
	resp, err := a.Call(ctx, localapi.Request{Op: localapi.OpCatalog})
	switch {
	case err != nil:
		back(flash{"erro", "Não foi possível falar com o patchd neste computador: " + err.Error()})
		return
	case !resp.OK:
		back(flash{"erro", userError(resp)})
		return
	}
	q := a.queueView()
	var direct, toIT, refused []string
	for _, it := range resp.Catalog.Items {
		if it.Action != protocol.SelfServiceUpdate || it.Pending > 0 || slices.Contains(q.Waiting, it.App) || it.App == q.Current {
			continue
		}
		if !it.Approval {
			direct = append(direct, it.App)
			continue
		}
		rr, err := a.Call(ctx, localapi.Request{Op: localapi.OpRequest, App: it.App, Action: protocol.SelfServiceUpdate})
		switch {
		case err == nil && rr.OK && rr.Result.Status == protocol.SelfServiceApproval:
			toIT = append(toIT, it.Name)
		case err == nil && !rr.OK:
			refused = append(refused, it.Name+": "+userError(rr))
		case err == nil:
			refused = append(refused, it.Name+": "+rr.Result.Message)
		default:
			refused = append(refused, it.Name+": "+err.Error())
		}
	}
	if len(direct)+len(toIT)+len(refused) == 0 {
		back(flash{"atencao", "Nada novo para atualizar: o que havia já está na fila ou com a TI."})
		return
	}
	if len(direct) > 0 {
		a.startQueue(direct)
	}
	if len(toIT) > 0 {
		a.kickWatch()
	}
	var msg []string
	if len(direct) > 0 {
		msg = append(msg, plural(len(direct), "atualização na fila", "atualizações na fila")+": uma de cada vez, a primeira já começa. Feche os programas, se estiverem abertos, e deixe a Central aberta até terminar.")
	}
	if len(toIT) > 0 {
		msg = append(msg, plural(len(toIT), "pedido foi para a aprovação da TI", "pedidos foram para a aprovação da TI")+" ("+strings.Join(toIT, ", ")+").")
	}
	kind := "sucesso"
	if len(refused) > 0 {
		msg = append(msg, "Não deu para pedir: "+strings.Join(refused, "; ")+".")
		if len(direct)+len(toIT) == 0 {
			kind = "erro"
		} else {
			kind = "atencao"
		}
	}
	back(flash{kind, strings.Join(msg, " ")})
}

// cancelAll: POST cancelar-fila.
func (a *App) cancelAll(w http.ResponseWriter, r *http.Request) {
	n := a.cancelQueue()
	f := flash{"sucesso", plural(n, "atualização saiu da fila", "atualizações saíram da fila") + ". O que já estava instalando continua."}
	if n == 0 {
		f = flash{"atencao", "A fila já estava vazia."}
	}
	a.setFlash(f)
	http.Redirect(w, r, a.base()+"?aba="+tabUpdates, http.StatusSeeOther)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}
