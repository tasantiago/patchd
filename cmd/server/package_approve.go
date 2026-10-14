package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
)

// approveStore: o que approve e revoke usam.
type approveStore interface {
	ThirdPartyPackages(ctx context.Context) ([]store.ThirdPartyPackage, error)
	ApproveThirdParty(ctx context.Context, a store.ThirdPartyApproval) (bool, error)
	RevokeThirdParty(ctx context.Context, app, version string) (bool, error)
}

const maxApprovalNote = 500

// packageApprove aprova um programa numa versão (Aula 14.5). Só uma versão que o servidor
// já guardou e conferiu (package fetch): aprova-se o que se pode instalar.
func packageApprove(ctx context.Context, st approveStore, manifest thirdparty.Manifest, args []string, by string, stdout, stderr io.Writer) int {
	app, ver, note, ok := approvalFlags("package approve", args, true, stderr)
	if !ok {
		return exitConfig
	}
	if !hasInstallRule(manifest, app) {
		fmt.Fprintf(stderr, "patchd-server: a regra %q do Windows não existe ou não tem \"install\"\n", app)
		return exitConfig
	}
	pkgs, err := st.ThirdPartyPackages(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	var stored []string
	for _, p := range pkgs {
		if p.AppID == app && p.Version == ver {
			stored = append(stored, p.Type+" "+p.Architecture)
		}
	}
	if len(stored) == 0 {
		fmt.Fprintf(stderr, "patchd-server: nenhum instalador de %s %s guardado: baixe e confira antes (package fetch -app %s)\n", app, ver, app)
		return exitRuntime
	}
	created, err := st.ApproveThirdParty(ctx, store.ThirdPartyApproval{AppID: app, Version: ver, ApprovedBy: by, Note: note})
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	if !created {
		fmt.Fprintf(stdout, "%s %s já estava aprovado; a aprovação original ficou\n", app, ver)
		return exitOK
	}
	fmt.Fprintf(stdout, "%s %s aprovado (%s): vale para os instaladores guardados dessa versão: %s\n", app, ver, by, strings.Join(stored, ", "))
	return exitOK
}

// packageRevoke revoga a aprovação. Jobs já criados seguem o prazo deles: quem quiser
// impedir cancela (job cancel) os que ainda não foram entregues.
func packageRevoke(ctx context.Context, st approveStore, args []string, stdout, stderr io.Writer) int {
	app, ver, _, ok := approvalFlags("package revoke", args, false, stderr)
	if !ok {
		return exitConfig
	}
	removed, err := st.RevokeThirdParty(ctx, app, ver)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	if !removed {
		fmt.Fprintf(stderr, "patchd-server: %s %s não estava aprovado\n", app, ver)
		return exitRuntime
	}
	fmt.Fprintf(stdout, "aprovação de %s %s revogada; jobs já criados seguem o prazo deles (job list, job cancel)\n", app, ver)
	return exitOK
}

func approvalFlags(name string, args []string, withNote bool, stderr io.Writer) (app, ver, note string, ok bool) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, packageUsage) }
	fs.StringVar(&app, "app", "", "")
	fs.StringVar(&ver, "version", "", "")
	if withNote {
		fs.StringVar(&note, "note", "", "")
	}
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return "", "", "", false
	}
	note = strings.TrimSpace(note)
	switch {
	case app == "" || ver == "":
		fmt.Fprintln(stderr, "patchd-server: informe -app e -version")
	case utf8.RuneCountInString(note) > maxApprovalNote:
		fmt.Fprintf(stderr, "patchd-server: -note tem no máximo %d caracteres\n", maxApprovalNote)
	default:
		return app, ver, note, true
	}
	return "", "", "", false
}

func hasInstallRule(m thirdparty.Manifest, app string) bool {
	for _, a := range m.For("windows") {
		if a.ID == app && a.Install != nil {
			return true
		}
	}
	return false
}

// approvalIndex: aprovações por programa e versão.
func approvalIndex(list []store.ThirdPartyApproval) map[string]store.ThirdPartyApproval {
	out := make(map[string]store.ThirdPartyApproval, len(list))
	for _, a := range list {
		out[store.TPApprovalKey(a.AppID, a.Version)] = a
	}
	return out
}
