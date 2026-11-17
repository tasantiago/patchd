package agent

import (
	"context"
	"io"
	"sync/atomic"
	"testing"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/thirdparty"
)

// O check-in entrega a lista de atalhos da área de trabalho pública ao agente; sem a lista
// (servidor sem as regras de terceiros), o agente não mexe nos atalhos.
func TestCheckinEntregaAtalhos(t *testing.T) {
	base, _ := thirdparty.Default()
	regra, err := thirdparty.BuildCustom(thirdparty.CustomInput{Name: "IRPF 2026", InventoryName: "IRPF2026", InstallType: "exe",
		UnsignedReason: "teste", Upload: true, ShortcutTarget: `C:\IRPF\irpf.exe`})
	if err != nil {
		t.Fatal(err)
	}
	base.Apps = append(base.Apps, regra)
	srv, _, cred := novoServidor(t, api.WithThirdPartyManifest(thirdparty.NewLive(base)))
	var buscas atomic.Int32
	a := agenteDeTeste(t, srv, cred, &buscas, io.Discard)
	var got []protocol.DesktopShortcuts
	a.DesktopShortcuts = func(_ context.Context, l protocol.DesktopShortcuts) { got = append(got, l) }
	if out := a.CheckIn(context.Background()); out != outcomeOK {
		t.Fatalf("ciclo: %v", out)
	}
	if len(got) != 1 || len(got[0].Items) != 1 || got[0].Items[0].App != "irpf-2026" || got[0].Items[0].Name != "IRPF 2026" {
		t.Fatalf("atalhos: %+v", got)
	}

	sem, _, cred2 := novoServidor(t)
	b := agenteDeTeste(t, sem, cred2, &buscas, io.Discard)
	called := false
	b.DesktopShortcuts = func(context.Context, protocol.DesktopShortcuts) { called = true }
	if out := b.CheckIn(context.Background()); out != outcomeOK || called {
		t.Errorf("sem lista: %v %v", out, called)
	}
}
