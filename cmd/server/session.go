package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/store"
)

// Sessão para varredura de segurança (Aula 20.4). O ZAP percorre o painel como um usuário
// logado: precisa do cookie de uma sessão. Em vez de copiar o cookie do navegador (a sessão
// de quem está trabalhando, com o perfil dele), o operador abre uma sessão só para isso:
// perfil escolhido (leitura, para a varredura ativa), prazo curto, origem "varredura" na
// tela de sessões e na trilha, e fim com "session end".

const sessionUsage = `uso: patchd-server session <comando> [opções]

comandos:
  create  abre uma sessão do painel para uma ferramenta de varredura e imprime o valor do
          cookie patchd_sessao (só ele, na saída padrão, uma vez):
            -role leitura|admin   perfil (padrão: leitura)
            -hours N              prazo, de 1 a 8 horas (padrão: 2)
            -name NOME            usuário que aparece nas sessões e na trilha (padrão: varredura)
  end     encerra todas as sessões de -name NOME (padrão: varredura)

A varredura ativa do ZAP manda formulários: use sempre -role leitura nela. O valor
impresso é um segredo: vai direto para um arquivo com permissão 600, nunca para o terminal
de outra pessoa nem para um argumento de comando.

  (umask 077; patchd-server session create -role leitura > zap/relatorios/.sessao)
`

const (
	defaultScanUser  = "varredura"
	maxScanHours     = 8
	defaultScanHours = 2
)

type sessionStore interface {
	CreatePanelSession(ctx context.Context, tokenHash []byte, s panel.Session) error
	DeleteUserSessions(ctx context.Context, username string) (int64, error)
}

func runSession(args []string, look config.Lookup, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = io.WriteString(stderr, sessionUsage)
		return exitConfig
	}
	cfg, err := loadServerConfig(nil, look, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: configuração inválida:\n%v\n", err)
		return exitConfig
	}
	if cfg.DatabaseURL == "" {
		fmt.Fprintln(stderr, "patchd-server: as sessões do painel exigem o PostgreSQL (PATCHD_DATABASE_URL ou PATCHD_DATABASE_URL_FILE)")
		return exitConfig
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	pool, err := store.Connect(ctx, cfg.DatabaseURL, 10*time.Second)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	defer pool.Close()
	if err := store.Migrate(ctx, pool, logger); err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	return sessionCommand(ctx, store.NewPostgres(pool), args, time.Now(), stdout, stderr)
}

func sessionCommand(ctx context.Context, st sessionStore, args []string, now time.Time, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("session "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	name := fs.String("name", defaultScanUser, "usuário que aparece nas sessões e na trilha")
	var role *string
	var hours *int
	switch args[0] {
	case "create":
		role = fs.String("role", panel.RoleRead, "perfil: leitura ou admin")
		hours = fs.Int("hours", defaultScanHours, "prazo em horas, de 1 a 8")
	case "end":
	default:
		fmt.Fprintf(stderr, "patchd-server: comando desconhecido %q\n\n%s", args[0], sessionUsage)
		return exitConfig
	}
	if err := fs.Parse(args[1:]); err != nil {
		return exitConfig
	}
	user := panel.NormalizeUsername(*name)
	if !panel.ValidUsername(user) {
		fmt.Fprintln(stderr, "patchd-server: -name inválido: use minúsculas, dígitos, ponto, hífen e sublinhado, de 2 a 64 caracteres")
		return exitConfig
	}
	if args[0] == "end" {
		n, err := st.DeleteUserSessions(ctx, user)
		if err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitRuntime
		}
		fmt.Fprintf(stderr, "%d sessão(ões) de %s encerrada(s)\n", n, user)
		return exitOK
	}
	if !panel.ValidRole(*role) {
		fmt.Fprintf(stderr, "patchd-server: -role deve ser %s ou %s\n", panel.RoleRead, panel.RoleAdmin)
		return exitConfig
	}
	if *hours < 1 || *hours > maxScanHours {
		fmt.Fprintf(stderr, "patchd-server: -hours vai de 1 a %d\n", maxScanHours)
		return exitConfig
	}
	plain, hash := identity.NewSecret(panel.SessionPrefix)
	s := panel.Session{Username: user, DisplayName: "Varredura de segurança", Role: *role, Source: panel.SourceScan,
		CreatedAt: now, ExpiresAt: now.Add(time.Duration(*hours) * time.Hour), LastSeenAt: now}
	if err := st.CreatePanelSession(ctx, hash, s); err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	fmt.Fprintln(stdout, plain)
	fmt.Fprintf(stderr, "sessão de %s aberta (perfil %s, vale até %s; inatividade de %s também a encerra); fim: patchd-server session end -name %s\n",
		user, *role, s.ExpiresAt.Format("02/01/2006 15:04"), panel.SessionIdle, user)
	if *role == panel.RoleAdmin {
		fmt.Fprintln(stderr, "atenção: perfil admin. Use só na varredura passiva (o ZAP não manda formulários nela).")
	}
	return exitOK
}
