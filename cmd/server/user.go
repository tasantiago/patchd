package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/store"
)

const userUsage = `uso: patchd-server user <comando> [opções]

comandos:
  create    cria um usuário local do painel: -name NOME -role admin|leitura
  list      lista os usuários locais (nunca as senhas)
  password  troca a senha de -name NOME e encerra as sessões dele
  delete    remove -name NOME e encerra as sessões dele

A senha (create, password) vem da primeira linha da entrada padrão, nunca de um
argumento, que ficaria no histórico do shell e na lista de processos:

  read -rsp 'Senha: ' S; echo; printf '%s\n' "$S" | patchd-server user create -name fulano -role admin; unset S

A URL do banco vem da mesma configuração do servidor (PATCHD_DATABASE_URL ou _FILE).
`

// userStore é o que os comandos de usuário usam do armazenamento (o PostgreSQL; a
// memória, nos testes).
type userStore interface {
	CreatePanelUser(ctx context.Context, u panel.User) error
	PanelUsers(ctx context.Context) ([]panel.User, error)
	SetPanelPassword(ctx context.Context, name, hash string) (bool, error)
	DeletePanelUser(ctx context.Context, name string) (bool, error)
}

// runUser administra os usuários locais do painel direto no banco, como os tokens.
func runUser(args []string, look config.Lookup, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = io.WriteString(stderr, userUsage)
		return exitConfig
	}
	cfg, err := loadServerConfig(nil, look, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: configuração inválida:\n%v\n", err)
		return exitConfig
	}
	if cfg.DatabaseURL == "" {
		fmt.Fprintln(stderr, "patchd-server: os usuários do painel exigem o PostgreSQL (PATCHD_DATABASE_URL ou PATCHD_DATABASE_URL_FILE)")
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
	// O primeiro administrador pode ser criado antes da primeira partida do servidor.
	if err := store.Migrate(ctx, pool, logger); err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	return userCommand(ctx, store.NewPostgres(pool), args, stdin, stdout, stderr)
}

func userCommand(ctx context.Context, st userStore, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	switch args[0] {
	case "create":
		return userCreate(ctx, st, args[1:], stdin, stderr)
	case "list":
		return userList(ctx, st, stdout, stderr)
	case "password":
		return userPassword(ctx, st, args[1:], stdin, stderr)
	case "delete":
		return userDelete(ctx, st, args[1:], stderr)
	default:
		fmt.Fprintf(stderr, "patchd-server: comando desconhecido %q\n\n%s", args[0], userUsage)
		return exitConfig
	}
}

// parseUserFlags lê e confere -name (e -role, quando pedido).
func parseUserFlags(cmd string, args []string, withRole bool, stderr io.Writer) (name, role string, ok bool) {
	fs := flag.NewFlagSet("user "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	n := fs.String("name", "", "nome do usuário: minúsculas, dígitos, . _ -, de 2 a 64 caracteres")
	var r *string
	if withRole {
		r = fs.String("role", "", "perfil: admin ou leitura")
	}
	if err := fs.Parse(args); err != nil {
		return "", "", false
	}
	name = panel.NormalizeUsername(*n)
	if !panel.ValidUsername(name) {
		fmt.Fprintln(stderr, "patchd-server: -name inválido: use minúsculas, dígitos, ponto, hífen e sublinhado, de 2 a 64 caracteres")
		return "", "", false
	}
	if withRole {
		role = *r
		if !panel.ValidRole(role) {
			fmt.Fprintf(stderr, "patchd-server: -role deve ser %s ou %s\n", panel.RoleAdmin, panel.RoleRead)
			return "", "", false
		}
	}
	return name, role, true
}

// readPassword lê a senha da primeira linha da entrada padrão e confere a política.
// Recusa o terminal: sem desligar o eco, a senha apareceria na tela.
func readPassword(stdin io.Reader) (string, error) {
	if f, ok := stdin.(*os.File); ok {
		if fi, err := f.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
			return "", errors.New("a senha vem pela entrada padrão redirecionada, não do terminal (veja o exemplo em \"patchd-server user\")")
		}
	}
	line, err := bufio.NewReader(io.LimitReader(stdin, panel.MaxPasswordBytes+2)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	pw := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	if pw == "" {
		return "", errors.New("nenhuma senha na entrada padrão")
	}
	return pw, panel.CheckPasswordPolicy(pw)
}

func userCreate(ctx context.Context, st userStore, args []string, stdin io.Reader, stderr io.Writer) int {
	name, role, ok := parseUserFlags("create", args, true, stderr)
	if !ok {
		return exitConfig
	}
	pw, err := readPassword(stdin)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitConfig
	}
	hash, err := panel.HashPassword(pw)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	err = st.CreatePanelUser(ctx, panel.User{Name: name, Role: role, PasswordHash: hash})
	if errors.Is(err, panel.ErrUserExists) {
		fmt.Fprintf(stderr, "patchd-server: o usuário %s já existe (para trocar a senha: user password)\n", name)
		return exitRuntime
	}
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: criar usuário: %v\n", err)
		return exitRuntime
	}
	fmt.Fprintf(stderr, "usuário %s criado com o perfil %s\n", name, role)
	return exitOK
}

func userList(ctx context.Context, st userStore, stdout, stderr io.Writer) int {
	list, err := st.PanelUsers(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: listar usuários: %v\n", err)
		return exitRuntime
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "USUÁRIO\tPERFIL\tCRIADO (UTC)")
	for _, u := range list {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", u.Name, u.Role, u.CreatedAt.Format("2006-01-02 15:04"))
	}
	_ = tw.Flush()
	return exitOK
}

func userPassword(ctx context.Context, st userStore, args []string, stdin io.Reader, stderr io.Writer) int {
	name, _, ok := parseUserFlags("password", args, false, stderr)
	if !ok {
		return exitConfig
	}
	pw, err := readPassword(stdin)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitConfig
	}
	hash, err := panel.HashPassword(pw)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	found, err := st.SetPanelPassword(ctx, name, hash)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: trocar senha: %v\n", err)
		return exitRuntime
	}
	if !found {
		fmt.Fprintf(stderr, "patchd-server: o usuário %s não existe\n", name)
		return exitRuntime
	}
	fmt.Fprintf(stderr, "senha de %s trocada; as sessões abertas dele foram encerradas\n", name)
	return exitOK
}

func userDelete(ctx context.Context, st userStore, args []string, stderr io.Writer) int {
	name, _, ok := parseUserFlags("delete", args, false, stderr)
	if !ok {
		return exitConfig
	}
	found, err := st.DeletePanelUser(ctx, name)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: remover usuário: %v\n", err)
		return exitRuntime
	}
	if !found {
		fmt.Fprintf(stderr, "patchd-server: o usuário %s não existe\n", name)
		return exitRuntime
	}
	fmt.Fprintf(stderr, "usuário %s removido; as sessões abertas dele foram encerradas\n", name)
	if list, err := st.PanelUsers(ctx); err == nil && countAdmins(list) == 0 {
		fmt.Fprintln(stderr, "atenção: não resta nenhum administrador local no painel")
	}
	return exitOK
}

func countAdmins(list []panel.User) int {
	n := 0
	for _, u := range list {
		if u.Role == panel.RoleAdmin {
			n++
		}
	}
	return n
}

// checkPanelUsers avisa, na partida, quando ninguém consegue entrar no painel.
func checkPanelUsers(ctx context.Context, logger *slog.Logger, st any) {
	if _, ok := st.(*store.Memory); ok {
		logger.Warn("armazenamento em memória: o painel não tem usuários (use o PostgreSQL e patchd-server user create)")
		return
	}
	us, ok := st.(userStore)
	if !ok {
		return
	}
	list, err := us.PanelUsers(ctx)
	switch {
	case err != nil:
		logger.Error("usuários do painel", "error", err)
	case len(list) == 0:
		logger.Warn("nenhum usuário no painel: crie o primeiro administrador com patchd-server user create -name NOME -role admin")
	default:
		logger.Info("usuários locais do painel", "total", len(list), "admins", countAdmins(list))
	}
}
