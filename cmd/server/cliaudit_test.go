package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/store"
)

// Aula 18.1: o que muda pela linha de comando entra na trilha.
func TestAcaoDaLinhaDeComando(t *testing.T) {
	casos := map[string]string{
		"job create -machine 92fc -type rescan":    "cli.job.create",
		"job list":                                 "",
		"campaign pause -id 3":                     "cli.campaign.pause",
		"campaign show -id 3":                      "",
		"ring window -ring 1 -days seg-sex":        "cli.ring.window",
		"release publish v0.17.3":                  "cli.release.publish",
		"release current":                          "",
		"package approve -app 7zip -version 26.04": "cli.package.approve",
		"retention":                                "",
		"retention -apply":                         "cli.retention.apply",
		"retention --apply=true":                   "cli.retention.apply",
		"repo-cache prune -dry-run":                "",
		"repo-cache prune":                         "cli.repo-cache.prune",
		"audit -n 5":                               "",
		"selfservice request -app 7zip":            "", // já registra com a própria ação
		"":                                         "",
	}
	for linha, quer := range casos {
		if got := cliAction(strings.Fields(linha)); got != quer {
			t.Errorf("%q: %q, quer %q", linha, got, quer)
		}
	}
	d := cliDetail(strings.Fields("user create -name fulano -role admin"), exitOK)
	if d != "-name fulano -role admin → ok" {
		t.Errorf("detalhe: %q", d)
	}
	for _, linha := range []string{"token create -note x -secret abc", "job create -token=abc -key segredo"} {
		if d := cliDetail(strings.Fields(linha), exitRuntime); strings.Contains(d, "abc") || strings.Contains(d, "segredo") || !strings.HasSuffix(d, "falhou (código 1)") {
			t.Errorf("segredo na trilha: %q", d)
		}
	}
	if d := cliDetail([]string{"retention", "-apply"}, exitOK); d != "-apply → ok" {
		t.Errorf("retention: %q", d)
	}
	look := func(m map[string]string) config.Lookup {
		return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
	}
	if operator(look(map[string]string{"PATCHD_OPERATOR": " thyago "})) != "thyago" {
		t.Error("PATCHD_OPERATOR")
	}
	if op := operator(look(map[string]string{"PATCHD_OPERATOR": "a\nb"})); op == "a\nb" || op == "" {
		t.Errorf("operador com quebra de linha: %q", op)
	}
}

type trilhaFalsa struct {
	ev  []store.AuditEvent
	err error
}

func (f *trilhaFalsa) RecordAudit(_ context.Context, e store.AuditEvent) error {
	f.ev = append(f.ev, e)
	return f.err
}

func TestRunRegistraNaTrilha(t *testing.T) {
	tr := &trilhaFalsa{}
	old := cliAuditOpen
	t.Cleanup(func() { cliAuditOpen = old })
	cliAuditOpen = func(config.Lookup) (auditRecorder, func(), error) { return tr, func() {}, nil }
	look := func(k string) (string, bool) {
		if k == "PATCHD_OPERATOR" {
			return "thyago", true
		}
		return "", false
	}
	var out, errb bytes.Buffer
	chave := filepath.Join(t.TempDir(), "jobs.key")
	if code := run([]string{"job", "keygen", "-out", chave}, look, &out, &errb); code != exitOK {
		t.Fatalf("keygen: %d %s", code, errb.String())
	}
	if len(tr.ev) != 1 || tr.ev[0].Action != "cli.job.keygen" || tr.ev[0].Username != "thyago" || tr.ev[0].Source != "linha de comando" ||
		!strings.HasSuffix(tr.ev[0].Detail, "→ ok") {
		t.Fatalf("evento: %+v", tr.ev)
	}
	// Erro de uso não entra; consulta também não.
	run([]string{"job", "create"}, look, &out, &errb)
	run([]string{"job", "list"}, look, &out, &errb)
	if len(tr.ev) != 1 {
		t.Errorf("uso errado ou consulta na trilha: %+v", tr.ev)
	}
	// A trilha fora do ar: o comando vale, e o aviso aparece.
	tr.err = errors.New("banco fora")
	errb.Reset()
	if code := run([]string{"job", "keygen", "-out", chave + "2"}, look, &out, &errb); code != exitOK || !strings.Contains(errb.String(), "não foi registrado na auditoria") {
		t.Errorf("trilha fora: %d %s", code, errb.String())
	}
}

// Cada ação da linha de comando tem rótulo no painel (internal/api/audit.go).
func TestAcoesDaLinhaDeComandoTemRotulo(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "internal", "api", "audit.go"))
	if err != nil {
		t.Skip(err)
	}
	acoes := []string{"cli.retention.apply", "cli.repo-cache.prune"}
	for cmd, subs := range cliAudited {
		for _, s := range subs {
			acoes = append(acoes, "cli."+cmd+"."+s)
		}
	}
	for _, a := range acoes {
		if !strings.Contains(string(src), `{"`+a+`", "`) {
			t.Errorf("%s sem rótulo no painel", a)
		}
	}
}
