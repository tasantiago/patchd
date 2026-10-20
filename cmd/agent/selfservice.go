package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"text/tabwriter"
	"time"

	"github.com/tasantiago/patchd/internal/agent"
	"github.com/tasantiago/patchd/internal/localapi"
	"github.com/tasantiago/patchd/internal/protocol"
)

// startSelfService abre o canal local do autoatendimento (Aula 17.3), só no Windows: o
// app do usuário pede a loja e os pedidos por ele, e o agente fala com o servidor. Um
// pedido que vira job antecipa o check-in. O canal fecha com o serviço.
func startSelfService(ctx context.Context, logger *slog.Logger, ag *agent.Agent, onAppName func(string)) {
	if runtime.GOOS != "windows" {
		return
	}
	srv := &localapi.Server{Backend: ag, Logger: logger, OnCreated: ag.Wake, RebootStatus: ag.RebootStatus, OnAppName: onAppName}
	go func() {
		logger.Info("autoatendimento: canal local aberto", "pipe", localapi.PipeName)
		if err := localapi.Listen(ctx, func(c localapi.Conn) { srv.HandleConn(ctx, c) }); err != nil {
			logger.Error("autoatendimento: canal local fechado; o app do usuário não vai conseguir falar com o agente", "error", err)
		}
	}()
}

const selfServiceUsage = `uso: patchd-agent selfservice <comando>

Fala com o agente desta máquina pelo canal local, como o app do usuário (Aula 17.3). Rode
na sua sessão, sem elevar: quem pede é você, pelo seu token do Windows.

comandos:
  whoami [-groups]                       a conta e os grupos que o agente vê no seu token
  catalog                                a sua loja nesta máquina
  request -app ID -action atualizar|instalar
                                         pede ao servidor; aceito, o agente faz o check-in na hora
  request -action reiniciar [-at HH:MM]  reinicia agora (com 2 min de aviso) ou na hora escolhida
`

// localCall é o canal local; os testes o substituem.
var localCall = localapi.Call

func runSelfService(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, selfServiceUsage)
		return exitConfig
	}
	fs := flag.NewFlagSet("selfservice "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	groups := fs.Bool("groups", false, "whoami: lista os grupos")
	app := fs.String("app", "", "request: o programa, como na loja (ex.: 7zip)")
	action := fs.String("action", "", "request: atualizar, instalar ou reiniciar")
	at := fs.String("at", "", "request -action reiniciar: a hora, HH:MM (vazio: agora)")
	if err := fs.Parse(args[1:]); err != nil {
		return exitConfig
	}
	req := localapi.Request{}
	switch args[0] {
	case "whoami":
		req.Op = localapi.OpWhoami
	case "catalog":
		req.Op = localapi.OpCatalog
	case "request":
		if *action == "" || *app == "" && *action != protocol.SelfServiceReboot {
			fmt.Fprintln(stderr, "patchd-agent: o request leva -app e -action (o reiniciar, só -action e, se quiser, -at)")
			return exitConfig
		}
		req = localapi.Request{Op: localapi.OpRequest, App: *app, Action: *action}
		if *at != "" {
			t, err := protocol.NextClock(*at, time.Now())
			if err != nil {
				fmt.Fprintf(stderr, "patchd-agent: -at: %v\n", err)
				return exitConfig
			}
			req.At = &t
		}
	default:
		fmt.Fprintf(stderr, "patchd-agent: comando desconhecido %q\n\n%s", args[0], selfServiceUsage)
		return exitConfig
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "patchd-agent: argumentos não reconhecidos: %q\n", fs.Args())
		return exitConfig
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	r, err := localCall(ctx, req)
	if errors.Is(err, localapi.ErrUnsupported) {
		fmt.Fprintf(stderr, "patchd-agent: %v\n", err)
		return exitConfig
	}
	if err != nil {
		fmt.Fprintf(stderr, "patchd-agent: %v\n", err)
		return exitRuntime
	}
	if !r.OK {
		fmt.Fprintf(stderr, "patchd-agent: %s (%s)\n", r.Error, r.Code)
		return exitRuntime
	}

	switch req.Op {
	case localapi.OpWhoami:
		fmt.Fprintf(stdout, "conta: %s\n", r.Account)
		fmt.Fprintf(stdout, "grupos: %d\n", len(r.Groups))
		if *groups {
			for _, g := range r.Groups {
				fmt.Fprintf(stdout, "  %s\n", g)
			}
		}
	case localapi.OpCatalog:
		tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "PROGRAMA\tINSTALADO\tAÇÃO\tVERSÃO\tOBSERVAÇÃO")
		for _, it := range r.Catalog.Items {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", it.App, dash(it.Installed), it.Action, dash(it.Target), dash(it.Reason))
		}
		_ = tw.Flush()
		fmt.Fprintf(stdout, "%d programa(s) na loja\n", len(r.Catalog.Items))
		if r.Catalog.OpenJob > 0 {
			fmt.Fprintf(stdout, "instalação em andamento nesta máquina: job %d\n", r.Catalog.OpenJob)
		}
		for _, j := range r.Catalog.Requests { // Aula 17.5
			fmt.Fprintf(stdout, "pedido: job %d · %s %s · %s\n", j.ID, j.Name, j.Version, j.State)
		}
		if rb := r.Catalog.Reboot; rb != nil { // Aula 17.6
			switch {
			case rb.JobID > 0 && rb.At != nil:
				fmt.Fprintf(stdout, "reinício agendado: job %d, às %s\n", rb.JobID, rb.At.Local().Format("15:04"))
			case rb.JobID > 0:
				fmt.Fprintf(stdout, "reinício pedido: job %d\n", rb.JobID)
			case rb.Pending != nil && *rb.Pending:
				fmt.Fprintln(stdout, "reinício pendente: escolha a hora com request -action reiniciar [-at HH:MM]")
			case rb.Pending != nil:
				fmt.Fprintln(stdout, "reinício pendente: não")
			}
		}
	case localapi.OpRequest:
		fmt.Fprintf(stdout, "%s: %s\n", r.Result.Status, r.Result.Message)
		if r.Result.Status != protocol.SelfServiceCreated {
			return exitRuntime
		}
		fmt.Fprintf(stderr, "job %d criado; o agente faz o check-in agora e começa a instalação\n", r.Result.JobID)
	}
	return exitOK
}

func dash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
