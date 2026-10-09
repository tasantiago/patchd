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
  pubkey  mostra a chave pública da chave privada configurada (para conferir ou refazer
          o PATCHD_JOB_PUBKEY) e a impressão curta que aparece nos logs
  create  cria um job: -machine ID -type rescan [-ttl 24h]. O agente o recebe no próximo
          check-in, confere a assinatura e executa
  list    lista os jobs, do mais novo para o mais antigo: [-machine ID] [-n 20]
  cancel  cancela um job ainda não entregue: -id N

Tipos: rescan (coleta o inventário e faz a busca de atualizações agora).
A chave privada vem de PATCHD_JOB_SIGNING_KEY_FILE (ou PATCHD_JOB_SIGNING_KEY); o create
e o pubkey precisam dela. Com ela, o servidor também cria jobs pelo painel (Aula 8.2).
A URL do banco vem da configuração do servidor.
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
	// Anéis e janelas (Aula 8.3).
	MachineRing(ctx context.Context, id string) (int, bool, error)
	RingMachines(ctx context.Context, ring int) ([]string, error)
	Windows(ctx context.Context) ([]store.RingWindow, error)
}

func runJob(args []string, look config.Lookup, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = io.WriteString(stderr, jobUsage)
		return exitConfig
	}
	switch args[0] {
	case "keygen":
		return jobKeygen(args[1:], stdout, stderr)
	case "pubkey":
		return jobPubkey(look, stdout, stderr)
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
		if key, err = jobSigningKey(look); err == nil && key == nil {
			err = errNoJobKey
		}
		if err != nil {
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

var errNoJobKey = errors.New("criar jobs exige a chave privada: defina PATCHD_JOB_SIGNING_KEY_FILE (crie com patchd-server job keygen)")

// jobSigningKey lê a chave privada dos jobs. Sem a variável: nil, sem erro (o servidor
// sobe com os jobs do painel desligados). Variável definida e chave ilegível: erro.
func jobSigningKey(look config.Lookup) (ed25519.PrivateKey, error) {
	s, err := config.Secret(look, "PATCHD_JOB_SIGNING_KEY")
	if err != nil || s == "" {
		return nil, err
	}
	key, err := jobs.ParsePrivateKey(s)
	if err != nil {
		return nil, fmt.Errorf("PATCHD_JOB_SIGNING_KEY: %w", err)
	}
	return key, nil
}

// jobPubkey mostra a chave pública que corresponde à chave privada configurada.
func jobPubkey(look config.Lookup, stdout, stderr io.Writer) int {
	key, err := jobSigningKey(look)
	if err == nil && key == nil {
		err = errNoJobKey
	}
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitConfig
	}
	pub := key.Public().(ed25519.PublicKey)
	fmt.Fprintf(stderr, "impressão da chave (key_id nos logs): %s\n", jobs.KeyID(pub))
	fmt.Fprintln(stdout, jobs.EncodePublicKey(pub))
	return exitOK
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
	fmt.Fprintf(stderr, "impressão da chave (key_id nos logs): %s\n", jobs.KeyID(pub))
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
		ringFlag := fs.Int("ring", -1, "em vez de -machine: um job para cada máquina do anel")
		typ := fs.String("type", "", "tipo do job: rescan")
		ttl := fs.Duration("ttl", 24*time.Hour, "prazo para o agente receber e executar (máximo 168h)")
		inWindow := fs.Bool("window", false, "só na próxima janela do anel da máquina (o prazo é o fim da janela)")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 {
			return exitConfig
		}
		ttlSet := false
		fs.Visit(func(f *flag.Flag) { ttlSet = ttlSet || f.Name == "ttl" })
		switch {
		case !jobs.KnownType(*typ):
			fmt.Fprintln(stderr, "patchd-server: -type deve ser rescan")
			return exitConfig
		case *ttl < jobs.MinTTL || *ttl > jobs.MaxTTL:
			fmt.Fprintln(stderr, "patchd-server: -ttl deve ficar entre 10m e 168h")
			return exitConfig
		case *inWindow && ttlSet:
			fmt.Fprintln(stderr, "patchd-server: -ttl e -window não combinam: na janela, o prazo é o fim dela")
			return exitConfig
		case (*prefix == "") == (*ringFlag < 0):
			fmt.Fprintln(stderr, "patchd-server: informe -machine ou -ring (um dos dois)")
			return exitConfig
		case *ringFlag > store.MaxRing:
			fmt.Fprintf(stderr, "patchd-server: -ring vai de 0 a %d\n", store.MaxRing)
			return exitConfig
		}
		var targets []string
		if *prefix != "" {
			id, code := resolveActive(ctx, st, *prefix, stderr)
			if code != exitOK {
				return code
			}
			targets = []string{id}
		} else {
			ids, err := st.RingMachines(ctx, *ringFlag)
			if err != nil {
				fmt.Fprintf(stderr, "patchd-server: %v\n", err)
				return exitRuntime
			}
			if len(ids) == 0 {
				fmt.Fprintf(stderr, "patchd-server: o anel %d não tem máquinas (ring set)\n", *ringFlag)
				return exitRuntime
			}
			targets = ids
		}
		var windows []store.RingWindow
		if *inWindow {
			var err error
			if windows, err = st.Windows(ctx); err != nil {
				fmt.Fprintf(stderr, "patchd-server: %v\n", err)
				return exitRuntime
			}
		}
		for _, id := range targets {
			req := jobs.Request{MachineID: id, Type: *typ, TTL: *ttl, By: "linha de comando"}
			where := ""
			if *inWindow {
				ring, ok, err := st.MachineRing(ctx, id)
				if err != nil || !ok {
					fmt.Fprintf(stderr, "patchd-server: anel da máquina %s: %v\n", short(id), err)
					return exitRuntime
				}
				rw, ok := store.WindowFor(windows, ring)
				if !ok {
					fmt.Fprintf(stderr, "patchd-server: o anel %d (máquina %s) não tem janela; defina com ring window ou crie sem -window\n", ring, short(id))
					return exitRuntime
				}
				start, end := rw.Window.Next(now, jobs.MinTTL)
				req.NotBefore, req.TTL = start, end.Sub(later(now, start))
				loc := rw.Window.Location
				where = fmt.Sprintf(" na janela do anel %d, de %s a %s (%s)", ring, start.In(loc).Format("02/01 15:04"), end.In(loc).Format("02/01 15:04"), loc)
			}
			rec, err := jobs.Issue(ctx, st, key, req, now)
			if errors.Is(err, jobs.ErrMachineUnavailable) {
				fmt.Fprintf(stderr, "patchd-server: a máquina %s foi aposentada agora há pouco; nada foi criado para ela\n", short(id))
				return exitRuntime
			}
			if err != nil {
				fmt.Fprintf(stderr, "patchd-server: criar job: %v\n", err)
				return exitRuntime
			}
			if where == "" {
				where = fmt.Sprintf("; vale até %s UTC", rec.NotAfter.Format("2006-01-02 15:04"))
			}
			fmt.Fprintf(stderr, "job %d (%s) criado para a máquina %s%s\n", rec.ID, rec.Type, short(id), where)
			fmt.Fprintln(stdout, rec.ID)
		}
		if *inWindow {
			fmt.Fprintln(stderr, "O servidor só entrega a partir do início da janela; o agente confere de novo pelo payload assinado.")
		} else {
			fmt.Fprintln(stderr, "O agente recebe no próximo check-in.")
		}
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

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// machineResolver acha máquinas pelo começo do ID.
type machineResolver interface {
	ResolveMachine(ctx context.Context, prefix string) (string, bool, error)
	Machines(ctx context.Context) ([]protocol.MachineSummary, error)
}

// resolveAny acha a máquina pelo prefixo, aposentada ou não.
func resolveAny(ctx context.Context, st machineResolver, prefix string, stderr io.Writer) (string, int) {
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
func resolveActive(ctx context.Context, st machineResolver, prefix string, stderr io.Writer) (string, int) {
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

// untilText escreve quanto falta: "40 min", "2 h 59 min", "3 dias".
func untilText(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h %02d min", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%d dias", int(d.Hours()/24))
}

// windowStart diz quando a janela do job abre (ou abriu): "em 40 min", "há 5 min", "-".
func windowStart(t *time.Time, now time.Time) string {
	switch {
	case t == nil:
		return "-"
	case t.After(now):
		return "abre em " + untilText(t.Sub(now))
	}
	return "abriu " + since(now.Sub(*t))
}

func printJobs(list []store.JobRow, now time.Time, out io.Writer) {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tMÁQUINA\tTIPO\tESTADO\tJANELA\tCRIADO\tPOR\tENTREGUE\tENCERRADO\tDETALHE")
	at := func(t *time.Time) string {
		if t == nil {
			return "-"
		}
		return since(now.Sub(*t))
	}
	for _, j := range list {
		created := j.CreatedAt
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", j.ID, short(j.MachineID), j.Type, j.State, windowStart(j.NotBefore, now), at(&created),
			j.CreatedBy, at(j.DeliveredAt), at(j.FinishedAt), orDash(strings.TrimSpace(j.Detail)))
	}
	_ = tw.Flush()
	fmt.Fprintf(out, "%d job(s)\n", len(list))
}
