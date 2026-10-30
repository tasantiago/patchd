package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/biospw"
	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/machinebox"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

// A cópia de emergência da guarda (docs/bios-senha.md, seção 9). O export tira do banco as
// senhas que podem estar valendo, ainda fechadas para a chave de guarda: o arquivo pode ir
// para qualquer cópia de segurança, porque sem a chave privada não revela nada. O reveal
// abre uma cópia (do arquivo ou do Registro da máquina) num computador qualquer, sem servidor
// e sem banco: é o caminho de quando o servidor caiu.

// biosExportFormat identifica o arquivo (e muda se o formato mudar).
const biosExportFormat = "patchd-bios-export-1"

// biosExportStates: as senhas que podem estar valendo na BIOS. A pendente entra porque um
// job pode estar gravando-a agora; a falhou e a substituída, não.
var biosExportStates = []string{store.BIOSActive, store.BIOSUncertain, store.BIOSPending}

type biosExportFile struct {
	Format    string          `json:"format"`
	CreatedAt time.Time       `json:"created_at"`
	Note      string          `json:"note"`
	Passwords []biosExportRow `json:"passwords"`
}

type biosExportRow struct {
	MachineID string    `json:"machine_id"`
	Hostname  string    `json:"hostname,omitempty"`
	Retired   bool      `json:"retired,omitempty"`
	Guard     int64     `json:"guard"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	Copy      string    `json:"copy"` // biospw.Copy
}

type biosExportStore interface {
	Machines(ctx context.Context) ([]protocol.MachineSummary, error)
	RetiredMachines(ctx context.Context) ([]store.RetiredMachine, error)
	BIOSPasswords(ctx context.Context, machineID string) ([]store.BIOSPassword, error)
}

func runBiosExport(args []string, look config.Lookup, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("bios export", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", "", "arquivo da cópia (substituído por inteiro a cada exportação)")
	if err := fs.Parse(args); err != nil || *out == "" || fs.NArg() > 0 {
		fmt.Fprintln(stderr, "patchd-server: informe -out ARQUIVO para a cópia das senhas cifradas")
		return exitConfig
	}
	cfg, err := loadServerConfig(nil, look, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: configuração inválida:\n%v\n", err)
		return exitConfig
	}
	if cfg.DatabaseURL == "" {
		fmt.Fprintln(stderr, "patchd-server: o bios export exige o PostgreSQL (PATCHD_DATABASE_URL ou PATCHD_DATABASE_URL_FILE)")
		return exitConfig
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := store.Connect(ctx, cfg.DatabaseURL, 10*time.Second)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	defer pool.Close()
	n, machines, err := biosExport(ctx, store.NewPostgres(pool), *out, time.Now().UTC())
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	fmt.Fprintf(stdout, "%d senhas cifradas de %d máquinas em %s\n", n, machines, *out)
	return exitOK
}

// biosExport grava o arquivo: num temporário ao lado e depois renomeado, para uma exportação
// agendada nunca deixar meio arquivo no lugar da cópia boa.
func biosExport(ctx context.Context, st biosExportStore, out string, now time.Time) (int, int, error) {
	active, err := st.Machines(ctx)
	if err != nil {
		return 0, 0, err
	}
	retired, err := st.RetiredMachines(ctx)
	if err != nil {
		return 0, 0, err
	}
	type machine struct {
		id, hostname string
		retired      bool
	}
	all := make([]machine, 0, len(active)+len(retired))
	for _, m := range active {
		all = append(all, machine{m.ID, m.Hostname, false})
	}
	for _, m := range retired {
		all = append(all, machine{m.ID, m.Hostname, true})
	}
	file := biosExportFile{Format: biosExportFormat, CreatedAt: now, Passwords: []biosExportRow{},
		Note: "Senhas da BIOS fechadas para a chave de guarda; sem a chave privada não revelam nada. " +
			"Para revelar sem o servidor: patchd-server bios reveal -key CHAVE -file ESTE_ARQUIVO -machine ID_OU_NOME"}
	machines := 0
	for _, m := range all {
		list, err := st.BIOSPasswords(ctx, m.id)
		if err != nil {
			return 0, 0, err
		}
		found := false
		for _, b := range list {
			if !isExportState(b.State) {
				continue
			}
			c, err := biospw.Copy{MachineID: b.MachineID, Guard: b.ID, KeyID: b.EscrowKeyID, Escrow: b.Escrow}.Format()
			if err != nil {
				return 0, 0, fmt.Errorf("senha #%d da máquina %s: %w", b.ID, b.MachineID, err)
			}
			file.Passwords = append(file.Passwords, biosExportRow{MachineID: m.id, Hostname: m.hostname, Retired: m.retired,
				Guard: b.ID, State: b.State, CreatedAt: b.CreatedAt, Copy: c})
			found = true
		}
		if found {
			machines++
		}
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return 0, 0, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(out), ".bios-export-*") // 0600
	if err != nil {
		return 0, 0, err
	}
	defer os.Remove(tmp.Name()) // sem efeito depois do Rename
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return 0, 0, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return 0, 0, err
	}
	if err := tmp.Close(); err != nil {
		return 0, 0, err
	}
	if err := os.Rename(tmp.Name(), out); err != nil {
		return 0, 0, err
	}
	return len(file.Passwords), machines, nil
}

func isExportState(s string) bool {
	for _, x := range biosExportStates {
		if s == x {
			return true
		}
	}
	return false
}

// biosStdin: de onde o reveal lê a chave com -key - (trocado nos testes).
var biosStdin io.Reader = os.Stdin

// runBiosReveal abre cópias com a chave privada de guarda. Não usa configuração nem banco.
func runBiosReveal(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("bios reveal", flag.ContinueOnError)
	fs.SetOutput(stderr)
	keyFile := fs.String("key", "", "arquivo da chave privada de guarda (- lê da entrada)")
	copyText := fs.String("copia", "", "a cópia lida no Registro da máquina (patchd-bios-1:...)")
	exportFile := fs.String("file", "", "o arquivo do bios export")
	machine := fs.String("machine", "", "com -file: o ID da máquina (ou o começo dele) ou o nome")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 || *keyFile == "" ||
		(*copyText == "") == (*exportFile == "") || (*exportFile != "" && *machine == "") {
		fmt.Fprintln(stderr, "patchd-server: use -key CHAVE com -copia TEXTO, ou -key CHAVE -file EXPORT -machine ID_OU_NOME")
		return exitConfig
	}
	priv, err := readEscrowKey(*keyFile)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: chave de guarda: %v\n", err)
		return exitConfig
	}
	defer clear(priv)

	type item struct {
		label string
		copy  string
	}
	var items []item
	if *copyText != "" {
		items = []item{{"cópia do Registro", *copyText}}
	} else {
		rows, err := readExport(*exportFile)
		if err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitConfig
		}
		ids := map[string]bool{}
		for _, r := range rows {
			if r.MachineID == *machine || strings.HasPrefix(r.MachineID, *machine) || strings.EqualFold(r.Hostname, *machine) {
				ids[r.MachineID] = true
				items = append(items, item{fmt.Sprintf("%s (%s), %s", dashed(r.Hostname), r.MachineID, r.State), r.Copy})
			}
		}
		switch {
		case len(ids) == 0:
			fmt.Fprintf(stderr, "patchd-server: nenhuma senha de %q no arquivo\n", *machine)
			return exitRuntime
		case len(ids) > 1:
			fmt.Fprintf(stderr, "patchd-server: %q casa com %d máquinas: use o ID inteiro\n", *machine, len(ids))
			return exitConfig
		}
	}
	code := exitOK
	for _, it := range items {
		c, err := biospw.ParseCopy(it.copy)
		if err == nil {
			var pw string
			if pw, err = c.Reveal(priv); err == nil {
				fmt.Fprintf(stdout, "%s, guarda #%d: %s\n", it.label, c.Guard, biospw.Grouped(pw))
				continue
			}
		}
		fmt.Fprintf(stderr, "patchd-server: %s: %v\n", it.label, err)
		code = exitRuntime
	}
	fmt.Fprintln(stderr, "Revelação offline: não entra na trilha do patchd. Registre no chamado e, com o servidor de volta, "+
		"troque a senha pela guarda (Preparar senha nova).")
	return code
}

func dashed(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// readEscrowKey lê a chave privada de um arquivo, ou da entrada com "-" (colada do cofre de
// senhas, sem passar pelo disco).
func readEscrowKey(path string) ([]byte, error) {
	var raw []byte
	var err error
	if path == "-" {
		line, rerr := bufio.NewReader(io.LimitReader(biosStdin, 4096)).ReadString('\n')
		if rerr != nil && !errors.Is(rerr, io.EOF) {
			return nil, rerr
		}
		raw = []byte(line)
	} else if raw, err = os.ReadFile(path); err != nil { // #nosec G304 -- arquivo da chave informado pelo operador
		return nil, err
	}
	defer clear(raw)
	priv, err := machinebox.ParsePrivate(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, err
	}
	return priv, nil
}

func readExport(path string) ([]biosExportRow, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- arquivo do export informado pelo operador
	if err != nil {
		return nil, err
	}
	var f biosExportFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("%s não é um arquivo do bios export: %w", path, err)
	}
	if f.Format != biosExportFormat {
		return nil, fmt.Errorf("%s: formato %q, esperava %q", path, f.Format, biosExportFormat)
	}
	return f.Passwords, nil
}
