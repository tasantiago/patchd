package main

import (
	"context"
	"crypto/ed25519"
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
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

const jobUsage = `uso: patchd-server job <comando> [opções]

comandos:
  keygen  cria o par de chaves dos jobs: -out ARQUIVO grava a privada (0600) e a pública
          sai na tela, para o build dos agentes (PATCHD_JOB_PUBKEY)
  create  cria um job: -machine ID -type rescan [-ttl 24h]. O agente o recebe no próximo
          check-in, confere a assinatura e executa
  list    lista os jobs, do mais novo para o mais antigo: [-machine ID] [-n 20]
  cancel  cancela um job ainda não entregue: -id N

Tipos: rescan (coleta o inventário e faz a busca de atualizações agora).
A chave privada vem de PATCHD_JOB_SIGNING_KEY_FILE (ou PATCHD_JOB_SIGNING_KEY); só o
create precisa dela. A URL do banco vem da configuração do servidor.
`

// jobStore é o que os comandos de job usam do banco.
type jobStore interface {
	ResolveMachine(ctx context.Context, prefix string) (string, bool, error)
	Machines(ctx context.Context) ([]protocol.MachineSummary, error)
	NextJobID(ctx context.Context) (int64, error)
	CreateJob(ctx context.Context, j store.NewJob) error
	ExpireJobs(ctx context.Context, now time.Time) (int64, error)
	Jobs(ctx context.Context, machineID string, limit int) ([]store.JobRow, error)
	CancelJob(ctx context.Context, id int64, by string) (bool, error)
}

func runJob(args []string, look config.Lookup, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = io.WriteString(stderr, jobUsage)
		return exitConfig
	}
	if args[0] == "keygen" {
		return jobKeygen(args[1:], stdout, stderr)
	}
	cfg, err := loadServerConfig(nil, look, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: configuração inválida:\n%v\n", err)
		return exitConfig
	}
	if cfg.DatabaseURL == "" {
		fmt.Fprintln(stderr, "patchd-server: os jobs exigem o PostgreSQL (PATCHD_DATABASE_URL ou PATCHD_DATABASE_URL_FILE)")
		return exitConfig
	}
	var key ed25519.PrivateKey
	if args[0] == "create" {
		if key, err = jobSigningKey(look); err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitConfig
		}
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
	return jobCommand(ctx, store.NewPostgres(pool), key, args, time.Now().UTC(), stdout, stderr)
}

// jobSigningKey lê a chave privada dos jobs.
func jobSigningKey(look config.Lookup) (ed25519.PrivateKey, error) {
	s, err := config.Secret(look, "PATCHD_JOB_SIGNING_KEY")
	if err != nil {
		return nil, err
	}
	if s == "" {
		return nil, errors.New("criar jobs exige a chave privada: defina PATCHD_JOB_SIGNING_KEY_FILE (crie com patchd-server job keygen)")
	}
	return jobs.ParsePrivateKey(s)
}

func jobKeygen(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("job keygen", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", "", "arquivo da chave privada (não pode existir)")
	if err := fs.Parse(args); err != nil || *out == "" {
		fmt.Fprintln(stderr, "patchd-server: informe -out ARQUIVO para a chave privada")
		return exitConfig
	}
	pub, priv, err := jobs.GenerateKey()
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	// O_EXCL: nunca sobrescreve uma chave existente (trocar a chave invalida os agentes).
	f, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	if _, err := io.WriteString(f, jobs.EncodePrivateKey(priv)); err != nil {
		f.Close()
		os.Remove(*out)
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	if err := f.Close(); err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	fmt.Fprintf(stderr, "chave privada dos jobs gravada em %s (0600). Ela fica com o servidor, fora do git e da imagem.\n", *out)
	fmt.Fprintln(stderr, "chave pública (PATCHD_JOB_PUBKEY do build dos agentes):")
	fmt.Fprintln(stdout, jobs.EncodePublicKey(pub))
	return exitOK
}

func jobCommand(ctx context.Context, st jobStore, key ed25519.PrivateKey, args []string, now time.Time, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("job "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	switch args[0] {
	case "create":
		prefix := fs.String("machine", "", "ID (ou começo do ID) da máquina")
		typ := fs.String("type", "", "tipo do job: rescan")
		ttl := fs.Duration("ttl", 24*time.Hour, "prazo para o agente receber e executar (máximo 168h)")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 {
			return exitConfig
		}
		if !jobs.KnownType(*typ) {
			fmt.Fprintln(stderr, "patchd-server: -type deve ser rescan")
			return exitConfig
		}
		if *ttl < 10*time.Minute || *ttl > jobs.MaxTTL {
			fmt.Fprintln(stderr, "patchd-server: -ttl deve ficar entre 10m e 168h")
			return exitConfig
		}
		id, code := resolveActive(ctx, st, *prefix, stderr)
		if code != exitOK {
			return code
		}
		jobID, err := st.NextJobID(ctx)
		if err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitRuntime
		}
		j := jobs.Job{ID: jobID, MachineID: id, Type: *typ, IssuedAt: now, NotAfter: now.Add(*ttl)}
		env, err := jobs.Sign(key, j)
		if err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitRuntime
		}
		if err := st.CreateJob(ctx, store.NewJob{ID: jobID, MachineID: id, Type: *typ, Signed: env,
			CreatedBy: "linha de comando", NotAfter: j.NotAfter}); err != nil {
			fmt.Fprintf(stderr, "patchd-server: criar job: %v\n", err)
			return exitRuntime
		}
		fmt.Fprintf(stderr, "job %d (%s) criado para a máquina %s; vale até %s UTC. O agente o recebe no próximo check-in.\n",
			jobID, *typ, short(id), j.NotAfter.Format("2006-01-02 15:04"))
		fmt.Fprintln(stdout, jobID)
		return exitOK
	case "list":
		prefix := fs.String("machine", "", "só os jobs desta máquina")
		n := fs.Int("n", 20, "quantos jobs mostrar")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 || *n < 1 {
			return exitConfig
		}
		id := ""
		if *prefix != "" {
			var code int
			if id, code = resolveAny(ctx, st, *prefix, stderr); code != exitOK {
				return code
			}
		}
		if _, err := st.ExpireJobs(ctx, now); err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitRuntime
		}
		list, err := st.Jobs(ctx, id, *n)
		if err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitRuntime
		}
		printJobs(list, now, stdout)
		return exitOK
	case "cancel":
		id := fs.Int64("id", 0, "ID do job")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 || *id <= 0 {
			fmt.Fprintln(stderr, "patchd-server: informe -id")
			return exitConfig
		}
		ok, err := st.CancelJob(ctx, *id, "linha de comando")
		if err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitRuntime
		}
		if !ok {
			fmt.Fprintf(stderr, "patchd-server: o job %d não existe ou não está pendente (um job entregue não se cancela: o agente pode estar executando)\n", *id)
			return exitRuntime
		}
		fmt.Fprintf(stderr, "job %d cancelado\n", *id)
		return exitOK
	default:
		fmt.Fprintf(stderr, "patchd-server: comando desconhecido %q\n\n%s", args[0], jobUsage)
		return exitConfig
	}
}

// resolveAny acha a máquina pelo prefixo, aposentada ou não.
func resolveAny(ctx context.Context, st jobStore, prefix string, stderr io.Writer) (string, int) {
	if prefix == "" {
		fmt.Fprintln(stderr, "patchd-server: informe -machine")
		return "", exitConfig
	}
	id, ok, err := st.ResolveMachine(ctx, prefix)
	switch {
	case errors.Is(err, store.ErrMachineAmbiguous):
		fmt.Fprintf(stderr, "patchd-server: %q casa com mais de uma máquina; use mais caracteres do ID\n", prefix)
		return "", exitConfig
	case err != nil:
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return "", exitRuntime
	case !ok:
		fmt.Fprintf(stderr, "patchd-server: nenhuma máquina com o ID %q\n", prefix)
		return "", exitRuntime
	}
	return id, exitOK
}

// resolveActive exige uma máquina da frota: job para uma aposentada nunca seria entregue.
func resolveActive(ctx context.Context, st jobStore, prefix string, stderr io.Writer) (string, int) {
	id, code := resolveAny(ctx, st, prefix, stderr)
	if code != exitOK {
		return "", code
	}
	list, err := st.Machines(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return "", exitRuntime
	}
	for _, m := range list {
		if m.ID == id {
			return id, exitOK
		}
	}
	fmt.Fprintf(stderr, "patchd-server: a máquina %s está aposentada; restaure-a antes (machine restore)\n", short(id))
	return "", exitRuntime
}

func printJobs(list []store.JobRow, now time.Time, out io.Writer) {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tMÁQUINA\tTIPO\tESTADO\tCRIADO\tPOR\tENTREGUE\tENCERRADO\tDETALHE")
	at := func(t *time.Time) string {
		if t == nil {
			return "-"
		}
		return since(now.Sub(*t))
	}
	for _, j := range list {
		created := j.CreatedAt
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", j.ID, short(j.MachineID), j.Type, j.State, at(&created),
			j.CreatedBy, at(j.DeliveredAt), at(j.FinishedAt), orDash(strings.TrimSpace(j.Detail)))
	}
	_ = tw.Flush()
	fmt.Fprintf(out, "%d job(s)\n", len(list))
}
