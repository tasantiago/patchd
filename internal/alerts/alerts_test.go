package alerts

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/campaign"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

type caixa struct {
	mu    sync.Mutex
	falha error
	msgs  []string // assunto + "\n" + corpo
}

func (c *caixa) Send(_ context.Context, subject, body string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.falha != nil {
		return c.falha
	}
	c.msgs = append(c.msgs, subject+"\n"+body)
	return nil
}

func (c *caixa) achar(prefix string) []string {
	var out []string
	for _, m := range c.msgs {
		if strings.HasPrefix(m, prefix) {
			out = append(out, m)
		}
	}
	return out
}

func TestParseKinds(t *testing.T) {
	if k, err := ParseKinds(""); err != nil || len(k) != 3 {
		t.Errorf("vazio: %v %v", k, err)
	}
	if k, err := ParseKinds(" Pedidos, fontes,pedidos "); err != nil || strings.Join(k, ",") != "pedidos,fontes" {
		t.Errorf("lista: %v %v", k, err)
	}
	if _, err := ParseKinds("pedidos,tudo"); err == nil {
		t.Error("tipo desconhecido aceito")
	}
}

// Aula 18.6: os três avisos, sem repetir; falha do SMTP volta na passada seguinte.
func TestPassadas(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	cx := &caixa{}
	agora := time.Now().UTC()
	w := &Watcher{Store: st, Mail: cx, PublicURL: "https://patchd.exemplo:8443/", Now: func() time.Time { return agora }}

	// Nada acontecendo: nada sai.
	if n, err := w.Step(ctx); n != 0 || err != nil || len(cx.msgs) != 0 {
		t.Fatalf("vazio: %d %v %v", n, err, cx.msgs)
	}

	// Dois pedidos esperando a TI; um já decidido não conta.
	for _, id := range []string{"7aa604f7-0002-4000-8000-000000000002", "60ef97e4-0001"} {
		_, _ = st.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: id, OS: protocol.OSInfo{Hostname: "estacao-" + id[:4]}})
	}
	pede := func(maq, conta string) int64 {
		id, err := st.CreateSelfServiceRequest(ctx, store.SelfServiceRequest{MachineID: maq, Account: conta, AppID: "7zip", Action: "instalar", Version: "24.08"})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	pede("7aa604f7-0002-4000-8000-000000000002", `EXEMPLO\fulano`)
	pede("60ef97e4-0001", `EXEMPLO\beltrano`)
	decidido := pede("60ef97e4-0001", `EXEMPLO\ciclano`)
	_ = st.DecideSelfServiceRequest(ctx, decidido, store.RequestRefused, "thyago", "", 0, agora)

	// Campanhas: uma que o servidor pausou, uma pausada pelo admin e uma em andamento.
	pausa := func(nome, detalhe string) {
		id, _ := st.CreateCampaign(ctx, campaign.Campaign{Name: nome, Type: "thirdparty", App: "7zip", Version: "24.08", Rings: []int{0, 1}})
		c, _, _ := st.Campaign(ctx, id)
		c.State, c.Detail = campaign.StatePaused, detalhe
		if ok, err := st.SaveCampaign(ctx, c, campaign.StateRunning); !ok || err != nil {
			t.Fatal(ok, err)
		}
	}
	pausa("7-Zip outubro", "anel 0: 1 job(s) sem sucesso (estacao-60ef); retome (campaign resume) para aceitar e seguir")
	pausa("manual", "pausada por thyago")
	_, _ = st.CreateCampaign(ctx, campaign.Campaign{Name: "andando", Type: "update", Sources: []string{"curl"}, Rings: []int{0}})

	// Fontes: a da política falhando (erro), o catálogo offline falhando (só atenção) e uma boa.
	falha := protocol.PatchScanReport{SchemaVersion: protocol.PatchSchemaVersion, ScannedAt: agora, Scans: []protocol.ScanResult{
		{Source: "wua-default", Error: "0x80244007\nSOAP"}, {Source: "wua-offline", Error: "sem catálogo"}}}
	_ = st.SaveScan(ctx, "60ef97e4-0001", falha)
	_ = st.SaveScan(ctx, "7aa604f7-0002-4000-8000-000000000002", protocol.PatchScanReport{SchemaVersion: protocol.PatchSchemaVersion, ScannedAt: agora,
		Scans: []protocol.ScanResult{{Source: "wua-default", Missing: []protocol.MissingUpdate{}}}})

	n, err := w.Step(ctx)
	if err != nil || n != 3 {
		t.Fatalf("primeira passada: %d %v\n%s", n, err, strings.Join(cx.msgs, "\n----\n"))
	}
	p := cx.achar("2 pedido(s) aguardando a TI\n")
	if len(p) != 1 || !strings.Contains(p[0], `instalar 7zip (24.08) · EXEMPLO\fulano · máquina 7aa604f7`) || strings.Contains(p[0], "ciclano") ||
		!strings.Contains(p[0], "https://patchd.exemplo:8443/painel/autoatendimento") {
		t.Errorf("pedidos:\n%s", p)
	}
	c := cx.achar("Campanha pausada: ")
	if len(c) != 1 || !strings.HasPrefix(c[0], "Campanha pausada: 7-Zip outubro\n") || !strings.Contains(c[0], "1 job(s) sem sucesso") || !strings.Contains(c[0], "/painel/campanhas/1") {
		t.Errorf("campanhas:\n%s", c)
	}
	f := cx.achar("1 máquina(s) com a busca falhando\n")
	if len(f) != 1 || !strings.Contains(f[0], "estacao-60ef · wua-default · nenhuma resposta guardada · 0x80244007 SOAP") || strings.Contains(f[0], "wua-offline") ||
		!strings.Contains(f[0], "/painel/patch?aba=geral") {
		t.Errorf("fontes:\n%s", f)
	}

	// Segunda passada, nada novo: nada sai.
	if n, err := w.Step(ctx); n != 0 || err != nil || len(cx.msgs) != 3 {
		t.Errorf("repetiu: %d %v %d", n, err, len(cx.msgs))
	}

	// Um pedido novo: o e-mail fala só dele, com o total da fila.
	pede("60ef97e4-0001", `EXEMPLO\novo`)
	// A fonte voltou e falhou de novo: aviso novo (a última resposta mudou).
	ok := protocol.PatchScanReport{SchemaVersion: protocol.PatchSchemaVersion, ScannedAt: agora.Add(time.Minute), Scans: []protocol.ScanResult{{Source: "wua-default", Missing: []protocol.MissingUpdate{}}}}
	_ = st.SaveScan(ctx, "60ef97e4-0001", ok)
	falha.ScannedAt = agora.Add(2 * time.Minute)
	_ = st.SaveScan(ctx, "60ef97e4-0001", falha)

	// SMTP fora: nada é marcado, e a passada devolve o erro.
	cx.falha = errors.New("conectar ao SMTP: recusada")
	if n, err := w.Step(ctx); n != 0 || err == nil || !strings.Contains(err.Error(), "recusada") {
		t.Fatalf("smtp fora: %d %v", n, err)
	}
	cx.falha = nil
	if n, err := w.Step(ctx); n != 2 || err != nil {
		t.Fatalf("depois do smtp voltar: %d %v", n, err)
	}
	p = cx.achar("1 pedido(s) aguardando a TI\n")
	if len(p) != 1 || !strings.Contains(p[0], `EXEMPLO\novo`) || strings.Contains(p[0], "fulano") || !strings.Contains(p[0], "(3 no total na fila)") {
		t.Errorf("pedido novo:\n%s", p)
	}
	if f := cx.achar("1 máquina(s) com a busca falhando\n"); len(f) != 2 || !strings.Contains(f[1], "última resposta em") {
		t.Errorf("fonte de novo:\n%s", f)
	}

	// Só os tipos pedidos; sem URL pública, sem link.
	cx2 := &caixa{}
	w2 := &Watcher{Store: store.NewMemory(), Mail: cx2, Kinds: []string{KindSources}, Now: w.Now}
	_ = w2.Store.(*store.Memory).SaveScan(ctx, "x", falha)
	_, _ = w2.Store.(*store.Memory).CreateSelfServiceRequest(ctx, store.SelfServiceRequest{MachineID: "x", Account: "a", AppID: "7zip", Action: "instalar"})
	if n, _ := w2.Step(ctx); n != 1 || len(cx2.achar("1 máquina")) != 1 || strings.Contains(cx2.msgs[0], "https://") {
		t.Errorf("só fontes: %d %v", n, cx2.msgs)
	}

	// A pausa antiga (mais de 7 dias) não avisa.
	w3 := &Watcher{Store: st, Mail: &caixa{}, Kinds: []string{KindCampaigns}, Now: func() time.Time { return agora.Add(8 * 24 * time.Hour) }}
	pausa("outra", "anel 0 sem janela de manutenção (ring window); defina e retome")
	if n, _ := w3.Step(ctx); n != 0 {
		t.Errorf("pausa de 8 dias atrás avisou")
	}
}

// A prévia não manda nem marca.
func TestPrevia(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	_, _ = st.CreateSelfServiceRequest(ctx, store.SelfServiceRequest{MachineID: "x", Account: "a", AppID: "7zip", Action: "instalar"})
	cx := &caixa{}
	w := &Watcher{Store: st, Mail: cx, Now: time.Now}
	for range 2 {
		if l, err := w.Preview(ctx); err != nil || len(l) != 1 || l[0].Kind != KindRequests || l[0].Subject != "1 pedido(s) aguardando a TI" {
			t.Fatalf("prévia: %+v %v", l, err)
		}
	}
	if len(cx.msgs) != 0 {
		t.Error("a prévia mandou")
	}
}
