package api_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

// Endereços de documentação (RFC 5737 e 3849): nenhum é de rede real.
func estadoTeste() *protocol.MachineState {
	return &protocol.MachineState{CollectedAt: time.Now().UTC(),
		Interfaces: []protocol.NetInterface{
			{Name: "eth0", MAC: "00:15:5d:01:02:03", IPv4: []string{"192.0.2.1/24"}, IPv6: []string{"2001:db8::1/64"}, Up: true},
			{Name: "docker0", MAC: "02:42:ac:11:00:01", IPv4: []string{"172.17.0.1/16"}, Up: true, LocalMAC: true},
		},
		Disks: []protocol.Disk{
			{Mount: "/", FS: "ext4", Device: "/dev/sda2", TotalBytes: 100 << 30, FreeBytes: 5 << 30},
			{Mount: "/srv", FS: "xfs", Device: "/dev/sdb1", TotalBytes: 2 << 40, FreeBytes: 1 << 40},
			{Mount: "D:", FS: "NTFS", Model: "Disco <Teste>", Bus: "SATA", TotalBytes: 1 << 40, FreeBytes: 1 << 39},
		}}
}

func TestCheckinGravaEstado(t *testing.T) {
	st := store.NewMemory()
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	if _, err := st.CreateEnrollmentToken(context.Background(), hash, "teste", time.Now().Add(time.Hour), 5); err != nil {
		t.Fatal(err)
	}
	log := &syncBuffer{}
	h := api.New(st, slog.New(slog.NewTextHandler(log, nil)))
	m := registra(t, h, tok)
	checkin := func(s *protocol.MachineState) int {
		corpo, _ := json.Marshal(protocol.CheckinRequest{AgentVersion: "v0.12.1", SentAt: time.Now().UTC(), State: s})
		return envia(h, "POST", "/api/v1/agent/checkin", m.Credential, corpo).Code
	}

	// O httptest conecta de 192.0.2.1: é o endereço de uma interface da máquina.
	if code := checkin(estadoTeste()); code != http.StatusOK {
		t.Fatalf("check-in com estado: %d", code)
	}
	hs, _, _ := st.HostState(context.Background(), m.MachineID)
	if hs.State == nil || len(hs.State.Disks) != 3 || hs.CheckinAddr != "192.0.2.1" {
		t.Fatalf("estado gravado: %+v", hs)
	}

	// Estado fora do formato: o check-in segue (200), o estado é descartado com aviso, e o
	// anterior fica.
	ruim := estadoTeste()
	ruim.Disks[0].FreeBytes = ruim.Disks[0].TotalBytes + 1
	ruim.Disks = ruim.Disks[:1]
	if code := checkin(ruim); code != http.StatusOK {
		t.Fatalf("estado inválido não pode derrubar o check-in: %d", code)
	}
	if hs, _, _ = st.HostState(context.Background(), m.MachineID); hs.State == nil || len(hs.State.Disks) != 3 {
		t.Errorf("estado inválido sobrescreveu o anterior: %+v", hs.State)
	}
	if !strings.Contains(log.String(), "estado da máquina descartado") || !strings.Contains(log.String(), "livre") {
		t.Errorf("log sem o motivo do descarte:\n%s", log)
	}
	// Agente anterior à 12.1: sem o campo, o check-in é o de sempre.
	if code := checkin(nil); code != http.StatusOK {
		t.Errorf("check-in sem estado: %d", code)
	}
}

func TestPaginaDaMaquinaRedeEDiscos(t *testing.T) {
	h, st, _ := ambientePaginas(t)
	b := pagina(h, "GET", "/painel/maquinas/60ef97e4-0001?aba=hardware", sessaoTeste, nil).Body.String()
	if !strings.Contains(b, "<h2>Rede e discos</h2>") || !strings.Contains(b, "ainda não manda rede e discos") {
		t.Errorf("sem estado: a página diz que falta atualizar o agente")
	}

	// Visto de um endereço que não é da máquina: NAT no caminho.
	if err := st.SaveHostState(context.Background(), "60ef97e4-0001", estadoTeste(), "203.0.113.9"); err != nil {
		t.Fatal(err)
	}
	b = pagina(h, "GET", "/painel/maquinas/60ef97e4-0001?aba=hardware", sessaoTeste, nil).Body.String()
	for _, tr := range []string{
		"<dt>IP principal</dt><dd>192.0.2.1</dd>",
		"203.0.113.9 <span class=\"muted\">· não é de nenhuma interface da máquina",
		"<code>/</code> <span class=\"muted\">/dev/sda2</span>", "100,0 GiB", "5,0 GiB",
		`<span class="uso cheio" role="img" aria-label="95% ocupado"><span style="width:95%"></span></span> 95%`,
		`<span class="uso " role="img" aria-label="50% ocupado">`, "2,0 TiB",
		"<td>eth0</td><td>ativa</td>", "2001:db8::1/64",
		`<code>D:</code> <span class="muted">Disco &lt;Teste&gt; · SATA</span>`,
		`<code>02:42:ac:11:00:01</code> <span class="muted" title="MAC administrado localmente`,
	} {
		if !strings.Contains(b, tr) {
			t.Errorf("faltou %q", tr)
		}
	}

	// Visto do próprio endereço da máquina.
	_ = st.SaveHostState(context.Background(), "60ef97e4-0001", nil, "192.0.2.1")
	if b = pagina(h, "GET", "/painel/maquinas/60ef97e4-0001?aba=hardware", sessaoTeste, nil).Body.String(); !strings.Contains(b, "endereço de uma interface da máquina") {
		t.Error("check-in vindo de uma interface da máquina")
	}
}

// Aula 15.6: quem está conectado, no resumo da máquina e na lista de Equipamentos.
func TestUsuariosConectados(t *testing.T) {
	h, st, _ := ambientePaginas(t)
	ctx := context.Background()
	resumo := func() string {
		return pagina(h, "GET", "/painel/maquinas/60ef97e4-0001", sessaoTeste, nil).Body.String()
	}
	// Agente anterior à 15.6: a linha não aparece.
	if err := st.SaveHostState(ctx, "60ef97e4-0001", estadoTeste(), "192.0.2.1"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(resumo(), "Usuários conectados") {
		t.Error("sem a coleta, não há o que mostrar")
	}
	// Ninguém conectado.
	s := estadoTeste()
	s.Users = []protocol.LoggedUser{}
	_ = st.SaveHostState(ctx, "60ef97e4-0001", s, "192.0.2.1")
	if b := resumo(); !strings.Contains(b, "<dt>Usuários conectados</dt><dd>ninguém <span class=\"muted\">· coletado agora") {
		t.Errorf("ninguém:\n%s", b)
	}
	s.Users = []protocol.LoggedUser{
		{Account: `EXEMPLO\fulano`, DisplayName: "Fulano <de> Tal", Session: protocol.SessionConsole, Active: true},
		{Account: `EXEMPLO\suporte`, Session: protocol.SessionRDP},
	}
	_ = st.SaveHostState(ctx, "60ef97e4-0001", s, "192.0.2.1")
	b := resumo()
	for _, tr := range []string{
		`<li>Fulano &lt;de&gt; Tal <span class="muted"><code>EXEMPLO\fulano</code></span> <span class="muted">· console</span></li>`,
		`<li><code>EXEMPLO\suporte</code> <span class="muted">· Área de Trabalho Remota · desconectada</span></li>`,
	} {
		if !strings.Contains(b, tr) {
			t.Errorf("resumo: faltou %q", tr)
		}
	}

	// Na lista: a coluna, a busca pelo nome ou pela conta, e a dica com todos.
	l := pagina(h, "GET", "/painel/inventario?campo=usuario&q=fulano", sessaoTeste, nil).Body.String()
	for _, tr := range []string{"1–1 de 1", `<option value="usuario" selected>Usuário conectado</option>`,
		`<td title="Fulano &lt;de&gt; Tal (EXEMPLO\fulano, console); EXEMPLO\suporte (Área de Trabalho Remota, desconectada)">Fulano &lt;de&gt; Tal <span class="muted">+1</span></td>`,
		`<a href="/painel/inventario?campo=usuario&amp;ordem=usuario&amp;q=fulano">Usuário</a>`} {
		if !strings.Contains(l, tr) {
			t.Errorf("lista: faltou %q", tr)
		}
	}
	if l := pagina(h, "GET", "/painel/inventario?q=suporte", sessaoTeste, nil).Body.String(); !strings.Contains(l, "1–1 de 1") {
		t.Error("a busca em todos os campos inclui a conta")
	}
	csv := pagina(h, "GET", "/painel/inventario.csv", sessaoTeste, nil).Body.String()
	if !strings.Contains(csv, ";anel;usuarios") || !strings.Contains(csv, `Fulano <de> Tal (EXEMPLO\fulano, console); EXEMPLO\suporte (Área de Trabalho Remota, desconectada)`) {
		t.Errorf("CSV:\n%s", csv)
	}
}
