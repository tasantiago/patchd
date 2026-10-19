package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/tasantiago/patchd/internal/buildinfo"
	"github.com/tasantiago/patchd/internal/userapp"
)

const appUsage = `uso: patchd-agent app [-no-tray]

A Central de software (Aula 17.4): roda na sessão do usuário, mostra o ícone perto do relógio
e abre no navegador a página com as atualizações oferecidas a ele. O serviço do agente põe o
app para abrir no logon de todos (desligue com -user-app=false no serviço).

  -no-tray   sem ícone: mostra o endereço da página no terminal (testes e outros sistemas)
`

// Intervalos do aviso no ícone: a primeira conferência espera o logon assentar.
const (
	appFirstCheck = 2 * time.Minute
	appCheckEvery = 4 * time.Hour
)

// appRenewEvery: de quanto em quanto o app confere se a cópia dele foi trocada por uma
// atualização do agente (Aula 18.8).
const appRenewEvery = time.Minute

func runApp(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("app", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, appUsage) }
	noTray := fs.Bool("no-tray", false, "sem ícone: mostra o endereço da página")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitConfig
	}
	release, ok := userapp.SingleInstance()
	release = sync.OnceFunc(release) // a reabertura solta o mutex antes de abrir a cópia nova
	defer release()
	if !ok && !*noTray {
		return exitOK // já há uma Central aberta nesta sessão: o ícone dela atende
	}

	app := userapp.New(userapp.Caller(localCall))
	app.Version = buildinfo.Get().Version // Aula 19.1b: o selo na barra superior
	ln, err := app.Listen()
	if err != nil {
		fmt.Fprintf(stderr, "patchd-agent: porta local: %v\n", err)
		return exitRuntime
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- app.Serve(ctx, ln) }()

	if *noTray {
		fmt.Fprintf(stdout, "%s\n(Ctrl+C encerra)\n", app.URL())
		<-ctx.Done()
		<-served
		return exitOK
	}
	notices := make(chan userapp.Notice, 1)
	go app.Watch(ctx, appFirstCheck, appCheckEvery, notices)
	// Aula 18.8: a cópia em disco trocada pela atualização do agente reabre o app com ela.
	renew := make(chan userapp.Renewal, 1)
	self := managedAppPath()
	if self != "" {
		if w, err := userapp.WatchSelf(self); err == nil {
			go func() {
				if r := w.Wait(ctx, appRenewEvery); r != userapp.RenewNone {
					renew <- r
					cancel()
				}
			}()
		}
	}
	err = userapp.RunTray(ctx, userapp.TrayOptions{Tooltip: userapp.Title, Open: func(tab string) { userapp.OpenURL(app.TabURL(tab)) }, Notices: notices})
	cancel()
	<-served
	select {
	case r := <-renew:
		if r == userapp.RenewReplaced {
			release()
			if err := exec.Command(self, "app").Start(); err != nil {
				fmt.Fprintf(stderr, "patchd-agent: reabrir a Central: %v\n", err)
				return exitRuntime
			}
		}
		return exitOK
	default:
	}
	if err != nil {
		fmt.Fprintf(stderr, "patchd-agent: %v\n", err)
		return exitRuntime
	}
	return exitOK
}
