package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

// memoriaComPrefixo dá à memória o ResolveMachine do PostgreSQL (ID inteiro primeiro).
type memoriaComPrefixo struct{ *store.Memory }

func (m memoriaComPrefixo) ResolveMachine(ctx context.Context, prefix string) (string, bool, error) {
	ativas, _ := m.Machines(ctx)
	aposentadas, _ := m.RetiredMachines(ctx)
	var achou []string
	for _, id := range append(idsDe(ativas), idsAposentadas(aposentadas)...) {
		if id == prefix {
			return id, true, nil
		}
		if strings.HasPrefix(id, prefix) {
			achou = append(achou, id)
		}
	}
	switch len(achou) {
	case 0:
		return "", false, nil
	case 1:
		return achou[0], true, nil
	}
	return "", false, store.ErrMachineAmbiguous
}

func idsDe(l []protocol.MachineSummary) []string {
	var out []string
	for _, m := range l {
		out = append(out, m.ID)
	}
	return out
}

func idsAposentadas(l []store.RetiredMachine) []string {
	var out []string
	for _, m := range l {
		out = append(out, m.ID)
	}
	return out
}

func TestComandosDeMaquina(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	if _, err := mem.CreateEnrollmentToken(ctx, hash, "t", time.Now().Add(time.Hour), 5); err != nil {
		t.Fatal(err)
	}
	// A mesma VM registrada duas vezes (mesma instalação) e outra máquina sem inventário.
	for _, c := range []struct{ id, install string }{{"7aa604f7-0001", "guid-vm"}, {"60ef97e4-0002", "guid-vm"}, {"e2b82502-0003", "guid-outra"}} {
		_, ch := identity.NewSecret(identity.CredentialPrefix)
		if _, err := mem.Enroll(ctx, identity.Hash(tok), identity.NewMachine{ID: c.id, CredentialHash: ch, Keys: identity.Keys{InstallID: c.install},
			Evidence: protocol.IdentityEvidence{OSFamily: "linux"}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"7aa604f7-0001", "60ef97e4-0002"} {
		if _, err := mem.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: "h" + id,
			OS: protocol.OSInfo{Hostname: "vm-lab", Name: "Ubuntu", Version: "26.04"}}); err != nil {
			t.Fatal(err)
		}
	}
	st := memoriaComPrefixo{mem}
	roda := func(args ...string) (int, string, string) {
		var out, errOut bytes.Buffer
		code := machineCommand(ctx, st, args, time.Now().UTC(), &out, &errOut)
		return code, out.String(), errOut.String()
	}

	code, out, _ := roda("list")
	if code != exitOK || !strings.Contains(out, "mesma instalação: 60ef97e4") || !strings.Contains(out, "mesma instalação: 7aa604f7") ||
		!strings.Contains(out, "vm-lab") || !strings.Contains(out, "3 máquina(s)") {
		t.Fatalf("list:\n%s", out)
	}
	if _, out, _ := roda("list", "-sem-nome"); strings.Contains(out, "vm-lab") || strings.Contains(out, "NOME") {
		t.Errorf("-sem-nome:\n%s", out)
	}

	for _, c := range []struct {
		args []string
		code int
		msg  string
	}{
		{[]string{"retire", "-id", "7aa6"}, exitConfig, "-reason"},
		{[]string{"retire", "-reason", "registro antigo"}, exitConfig, "-id"},
		{[]string{"retire", "-id", "ffff", "-reason", "registro antigo"}, exitRuntime, "nenhuma máquina"},
		{[]string{"retire", "-id", "", "-reason", "registro antigo"}, exitConfig, "-id"},
		{[]string{"apagar"}, exitConfig, "desconhecido"},
	} {
		if code, _, e := roda(c.args...); code != c.code || !strings.Contains(e, c.msg) {
			t.Errorf("%v: %d %s", c.args, code, e)
		}
	}
	// Prefixo ambíguo: "" casa com todas; com dois caracteres em comum, também.
	mem2 := memoriaComPrefixo{store.NewMemory()}
	_, _ = mem2.SaveInventory(ctx, "abc1", protocol.InventoryReport{SchemaVersion: 1, Hash: "x"})
	_, _ = mem2.SaveInventory(ctx, "abc2", protocol.InventoryReport{SchemaVersion: 1, Hash: "y"})
	var e2 bytes.Buffer
	if code := machineCommand(ctx, mem2, []string{"retire", "-id", "abc", "-reason", "teste"}, time.Now(), &bytes.Buffer{}, &e2); code != exitConfig ||
		!strings.Contains(e2.String(), "mais de uma") {
		t.Errorf("ambíguo: %d %s", code, e2.String())
	}

	if code, _, e := roda("retire", "-id", "7aa6", "-reason", "registro antigo da VM reinstalada"); code != exitOK || !strings.Contains(e, "aposentada") {
		t.Fatalf("retire: %d %s", code, e)
	}
	if _, _, e := roda("retire", "-id", "7aa604f7-0001", "-reason", "de novo"); !strings.Contains(e, "já estava aposentada") {
		t.Errorf("retire de novo: %s", e)
	}
	_, out, _ = roda("list")
	if strings.Contains(out, "7aa604f7  ") || !strings.Contains(out, "mesma instalação: 7aa604f7 (aposentada)") || !strings.Contains(out, "2 máquina(s); 1 aposentada(s)") {
		t.Errorf("list depois do retire:\n%s", out)
	}
	_, out, _ = roda("list", "-retired")
	if !strings.Contains(out, "registro antigo da VM reinstalada") || !strings.Contains(out, "APOSENTADA") || !strings.Contains(out, "1 máquina(s)") {
		t.Errorf("list -retired:\n%s", out)
	}
	if code, _, e := roda("restore", "-id", "7aa6"); code != exitOK || !strings.Contains(e, "de volta à frota") {
		t.Errorf("restore: %d %s", code, e)
	}
	if _, _, e := roda("restore", "-id", "7aa6"); !strings.Contains(e, "não estava aposentada") {
		t.Errorf("restore de novo: %s", e)
	}
}
