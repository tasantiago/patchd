package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/selfservice"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
)

const selfServiceUsage = `uso: patchd-server selfservice <comando> [opções]

Simula o agente no autoatendimento (Aula 17.2), com o mesmo serviço da API: serve para
conferir a loja e o pedido antes de existir o app do usuário (17.3 e 17.4).

comandos:
  catalog  a loja do usuário na máquina: -machine ID -user DOMINIO\usuario [-groups DOMINIO\G1,DOMINIO\G2]
  request  o pedido: as mesmas opções, mais -app PROGRAMA -action atualizar|instalar.
           Atendido, cria o job thirdparty de verdade (autor "autoatendimento: usuario");
           o pedido e a recusa entram na auditoria com a origem "autoatendimento (linha de comando)"

Os grupos são os do token do Windows do usuário (whoami /groups na máquina mostra os
mesmos). A chave de jobs vem de PATCHD_JOB_SIGNING_KEY_FILE; os instaladores, de
PATCHD_CONTENT_DIR/pacotes; as regras, de PATCHD_THIRDPARTY_MANIFEST (vazio: as embutidas).
`

type selfServiceCLIStore interface {
	selfservice.Store
	machineResolver
	RecordAudit(ctx context.Context, e store.AuditEvent) error
}

func runSelfService(args []string, look config.Lookup, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "catalog" && args[0] != "request") {
		fmt.Fprint(stderr, selfServiceUsage)
		return exitConfig
	}
	path, _ := look("PATCHD_THIRDPARTY_MANIFEST")
	manifest, err := thirdparty.Load(path)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitConfig
	}
	dir, err := contentDir(look)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitConfig
	}
	cfg, err := loadServerConfig(nil, look, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: configuração inválida:\n%v\n", err)
		return exitConfig
	}
	if cfg.DatabaseURL == "" {
		fmt.Fprintln(stderr, "patchd-server: o autoatendimento exige o PostgreSQL (PATCHD_DATABASE_URL ou PATCHD_DATABASE_URL_FILE)")
		return exitConfig
	}
	svc := &selfservice.Service{Manifest: &manifest, Now: time.Now}
	if dir != "" {
		svc.PackagesDir = packagesDir(dir)
	}
	if args[0] == "request" {
		if svc.Key, err = jobSigningKey(look); err == nil && svc.Key == nil {
			err = errNoJobKey
		}
		if err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitConfig
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := store.Connect(ctx, cfg.DatabaseURL, 10*time.Second)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	defer pool.Close()
	if err := store.Migrate(ctx, pool, slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))); err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	pg := store.NewPostgres(pool)
	svc.Store = pg
	return selfServiceCommand(ctx, pg, svc, args, stdout, stderr)
}

func selfServiceCommand(ctx context.Context, st selfServiceCLIStore, svc *selfservice.Service, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("selfservice "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	prefix := fs.String("machine", "", "ID (ou começo do ID) da máquina")
	user := fs.String("user", "", `conta do usuário: DOMINIO\usuario`)
	groups := fs.String("groups", "", `grupos do token, separados por vírgula: DOMINIO\G1,DOMINIO\G2`)
	app := fs.String("app", "", "request: o programa (7zip)")
	action := fs.String("action", "", "request: atualizar ou instalar")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 {
		return exitConfig
	}
	u := protocol.SelfServiceUser{Account: strings.TrimSpace(*user), Groups: []string{}}
	for _, g := range strings.Split(*groups, ",") {
		if g = strings.TrimSpace(g); g != "" {
			u.Groups = append(u.Groups, g)
		}
	}
	switch {
	case *prefix == "" || u.Account == "":
		fmt.Fprintln(stderr, "patchd-server: informe -machine e -user")
		return exitConfig
	case u.Check() != nil:
		fmt.Fprintf(stderr, "patchd-server: -user/-groups: %v\n", u.Check())
		return exitConfig
	case args[0] == "request" && (*app == "" || *action == ""):
		fmt.Fprintln(stderr, "patchd-server: o request leva -app e -action")
		return exitConfig
	}
	id, code := resolveActive(ctx, st, *prefix, stderr)
	if code != exitOK {
		return code
	}
	if args[0] == "catalog" {
		cat, err := svc.Catalog(ctx, id, u)
		if err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitRuntime
		}
		tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "PROGRAMA\tINSTALADO\tAÇÃO\tVERSÃO\tOBSERVAÇÃO")
		for _, it := range cat.Items {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", it.App, orDash(it.Installed), it.Action, orDash(it.Target), orDash(it.Reason))
		}
		_ = tw.Flush()
		fmt.Fprintf(stdout, "%d programa(s) na loja\n", len(cat.Items))
		if cat.OpenJob > 0 {
			fmt.Fprintf(stdout, "instalação em andamento na máquina: job %d\n", cat.OpenJob)
		}
		return exitOK
	}
	o, err := svc.Request(ctx, id, protocol.SelfServiceRequest{SelfServiceUser: u, App: *app, Action: *action})
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	e := store.AuditEvent{At: time.Now().UTC(), Username: u.Account, Source: "autoatendimento (linha de comando)", Target: id}
	if o.Job != nil {
		e.Action, e.Detail = "autoatendimento.pedido", fmt.Sprintf("job %d · %s %s · %s · visto conectado: %s", o.Job.ID, *app, o.Version, *action, yesNoCLI(o.Seen))
	} else {
		e.Action, e.Detail = "autoatendimento.recusado", fmt.Sprintf("%s · %s · %s: %s", *app, *action, o.Response.Status, o.Why)
	}
	if err := st.RecordAudit(ctx, e); err != nil {
		fmt.Fprintf(stderr, "patchd-server: auditoria não gravada: %v\n", err)
	}
	fmt.Fprintf(stdout, "%s: %s\n", o.Response.Status, o.Response.Message)
	if o.Why != "" {
		fmt.Fprintf(stderr, "motivo: %s\n", o.Why)
	}
	if o.Job == nil {
		return exitRuntime
	}
	fmt.Fprintf(stderr, "job %d criado para a máquina %s; o agente recebe no próximo check-in\n", o.Job.ID, short(id))
	return exitOK
}

func yesNoCLI(b bool) string {
	if b {
		return "sim"
	}
	return "não"
}
