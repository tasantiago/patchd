package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/tasantiago/patchd/internal/canal"
	"github.com/tasantiago/patchd/internal/store"
)

// O canal em tempo real (internal/canal): o hub recebe os avisos do PostgreSQL (o gatilho da
// 0062, em todo job criado) e, a cada dueEvery, os jobs agendados cujo início acabou de
// chegar; e solta as esperas quando o servidor vai encerrar.

const (
	dueEvery      = 30 * time.Second
	listenBackoff = 5 * time.Second
	listenMaxWait = 2 * time.Minute
)

func startChannel(ctx context.Context, logger *slog.Logger, pg *store.Postgres, hub *canal.Hub) {
	go func() {
		<-ctx.Done()
		hub.Close() // o Shutdown do HTTP não espera as esperas de 45 s
	}()
	go func() {
		wait := listenBackoff
		for ctx.Err() == nil {
			ready := func() { wait = listenBackoff; logger.Info("canal em tempo real: escutando os jobs novos no banco") }
			err := pg.ListenJobs(ctx, ready, func(id string) { hub.Notify(id, "job") })
			if ctx.Err() != nil {
				return
			}
			logger.Warn("canal em tempo real: a escuta do banco caiu; os agentes seguem pelo check-in até voltar", "error", err, "nova_tentativa_em", wait.String())
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			wait = min(2*wait, listenMaxWait)
		}
	}()
	go func() {
		last := time.Now()
		t := time.NewTicker(dueEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-t.C:
				ids, err := pg.DueJobMachines(ctx, last, now)
				if err != nil {
					if ctx.Err() == nil {
						logger.Warn("canal em tempo real: jobs agendados que venceram", "error", err)
					}
					continue
				}
				last = now
				for _, id := range ids {
					hub.Notify(id, "job agendado")
				}
			}
		}
	}()
}
