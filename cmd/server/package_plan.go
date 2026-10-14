package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/tasantiago/patchd/internal/pkgcache"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
	"github.com/tasantiago/patchd/internal/tpinstall"
)

// planStore: o que o plano usa do armazenamento.
type planStore interface {
	packageStore
	Machines(ctx context.Context) ([]protocol.MachineSummary, error)
	LatestInventory(ctx context.Context, id string) (protocol.InventoryReport, bool, error)
	ResolveMachine(ctx context.Context, prefix string) (string, bool, error)
}

var stateLabels = map[string]string{
	tpinstall.StateUpdate:     "ATUALIZAR",
	tpinstall.StateNoPackage:  "SEM PACOTE",
	tpinstall.StateDuplicate:  "DUPLICADO",
	tpinstall.StateBundle:     "BUNDLE",
	tpinstall.StateUserOnly:   "USUÁRIO",
	tpinstall.StateBadVersion: "VERSÃO ILEGÍVEL",
	tpinstall.StateCurrent:    "EM DIA",
}

// packagePlan mostra a decisão de cada programa numa máquina, ou o resumo da frota.
func packagePlan(ctx context.Context, st planStore, manifest thirdparty.Manifest, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("package plan", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, packageUsage) }
	machine := fs.String("machine", "", "")
	all := fs.Bool("all", false, "")
	if err := fs.Parse(args); err != nil {
		return exitConfig
	}
	if (*machine == "") == !*all || fs.NArg() > 0 {
		fmt.Fprintln(stderr, "patchd-server: use um (e só um) de -machine e -all")
		return exitConfig
	}
	pkgs, err := st.ThirdPartyPackages(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	approvals, err := st.ThirdPartyApprovals(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	approved := approvalIndex(approvals)
	if *machine != "" {
		// O começo do ID basta, como no machine list (que mostra 8 caracteres).
		id, ok, err := st.ResolveMachine(ctx, *machine)
		switch {
		case errors.Is(err, store.ErrMachineAmbiguous):
			fmt.Fprintf(stderr, "patchd-server: %q é o começo de mais de uma máquina; use mais caracteres do ID\n", *machine)
			return exitConfig
		case err != nil:
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitRuntime
		case !ok:
			fmt.Fprintf(stderr, "patchd-server: nenhuma máquina com o ID (ou começo de ID) %q\n", *machine)
			return exitConfig
		}
		inv, found, err := st.LatestInventory(ctx, id)
		switch {
		case err != nil:
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitRuntime
		case !found:
			fmt.Fprintf(stderr, "patchd-server: a máquina %s ainda não mandou inventário\n", id)
			return exitConfig
		case inv.OS.ID != "windows":
			fmt.Fprintf(stderr, "patchd-server: a máquina %s é %s; a instalação de terceiros é só para Windows\n", id, inv.OS.Name)
			return exitConfig
		}
		*machine = id
		arch, ok := tpinstall.MachineArch(inv.OS.Arch)
		if !ok {
			fmt.Fprintf(stderr, "patchd-server: arquitetura do agente %q desconhecida\n", inv.OS.Arch)
			return exitRuntime
		}
		ds := tpinstall.Plan(inv.Software, manifest, pkgs, arch)
		fmt.Fprintf(stdout, "máquina %s · %s %s · %s\n", *machine, inv.OS.Name, inv.OS.Version, arch)
		if len(ds) == 0 {
			fmt.Fprintln(stdout, "\nnenhum programa com \"install\" na regra está instalado nesta máquina")
		}
		for _, d := range ds {
			printDecision(d, approved, stdout)
		}
		return exitOK
	}

	// A frota: máquinas Windows com inventário; por programa, quantas em cada estado.
	machines, err := st.Machines(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	counts := map[string]map[string]int{}
	var apps []string
	evaluated, noInventory, otherArch := 0, 0, 0
	for _, m := range machines {
		inv, found, err := st.LatestInventory(ctx, m.ID)
		if err != nil {
			fmt.Fprintf(stderr, "patchd-server: %s: %v\n", m.ID, err)
			return exitRuntime
		}
		if !found {
			noInventory++
			continue
		}
		if inv.OS.ID != "windows" {
			continue
		}
		arch, ok := tpinstall.MachineArch(inv.OS.Arch)
		if !ok {
			otherArch++
			continue
		}
		evaluated++
		for _, d := range tpinstall.Plan(inv.Software, manifest, pkgs, arch) {
			if counts[d.App.ID] == nil {
				counts[d.App.ID] = map[string]int{}
				apps = append(apps, d.App.ID)
			}
			counts[d.App.ID][d.State]++
		}
	}
	fmt.Fprintf(stdout, "máquinas Windows avaliadas: %d", evaluated)
	if noInventory > 0 || otherArch > 0 {
		fmt.Fprintf(stdout, " (sem inventário: %d; arquitetura desconhecida: %d)", noInventory, otherArch)
	}
	fmt.Fprintln(stdout)
	if len(apps) == 0 {
		fmt.Fprintln(stdout, "nenhum programa com \"install\" na regra foi encontrado")
		return exitOK
	}
	fmt.Fprintln(stdout)
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	header := []string{"PROGRAMA"}
	for _, s := range tpinstall.States {
		header = append(header, stateLabels[s])
	}
	fmt.Fprintln(tw, strings.Join(header, "\t"))
	for _, a := range sortedApps(manifest, apps) {
		row := []string{a}
		for _, s := range tpinstall.States {
			row = append(row, strconv.Itoa(counts[a][s]))
		}
		fmt.Fprintln(tw, strings.Join(row, "\t"))
	}
	tw.Flush()
	return exitOK
}

// sortedApps: na ordem do manifesto.
func sortedApps(m thirdparty.Manifest, apps []string) []string {
	var out []string
	for _, a := range m.For("windows") {
		for _, id := range apps {
			if id == a.ID {
				out = append(out, id)
			}
		}
	}
	return out
}

func printDecision(d tpinstall.Decision, approved map[string]store.ThirdPartyApproval, out io.Writer) {
	fmt.Fprintf(out, "\n%s (%s)\n", d.App.Name, d.App.ID)
	fmt.Fprintf(out, "  decisão     %s: %s\n", stateLabels[d.State], d.Reason)
	if d.State == tpinstall.StateUpdate && d.Package != nil {
		fmt.Fprintf(out, "  pacote      %s\n", filepath.Join("pacotes", pkgcache.FileName(d.Package.SHA256, d.Package.MSI())))
		if a, ok := approved[store.TPApprovalKey(d.App.ID, d.Package.Version)]; ok {
			fmt.Fprintf(out, "  aprovação   %s, %s UTC\n", a.ApprovedBy, a.ApprovedAt.UTC().Format("02/01/2006 15:04"))
		} else {
			fmt.Fprintf(out, "  aprovação   aguardando: package approve -app %s -version %s\n", d.App.ID, d.Package.Version)
		}
	}
	for _, e := range d.Entries {
		fmt.Fprintf(out, "  instalado   %s · %s · %s\n", e.Software.Name, e.Kind, e.Software.Version)
	}
	for _, e := range d.User {
		fmt.Fprintf(out, "  usuário     %s · %s · %s\n", e.Software.Name, e.Kind, e.Software.Version)
	}
}
