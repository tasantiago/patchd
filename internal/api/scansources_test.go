package api_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

// Aula 16.2: a fonte de busca que falha aparece na máquina (em qualquer aba) e na visão
// geral, com o nome do HRESULT e a última resposta.
func TestFontesDeBuscaFalhando(t *testing.T) {
	h, st, _ := ambientePaginas(t)
	ctx := context.Background()
	ontem := time.Now().UTC().Add(-26 * time.Hour)
	_, _ = st.SaveInventory(ctx, "92fc3b93-0003", protocol.InventoryReport{SchemaVersion: 1, Hash: "x", OS: protocol.OSInfo{Hostname: `<script>alert(1)</script>`}})
	busca := func(em time.Time, wsus bool) {
		def := protocol.ScanResult{Source: "wua-default", Missing: []protocol.MissingUpdate{}}
		if !wsus {
			def = protocol.ScanResult{Source: "wua-default", Error: "busca no WUA: Search: HRESULT 0x80244007"}
		}
		_ = st.SaveScan(ctx, "92fc3b93-0003", protocol.PatchScanReport{SchemaVersion: protocol.PatchSchemaVersion, ScannedAt: em,
			Scans: []protocol.ScanResult{def, {Source: "wua-offline", Missing: []protocol.MissingUpdate{}}}})
	}
	busca(ontem, true)
	busca(time.Now().UTC(), false)

	aviso := "A busca no Windows Update da política (WSUS ou Microsoft) falha: última resposta em "
	erro := "HRESULT 0x80244007 (WU_E_PT_SOAPCLIENT_SOAPFAULT: o servidor (WSUS) recusou a sincronização"
	for _, aba := range []string{"", "?aba=pendencias", "?aba=tarefas"} {
		b := pagina(h, "GET", "/painel/maquinas/92fc3b93-0003"+aba, sessaoTeste, nil).Body.String()
		if !strings.Contains(b, `<p class="aviso erro" role="alert">`+aviso) || !strings.Contains(b, "(há 26 h)") || !strings.Contains(b, erro) {
			t.Errorf("máquina%s: faltou o aviso", aba)
		}
	}
	if b := pagina(h, "GET", "/painel/maquinas/60ef97e4-0001", sessaoTeste, nil).Body.String(); strings.Contains(b, aviso) {
		t.Error("a outra máquina não tem fonte falhando")
	}
	b := pagina(h, "GET", "/painel/patch?aba=geral", sessaoTeste, nil).Body.String()
	if !strings.Contains(b, `<li class="erro"><a href="/painel/maquinas/92fc3b93-0003">&lt;script&gt;alert(1)&lt;/script&gt;: `+aviso) || !strings.Contains(b, erro) {
		t.Error("visão geral: faltou a fonte falhando")
	}

	// Voltou a responder: o aviso some.
	busca(time.Now().UTC(), true)
	if b := pagina(h, "GET", "/painel/maquinas/92fc3b93-0003", sessaoTeste, nil).Body.String(); strings.Contains(b, aviso) {
		t.Error("voltou a responder, o aviso continua")
	}
}
