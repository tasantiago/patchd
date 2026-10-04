package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"text/tabwriter"
	"time"

	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/store"
)

// Limites dos tokens de enrollment. Um token vazado registra máquinas falsas até
// vencer ou esgotar: prazo curto e poucos usos limitam o estrago.
const (
	tokenMaxTTL  = 30 * 24 * time.Hour
	tokenMaxUses = 10000
)

const tokenUsage = `uso: patchd-server token <comando> [opções]

comandos:
  create   cria um token de enrollment e o mostra uma única vez
  list     lista os tokens (nunca os segredos)
  revoke   revoga um token pelo ID

A URL do banco vem da mesma configuração do servidor (PATCHD_DATABASE_URL ou _FILE).
`

// runToken administra os tokens de enrollment direto no banco, sem passar pela API.
// O segredo do token criado vai sozinho para a saída padrão (para scripts); o resto, para stderr.
func runToken(args []string, look config.Lookup, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, tokenUsage)
		return exitConfig
	}

	cfg, err := loadServerConfig(nil, look, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: configuração inválida:\n%v\n", err)
		return exitConfig
	}
	if cfg.DatabaseURL == "" {
		fmt.Fprintln(stderr, "patchd-server: os tokens de enrollment exigem o PostgreSQL (PATCHD_DATABASE_URL ou PATCHD_DATABASE_URL_FILE)")
		return exitConfig
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// Só avisos e erros: a saída da ferramenta precisa ser limpa.
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	pool, err := store.Connect(ctx, cfg.DatabaseURL, 10*time.Second)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	defer pool.Close()
	// O token pode ser criado antes da primeira partida do servidor: as tabelas vêm daqui também.
	if err := store.Migrate(ctx, pool, logger); err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	st := store.NewPostgres(pool)

	switch args[0] {
	case "create":
		return tokenCreate(ctx, st, args[1:], stdout, stderr)
	case "list":
		return tokenList(ctx, st, stdout, stderr)
	case "revoke":
		return tokenRevoke(ctx, st, args[1:], stderr)
	default:
		fmt.Fprintf(stderr, "patchd-server: comando desconhecido %q\n\n%s", args[0], tokenUsage)
		return exitConfig
	}
}

func tokenCreate(ctx context.Context, st *store.Postgres, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("token create", flag.ContinueOnError)
	fs.SetOutput(stderr)
	uses := fs.Int("uses", 1, "número máximo de máquinas que o token registra")
	ttl := fs.Duration("ttl", 24*time.Hour, "validade, ex.: 2h, 72h (máximo 720h)")
	note := fs.String("note", "", "descrição para a lista, ex.: \"piloto anel 0\"")
	if err := fs.Parse(args); err != nil {
		return exitConfig
	}
	if *uses < 1 || *uses > tokenMaxUses {
		fmt.Fprintf(stderr, "patchd-server: -uses deve ficar entre 1 e %d\n", tokenMaxUses)
		return exitConfig
	}
	if *ttl <= 0 || *ttl > tokenMaxTTL {
		fmt.Fprintln(stderr, "patchd-server: -ttl deve ser positivo e de no máximo 720h (30 dias)")
		return exitConfig
	}

	plain, hash := identity.NewSecret(identity.EnrollmentPrefix)
	expires := time.Now().Add(*ttl).UTC()
	id, err := st.CreateEnrollmentToken(ctx, hash, *note, expires, *uses)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: criar token: %v\n", err)
		return exitRuntime
	}
	fmt.Fprintf(stderr, "token %d criado: até %d uso(s), vence em %s (UTC). Guarde-o agora: ele não será mostrado de novo.\n",
		id, *uses, expires.Format("2006-01-02 15:04"))
	fmt.Fprintln(stdout, plain)
	return exitOK
}

func tokenList(ctx context.Context, st *store.Postgres, stdout, stderr io.Writer) int {
	list, err := st.EnrollmentTokens(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: listar tokens: %v\n", err)
		return exitRuntime
	}
	now := time.Now()
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSITUAÇÃO\tUSOS\tVENCE (UTC)\tNOTA")
	for _, t := range list {
		fmt.Fprintf(tw, "%d\t%s\t%d/%d\t%s\t%s\n", t.ID, t.Status(now), t.Uses, t.MaxUses, t.ExpiresAt.Format("2006-01-02 15:04"), t.Note)
	}
	_ = tw.Flush()
	return exitOK
}

func tokenRevoke(ctx context.Context, st *store.Postgres, args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("token revoke", flag.ContinueOnError)
	fs.SetOutput(stderr)
	id := fs.Int64("id", 0, "ID do token (veja \"token list\")")
	if err := fs.Parse(args); err != nil {
		return exitConfig
	}
	if *id <= 0 {
		fmt.Fprintln(stderr, "patchd-server: informe -id")
		return exitConfig
	}
	ok, err := st.RevokeEnrollmentToken(ctx, *id)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: revogar token: %v\n", err)
		return exitRuntime
	}
	if !ok {
		fmt.Fprintf(stderr, "patchd-server: token %d não existe\n", *id)
		return exitRuntime
	}
	fmt.Fprintf(stderr, "token %d revogado; as máquinas já registradas com ele continuam com as suas credenciais\n", *id)
	return exitOK
}
