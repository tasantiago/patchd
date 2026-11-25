package userapp

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/localapi"
	"github.com/tasantiago/patchd/internal/protocol"
)

func TestAtualizacoesDoWindowsNaCentral(t *testing.T) {
	scan := time.Date(2026, 10, 17, 8, 0, 0, 0, time.UTC)
	loja := lojaDeTeste(0)
	loja.Catalog.Windows = &protocol.SelfServiceWindowsState{ScanAt: &scan, Waiting: 1, Updates: []protocol.SelfServiceWinUpdate{
		{KB: "5127648", Title: "Atualização cumulativa de 2026-10", Exploited: true, Restart: true}, {KB: "5130001"}}}
	c := &canal{loja: loja, pedido: localapi.Response{OK: true, Result: &protocol.SelfServiceResponse{Status: "criado", JobID: 77,
		Message: "Pedido aceito: 2 atualização(ões) do Windows."}}}
	a := appDeTeste(c)
	b := get(a, a.base()).Body.String()
	for _, want := range []string{"<h2>Windows</h2>", "Instalar atualizações do Windows (2)", `name="action" value="windows"`, "Atualização cumulativa de 2026-10",
		"KB5127648 · pede reinício", "corrige falha já explorada", "Atualização KB5130001", "não reinicia sozinho", "use a aba Reinício"} {
		if !strings.Contains(b, want) {
			t.Errorf("faltou %q", want)
		}
	}
	// O número do menu conta as do Windows (1 programa + 2 do Windows).
	if !strings.Contains(b, `<span class="cont" title="atualizações disponíveis">3</span>`) {
		t.Error("contador do menu")
	}
	r := post(a, a.base()+"pedir", url.Values{"action": {"windows"}}, "http://127.0.0.1:5555")
	if r.Code != 303 || len(c.pedidos) != 1 || c.pedidos[0].Action != protocol.SelfServiceWindows || c.pedidos[0].App != "" {
		t.Fatalf("pedido: %d %+v", r.Code, c.pedidos)
	}
	// Em andamento: sem botão, com a página se atualizando.
	c.loja.Catalog.Windows.JobID = 77
	w := get(a, a.base())
	if b := w.Body.String(); strings.Contains(b, "Instalar atualizações do Windows") || !strings.Contains(b, "estão sendo instaladas") || w.Header().Get("Refresh") == "" {
		t.Error("em andamento")
	}
	// Só esperando a TI, e em dia.
	c.loja.Catalog.Windows = &protocol.SelfServiceWindowsState{Waiting: 3, Updates: []protocol.SelfServiceWinUpdate{}}
	if b := get(a, a.base()).Body.String(); !strings.Contains(b, "3 atualização(ões) do Windows aguardam a liberação") {
		t.Error("esperando a TI")
	}
	c.loja.Catalog.Windows = &protocol.SelfServiceWindowsState{Updates: []protocol.SelfServiceWinUpdate{}, ScanAt: &scan}
	if b := get(a, a.base()).Body.String(); !strings.Contains(b, "O Windows está em dia (busca de 17/10") {
		t.Error("em dia")
	}
	// Sem a seção (servidor anterior ou desligado): nada do Windows.
	c.loja.Catalog.Windows = nil
	if b := get(a, a.base()).Body.String(); strings.Contains(b, "<h2>Windows</h2>") {
		t.Error("sem a seção")
	}
	// O aviso do ícone.
	if ti, tx := noticeText(Notice{Updates: []string{"Atualizações do Windows (2)"}}); ti != "Atualizações do Windows" || !strings.Contains(tx, "liberadas pela TI") {
		t.Errorf("aviso: %s / %s", ti, tx)
	}
}
