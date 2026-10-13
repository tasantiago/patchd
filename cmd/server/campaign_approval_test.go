package main

import (
	"context"
	"errors"
	"testing"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

// A campanha só leva o que está aprovado (Aula 13.2).
func TestCampanhaSoComAprovados(t *testing.T) {
	detail := func(_ context.Context, id string) (protocol.ComplianceDetail, bool, error) {
		if id != "m-1" {
			return protocol.ComplianceDetail{}, false, nil
		}
		return protocol.ComplianceDetail{Items: []protocol.PendingItem{
			{ID: "rsync", FixedIn: "3.4.1+ds1-7ubuntu0.3", Packages: []string{"rsync"}},
			{ID: "curl", FixedIn: "8.18.0-1ubuntu2.11", Packages: []string{"curl"}},
		}}, true, nil
	}
	aprovados := []store.PatchApproval{{Item: "rsync", FixedIn: "3.4.1+ds1-7ubuntu0.3"}}
	approvals := func(context.Context) ([]store.PatchApproval, error) { return aprovados, nil }
	ctx := context.Background()

	items, found, err := approvedPending(ctx, detail, approvals, "m-1")
	if err != nil || !found || len(items) != 1 || items[0].ID != "rsync" {
		t.Fatalf("só o rsync: %+v %t %v", items, found, err)
	}
	// Revogada a aprovação, o anel seguinte já não leva nada para essa máquina.
	aprovados = nil
	if items, found, _ := approvedPending(ctx, detail, approvals, "m-1"); !found || len(items) != 0 {
		t.Errorf("sem aprovação: %+v", items)
	}
	if _, found, _ := approvedPending(ctx, detail, approvals, "outra"); found {
		t.Error("máquina desconhecida")
	}
	falha := func(context.Context) ([]store.PatchApproval, error) { return nil, errors.New("banco fora") }
	if _, _, err := approvedPending(ctx, detail, falha, "m-1"); err == nil {
		t.Error("sem conseguir ler as aprovações, nada é liberado")
	}
}
