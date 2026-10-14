package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/pkgcache"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
	"github.com/tasantiago/patchd/internal/tpinstall"
)

// installStore: o plano mais a fila de jobs.
type installStore interface {
	planStore
	jobs.Queue
}

// packageInstall cria o job thirdparty para um programa numa máquina (Aula 14.4). Só cria
// quando o plano da máquina diz ATUALIZAR: o job leva o instalador guardado da família da
// máquina, com o SHA-256, o tamanho e os argumentos conferidos.
func packageInstall(ctx context.Context, st installStore, key ed25519.PrivateKey, manifest thirdparty.Manifest, dir string,
	args []string, now time.Time, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("package install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, packageUsage) }
	prefix := fs.String("machine", "", "")
	app := fs.String("app", "", "")
	ttl := fs.Duration("ttl", 24*time.Hour, "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return exitConfig
	}
	switch {
	case *prefix == "" || *app == "":
		fmt.Fprintln(stderr, "patchd-server: informe -machine e -app")
		return exitConfig
	case *ttl < jobs.MinTTL || *ttl > jobs.MaxTTL:
		fmt.Fprintln(stderr, "patchd-server: -ttl deve ficar entre 10m e 168h")
		return exitConfig
	}
	id, code := resolveActive(ctx, st, *prefix, stderr)
	if code != exitOK {
		return code
	}
	inv, found, err := st.LatestInventory(ctx, id)
	switch {
	case err != nil:
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	case !found || inv.OS.ID != "windows":
		fmt.Fprintf(stderr, "patchd-server: a máquina %s não tem inventário do Windows\n", short(id))
		return exitConfig
	}
	arch, ok := tpinstall.MachineArch(inv.OS.Arch)
	if !ok {
		fmt.Fprintf(stderr, "patchd-server: arquitetura do agente %q desconhecida\n", inv.OS.Arch)
		return exitRuntime
	}
	pkgs, err := st.ThirdPartyPackages(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	var d *tpinstall.Decision
	for _, x := range tpinstall.Plan(inv.Software, manifest, pkgs, arch) {
		if x.App.ID == *app {
			d = &x
		}
	}
	if d == nil {
		fmt.Fprintf(stderr, "patchd-server: o programa %q não está instalado na máquina %s (ou a regra não tem \"install\")\n", *app, short(id))
		return exitRuntime
	}
	if d.State != tpinstall.StateUpdate {
		fmt.Fprintf(stderr, "patchd-server: nada a instalar: %s: %s\n", stateLabels[d.State], d.Reason)
		return exitRuntime
	}
	// A aprovação manda (Aula 14.5): a versão do instalador escolhido tem de estar aprovada.
	approvals, err := st.ThirdPartyApprovals(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	if _, ok := approvalIndex(approvals)[store.TPApprovalKey(d.App.ID, d.Package.Version)]; !ok {
		fmt.Fprintf(stderr, "patchd-server: %s %s ainda não foi aprovado: package approve -app %s -version %s\n",
			d.App.ID, d.Package.Version, d.App.ID, d.Package.Version)
		return exitRuntime
	}
	// O arquivo tem de estar no cache, e conferir: senão o job só falharia na máquina (404).
	if _, err := pkgcache.Check(dir, d.Package.SHA256, d.Package.MSI()); err != nil {
		fmt.Fprintf(stderr, "patchd-server: o instalador de %s %s não está no cache ou não confere (%v): package fetch -app %s\n",
			d.App.ID, d.Package.Version, err, d.App.ID)
		return exitRuntime
	}
	p, err := tpinstall.JobParams(*d)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	raw, _ := json.Marshal(p)
	rec, err := jobs.Issue(ctx, st, key, jobs.Request{MachineID: id, Type: jobs.TypeThirdParty, TTL: *ttl, By: "linha de comando", Params: raw}, now)
	if errors.Is(err, jobs.ErrMachineUnavailable) {
		fmt.Fprintf(stderr, "patchd-server: a máquina %s foi aposentada agora há pouco; nada foi criado\n", short(id))
		return exitRuntime
	}
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: criar job: %v\n", err)
		return exitRuntime
	}
	signer := "não exigida (o agente informa)"
	if p.Signer != "" {
		signer = "exigida: " + p.Signer
	}
	fmt.Fprintf(stdout, "job %d criado para a máquina %s: %s %s → %s · %s · %s\n  arquivo     %s (%s bytes)\n  argumentos  %v\n  assinatura  %s\n  prazo       %s\n",
		rec.ID, short(id), d.App.Name, d.Installed, p.Version, p.Installer, d.Package.Architecture, d.Package.SHA256[:12]+"…",
		strconv.FormatInt(p.Size, 10), p.Args, signer, rec.NotAfter.Local().Format("02/01/2006 15:04"))
	return exitOK
}
