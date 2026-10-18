package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/selfservice"
	"github.com/tasantiago/patchd/internal/store"
)

// A fila da TI pelo terminal (Aula 18.7): listar, aprovar e recusar os pedidos do
// autoatendimento sem abrir o painel. O serviço é o mesmo do painel (selfservice.Approve e
// Refuse), e a decisão vai para a trilha com a mesma ação; muda só a origem.

// requestRef: "job 12", "pedido à TI nº 3" (ainda sem job) ou "job 12 (pedido à TI nº 3)".
func requestRef(r protocol.SelfServiceJob) string {
	switch {
	case r.ID == 0 && r.Request > 0:
		return fmt.Sprintf("pedido à TI nº %d", r.Request)
	case r.Request > 0:
		return fmt.Sprintf("job %d (pedido à TI nº %d)", r.ID, r.Request)
	}
	return fmt.Sprintf("job %d", r.ID)
}

func selfServiceQueue(ctx context.Context, st selfServiceCLIStore, svc *selfservice.Service, args []string, who string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("selfservice "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	id := fs.Int64("id", 0, "approve/refuse: o número do pedido (a coluna Nº do requests)")
	note := fs.String("note", "", fmt.Sprintf("refuse: o motivo, que o usuário vê em Meus pedidos (até %d letras)", selfservice.MaxNote))
	all := fs.Bool("all", false, "requests: também os decididos e os expirados (os 50 mais novos)")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 {
		return exitConfig
	}
	switch {
	case args[0] == "requests" && (*id != 0 || *note != ""):
		fmt.Fprintln(stderr, "patchd-server: o requests só aceita -all")
		return exitConfig
	case args[0] != "requests" && *id <= 0:
		fmt.Fprintf(stderr, "patchd-server: informe o pedido: selfservice %s -id N (selfservice requests lista)\n", args[0])
		return exitConfig
	case args[0] == "approve" && (*note != "" || *all):
		fmt.Fprintln(stderr, "patchd-server: o approve só aceita -id")
		return exitConfig
	case args[0] == "refuse" && *all:
		fmt.Fprintln(stderr, "patchd-server: o refuse aceita -id e -note")
		return exitConfig
	}
	switch args[0] {
	case "requests":
		return listRequests(ctx, st, svc, *all, stdout, stderr)
	case "approve":
		res, why, err := svc.Approve(ctx, *id, who)
		if code := decisionError(*id, err, stderr); code != exitOK {
			return code
		}
		if why != "" {
			fmt.Fprintf(stderr, "patchd-server: pedido nº %d não aprovado: %s\n", *id, why)
			return exitRuntime
		}
		q := res.Request
		recordDecision(ctx, st, who, "autoatendimento.aprovar", q.MachineID,
			fmt.Sprintf("pedido %d · job %d · %s %s · %s · de %s", q.ID, res.Job.ID, q.AppID, q.Version, q.Action, q.Account), stderr)
		fmt.Fprintf(stdout, "pedido nº %d aprovado: job %d para a máquina %s (%s %s, de %s)\n", q.ID, res.Job.ID, short(q.MachineID), q.AppID, q.Version, q.Account)
		fmt.Fprintln(stderr, "o agente recebe no próximo check-in; com o app do usuário aberto (v0.17.5), em minutos")
		return exitOK
	default: // refuse
		q, why, err := svc.Refuse(ctx, *id, who, *note)
		if code := decisionError(*id, err, stderr); code != exitOK {
			return code
		}
		if why != "" {
			fmt.Fprintf(stderr, "patchd-server: pedido nº %d: %s\n", *id, why)
			return exitRuntime
		}
		recordDecision(ctx, st, who, "autoatendimento.reprovar", q.MachineID,
			fmt.Sprintf("pedido %d · %s %s · %s · de %s · motivo: %s", q.ID, q.AppID, q.Version, q.Action, q.Account, orDash(q.Note)), stderr)
		fmt.Fprintf(stdout, "pedido nº %d recusado (%s %s, de %s); o usuário vê em Meus pedidos\n", q.ID, q.AppID, q.Version, q.Account)
		return exitOK
	}
}

func decisionError(id int64, err error, stderr io.Writer) int {
	switch {
	case err == nil:
		return exitOK
	case errors.Is(err, selfservice.ErrNotFound):
		fmt.Fprintf(stderr, "patchd-server: o pedido nº %d não existe\n", id)
	case errors.Is(err, selfservice.ErrOff):
		fmt.Fprintln(stderr, "patchd-server: o autoatendimento está desligado (sem a chave de jobs ou sem as regras de terceiros)")
		return exitConfig
	default:
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
	}
	return exitRuntime
}

// recordDecision grava a decisão na trilha. Falha não desfaz a decisão, mas aparece.
func recordDecision(ctx context.Context, st selfServiceCLIStore, who, action, target, detail string, stderr io.Writer) {
	err := st.RecordAudit(ctx, store.AuditEvent{At: time.Now().UTC(), Username: who, Source: cliSource, Action: action, Target: target, Detail: detail})
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: aviso: a decisão não foi registrada na auditoria: %v\n", err)
	}
}

func listRequests(ctx context.Context, st selfServiceCLIStore, svc *selfservice.Service, all bool, stdout, stderr io.Writer) int {
	now := svc.Now()
	if _, err := st.ExpireSelfServiceRequests(ctx, now.Add(-store.RequestTTL), now); err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	list, err := st.SelfServiceRequests(ctx, store.RequestFilter{Waiting: !all, All: all, Limit: 50})
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	if len(list) == 0 {
		if all {
			fmt.Fprintln(stdout, "nenhum pedido à TI guardado")
		} else {
			fmt.Fprintln(stdout, "nenhum pedido aguardando a TI")
		}
		return exitOK
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "Nº\tESTADO\tMÁQUINA\tCONTA\tPEDIDO\tVERSÃO\tEM\tDECISÃO")
	waiting := 0
	for _, q := range list {
		decision := "—"
		switch q.State {
		case store.RequestWaiting:
			waiting++
			left := q.CreatedAt.Add(store.RequestTTL).Sub(now)
			decision = fmt.Sprintf("expira em %d dia(s)", max(0, int(left.Hours()/24)))
		case store.RequestApproved:
			decision = fmt.Sprintf("%s · job %d", q.DecidedBy, q.JobID)
		case store.RequestRefused:
			decision = q.DecidedBy + " · motivo: " + orDash(q.Note)
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s %s\t%s\t%s\t%s\n", q.ID, q.State, short(q.MachineID), q.Account, q.Action, q.AppID, q.Version,
			q.CreatedAt.Local().Format("02/01 15:04"), decision)
	}
	_ = tw.Flush()
	fmt.Fprintf(stdout, "%d pedido(s) aguardando a TI", waiting)
	if all {
		fmt.Fprintf(stdout, " entre os %d listados", len(list))
	}
	fmt.Fprintln(stdout)
	return exitOK
}
