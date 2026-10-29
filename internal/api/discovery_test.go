package api_test

import (
	"context"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

// espera a varredura em segundo plano terminar (o dialer falso é instantâneo).
func esperaVarredura(t *testing.T, st *store.Memory) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if runs, _ := st.DiscoveryRuns(context.Background(), 1); len(runs) > 0 && runs[0].FinishedAt != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("a varredura não terminou")
}

// Aula 23.7: cadastrar faixas, varrer (dialer falso) e ver os hosts sem agente com o comando
// de instalação; o que casa com o inventário (por IP ou por nome) fica de fora. Dados fictícios.
func TestDescoberta(t *testing.T) {
	// Falso: .10 é Windows (445/3389), .20 Windows, .30 SSH; .50 também Windows mas é o IP de
	// uma máquina do inventário; o resto (.40) morto.
	dial := func(_ context.Context, ip netip.Addr, port int) bool {
		switch ip.As4()[3] {
		case 10, 20, 50:
			return port == 445 || port == 3389
		case 30:
			return port == 22
		}
		return false
	}
	// Reverse-DNS falso: .20 resolve para um nome que existe no inventário (casa por nome);
	// os outros não resolvem.
	lookup := func(_ context.Context, ip string) string {
		if strings.HasSuffix(ip, ".20") {
			return "nb-exemplo.tjro.jus.br"
		}
		return ""
	}
	h, st, log := ambientePaginas(t, api.WithNetDial(dial), api.WithNetLookup(lookup),
		api.WithPublicURL("https://patchd.exemplo"))
	ctx := context.Background()

	// Inventário: uma máquina com o IP 192.0.2.50 (casa por IP) e outra com o hostname
	// nb-exemplo (casa por nome com o reverse-DNS do .20).
	mustInv := func(id, host, ip string) {
		if _, err := st.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: id, CollectedAt: time.Now().UTC(),
			OS: protocol.OSInfo{Family: "windows", Hostname: host}}); err != nil {
			t.Fatal(err)
		}
		if ip != "" {
			if err := st.SaveHostState(ctx, id, &protocol.MachineState{CollectedAt: time.Now().UTC(),
				Interfaces: []protocol.NetInterface{{Name: "eth0", Up: true, IPv4: []string{ip + "/24"}}}}, "198.51.100.9"); err != nil {
				t.Fatal(err)
			}
		}
	}
	mustInv("maq-50", "pc-com-agente", "192.0.2.50")
	mustInv("maq-nb", "nb-exemplo", "")

	// Só admin.
	if r := pagina(h, "POST", "/painel/descoberta/faixas", sessaoTeste, url.Values{"faixa": {"192.0.2.0/24"}}); r.Code != http.StatusForbidden {
		t.Fatalf("leitura cadastra faixa: %d", r.Code)
	}
	// Faixa inválida é recusada.
	if l := local(pagina(h, "POST", "/painel/descoberta/faixas", sessaoAdmin, url.Values{"faixa": {"10.0.0.0/8"}})); !strings.Contains(l, "erro=faixa") {
		t.Errorf("faixa grande: %s", l)
	}
	// Cadastra duas; uma fica desligada e não é varrida.
	if l := local(pagina(h, "POST", "/painel/descoberta/faixas", sessaoAdmin, url.Values{"faixa": {"192.0.2.0/24"}, "nota": {"prédio-exemplo"}})); !strings.Contains(l, "faixa=") {
		t.Fatalf("cadastro: %s", l)
	}
	pagina(h, "POST", "/painel/descoberta/faixas", sessaoAdmin, url.Values{"faixa": {"203.0.113.0/24"}})
	// Repetida é recusada.
	if l := local(pagina(h, "POST", "/painel/descoberta/faixas", sessaoAdmin, url.Values{"faixa": {"192.0.2.0/24"}})); !strings.Contains(l, "erro=repetida") {
		t.Errorf("repetida: %s", l)
	}
	rngs, _ := st.DiscoveryRanges(ctx)
	if len(rngs) != 2 {
		t.Fatalf("faixas: %+v", rngs)
	}
	// Desliga a 203.0.113.0/24 (lá o .50 morreria de toda forma, mas garante que desligada não varre).
	var id203 int64
	for _, d := range rngs {
		if d.Spec == "203.0.113.0/24" {
			id203 = d.ID
		}
	}
	pagina(h, "POST", "/painel/descoberta/faixas/"+itoa(id203)+"/desligar", sessaoAdmin, url.Values{})

	// Varre. Em segundo plano; espera terminar.
	if l := local(pagina(h, "POST", "/painel/descoberta/varrer", sessaoAdmin, url.Values{})); !strings.Contains(l, "iniciada=1") {
		t.Fatalf("varrer: %s", l)
	}
	esperaVarredura(t, st)

	// Quatro vivos (.10, .20, .30, .50); dois casam (.50 por IP, .20 por nome); dois sem agente.
	runs, _ := st.DiscoveryRuns(ctx, 1)
	if runs[0].Alive != 4 || runs[0].WithoutAgent != 2 || runs[0].Scanned != 256 || runs[0].Partial {
		t.Fatalf("varredura: %+v", runs[0])
	}
	hosts, _ := st.DiscoveryHosts(ctx)
	byIP := map[string]store.DiscoveryHost{}
	for _, hh := range hosts {
		byIP[hh.IP] = hh
	}
	if byIP["192.0.2.50"].MatchKind != "ip" || byIP["192.0.2.20"].MatchKind != "nome" {
		t.Errorf("casamento: %+v", hosts)
	}
	if byIP["192.0.2.10"].MatchID != "" || byIP["192.0.2.30"].OSGuess != "ssh" {
		t.Errorf("sem agente: %+v", hosts)
	}

	// A página mostra os dois sem agente, com o comando (IP e servidor), e esconde os casados.
	b := pagina(h, "GET", "/painel/descoberta", sessaoAdmin, nil).Body.String()
	for _, want := range []string{"<code>192.0.2.10</code>", "<code>192.0.2.30</code>", "PowerShell remoto (WinRM)", "SSH",
		"https://patchd.exemplo", "2 com agente de 4 vivos"} {
		if !strings.Contains(b, want) {
			t.Errorf("página: faltou %q", want)
		}
	}
	if strings.Contains(b, "192.0.2.50") || strings.Contains(b, "192.0.2.20") {
		t.Error("um host com agente apareceu na lista de sem agente")
	}
	// O .30 (SSH) tem o comando de SSH; o .10 (Windows) não traz binário Linux.
	if !strings.Contains(b, "patchd-agent-linux-amd64") || !strings.Contains(b, "DOMINIO\\SEU_USUARIO") {
		t.Error("comandos de instalação incompletos")
	}

	// Remover uma faixa.
	if l := local(pagina(h, "POST", "/painel/descoberta/faixas/"+itoa(id203)+"/remover", sessaoAdmin, url.Values{})); !strings.Contains(l, "removida=1") {
		t.Errorf("remover: %s", l)
	}
	if rngs, _ := st.DiscoveryRanges(ctx); len(rngs) != 1 {
		t.Errorf("depois de remover: %+v", rngs)
	}

	// Auditoria: faixas e a varredura.
	ev, _ := st.AuditEvents(ctx, store.AuditFilter{Action: "descoberta."})
	var temVarrer, temFaixa bool
	for _, e := range ev {
		switch e.Action {
		case "descoberta.varrer":
			temVarrer = true
		case "descoberta.faixa":
			temFaixa = true
		}
	}
	if !temVarrer || !temFaixa {
		t.Errorf("auditoria: %+v", ev)
	}
	_ = log
}
