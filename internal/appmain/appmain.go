// Package appmain é a Central de software do usuário (Aula 17.4): o programa que roda na
// sessão de cada usuário, com o ícone perto do relógio e a página local. No Windows ele é um
// executável próprio, patchd-app.exe, compilado como programa gráfico e com o próprio recurso
// de versão; o agente o leva embutido e o grava sem mudar um byte (antes, o serviço montava a
// cópia a partir do próprio executável, trocando o cabeçalho e a descrição: um antivírus
// por aprendizado de máquina via nisso um "dropper"). Fora do Windows, o agente roda o mesmo
// código com "patchd-agent app".
package appmain

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
	"github.com/tasantiago/patchd/internal/winres"
)

// Códigos de saída: os mesmos do agente.
const (
	ExitOK      = 0
	ExitRuntime = 1
	ExitConfig  = 2
)

const usage = `uso: patchd-app [app] [-abrir] [-no-tray]

A Central de software (Aula 17.4): roda na sessão do usuário, mostra o ícone perto do relógio
e abre no navegador a página com as atualizações oferecidas a ele. O serviço do agente põe o
app para abrir no logon de todos (desligue com -user-app=false no serviço).

  -abrir     abre a página no navegador (o atalho da área de trabalho: patchd-app.exe sem
             argumentos); com a Central já aberta na sessão, é ela que abre
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

// openWait: quanto a execução do atalho espera a Central aberta receber o pedido.
const openWait = 5 * time.Second

// Run roda a Central. managed: o caminho da cópia que o serviço do agente mantém, se é dela
// que o app roda (vigiada para reabrir com a cópia nova); vazio: nada a vigiar.
func Run(args []string, stdout, stderr io.Writer, call userapp.Caller, managed string) int {
	fs := flag.NewFlagSet("app", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	noTray := fs.Bool("no-tray", false, "sem ícone: mostra o endereço da página")
	open := fs.Bool("abrir", false, "abre a página no navegador")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return ExitOK
		}
		return ExitConfig
	}
	release, ok := userapp.SingleInstance()
	release = sync.OnceFunc(release) // a reabertura solta o mutex antes de abrir a cópia nova
	defer release()
	if !ok {
		// Já há uma Central aberta nesta sessão: o ícone dela atende; o atalho pede a ela que
		// mostre a página.
		if *open && !userapp.RequestOpen(openWait) {
			fmt.Fprintln(stderr, "patchd-app: a Central aberta nesta sessão não respondeu ao pedido de abrir a página")
		}
		return ExitOK
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Os pedidos de abrir das próximas execuções (o atalho), logo depois do mutex.
	requests, err := userapp.OpenRequests(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-app: o atalho da área de trabalho não vai reabrir a página: %v\n", err)
	}

	// O nome da Central é a descrição do recurso de versão deste executável, gravada na
	// compilação com o nome do painel (o mesmo que o Windows põe no título dos avisos); a página
	// troca pelo nome que o servidor manda na loja.
	if exe, err := os.Executable(); err == nil {
		if b, err := os.ReadFile(exe); err == nil {
			if name, ok := winres.Description(b); ok {
				userapp.SetTitle(name)
			}
		}
	}
	app := userapp.New(call)
	app.Version = buildinfo.Get().Version // Aula 19.1b: o selo na barra superior
	ln, err := app.Listen()
	if err != nil {
		fmt.Fprintf(stderr, "patchd-app: porta local: %v\n", err)
		return ExitRuntime
	}
	served := make(chan error, 1)
	go func() { served <- app.Serve(ctx, ln) }()
	go app.WatchStandardization(ctx, stderr)

	if *noTray {
		fmt.Fprintf(stdout, "%s\n(Ctrl+C encerra)\n", app.URL())
		<-ctx.Done()
		<-served
		return ExitOK
	}
	if *open {
		userapp.OpenURL(app.TabURL(""))
	}
	go func() {
		for range requests { // nil fora do Windows: nunca chega nada
			userapp.OpenURL(app.TabURL(""))
		}
	}()
	notices := make(chan userapp.Notice, 1)
	go app.Watch(ctx, appFirstCheck, appCheckEvery, notices)
	// Aula 18.8: a cópia em disco trocada pela atualização do agente reabre o app com ela.
	renew := make(chan userapp.Renewal, 1)
	self := managed
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
			// #nosec G204 -- reabre o próprio executável (os.Executable)
			if err := exec.Command(self, "app").Start(); err != nil {
				fmt.Fprintf(stderr, "patchd-app: reabrir a Central: %v\n", err)
				return ExitRuntime
			}
		}
		return ExitOK
	default:
	}
	if err != nil {
		fmt.Fprintf(stderr, "patchd-app: %v\n", err)
		return ExitRuntime
	}
	return ExitOK
}
