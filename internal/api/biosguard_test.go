package api_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/biospw"
	"github.com/tasantiago/patchd/internal/machinebox"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

var senhaNaPagina = regexp.MustCompile(`<code class="senha-bios">([a-z0-9 ]+)</code>`)

// TestGuardaDaSenhaDaBIOS (Aula 21.4): sortear e guardar (pendente), uma por máquina, só
// admin; revelar só admin, com motivo, auditoria antes de mostrar e aviso por e-mail; nada
// da senha na trilha, no log ou no e-mail.
func TestGuardaDaSenhaDaBIOS(t *testing.T) {
	ctx := context.Background()
	const maq = "/painel/maquinas/60ef97e4-0001"
	hw := maq + "?aba=hardware"

	// Sem a chave: o cartão avisa e nada é sorteado.
	h0, _, _ := ambientePaginas(t)
	if b := pagina(h0, "GET", hw, sessaoAdmin, nil).Body.String(); !strings.Contains(b, "Guarda desligada") || strings.Contains(b, "/bios/preparar") {
		t.Error("sem chave: o aviso, sem o botão")
	}
	if r := pagina(h0, "POST", maq+"/bios/preparar", sessaoAdmin, nil); local(r) != hw+"&bios-erro=sem-chave" {
		t.Errorf("sem chave: %s", local(r))
	}

	priv, _ := machinebox.Generate()
	pub, _ := machinebox.Public(priv)
	kid := machinebox.KeyID(pub)
	correio := &carteiro{}
	h, st, log := ambientePaginas(t, api.WithBIOSEscrow(priv), api.WithNotifier(correio))

	if b := pagina(h, "GET", hw, sessaoTeste, nil).Body.String(); !strings.Contains(b, "Nenhuma senha guardada") || strings.Contains(b, "/bios/preparar") {
		t.Error("leitura: o cartão, sem o botão")
	}
	if r := pagina(h, "POST", maq+"/bios/preparar", sessaoTeste, nil); r.Code != http.StatusForbidden {
		t.Errorf("leitura prepara: %d", r.Code)
	}
	b := pagina(h, "GET", hw, sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, "chave de guarda <code>"+kid+"</code>") || !strings.Contains(b, `action="/painel/maquinas/60ef97e4-0001/bios/preparar"`) {
		t.Fatalf("admin: a chave e o botão:\n%s", b)
	}
	r := pagina(h, "POST", maq+"/bios/preparar", sessaoAdmin, nil)
	if r.Code != http.StatusSeeOther || local(r) != hw+"&bios=preparada" {
		t.Fatalf("preparar: %d %s", r.Code, local(r))
	}
	if r := pagina(h, "POST", maq+"/bios/preparar", sessaoAdmin, nil); local(r) != hw+"&bios=existente" {
		t.Errorf("segunda: %s", local(r))
	}
	l, _ := st.BIOSPasswords(ctx, "60ef97e4-0001")
	if len(l) != 1 || l[0].State != store.BIOSPending || l[0].EscrowKeyID != kid || l[0].CreatedBy != "thyago" {
		t.Fatalf("guarda: %+v", l)
	}
	senha, err := biospw.Open(priv, "60ef97e4-0001", l[0].Escrow)
	if err != nil {
		t.Fatal(err)
	}
	b = pagina(h, "GET", hw+"&bios=preparada", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, "Senha da BIOS sorteada e guardada (pendente)") || strings.Contains(b, "/bios/preparar") ||
		!strings.Contains(b, "A senha #1 está pendente") || !strings.Contains(b, `action="/painel/maquinas/60ef97e4-0001/bios/1/revelar"`) ||
		strings.Contains(b, senha) {
		t.Errorf("depois de preparar:\n%s", b)
	}
	if b := pagina(h, "GET", hw, sessaoTeste, nil).Body.String(); strings.Contains(b, "/revelar") || !strings.Contains(b, "pendente: guardada") {
		t.Error("leitura vê o estado, sem o Revelar")
	}

	// Revelar: leitura não; motivo curto não; máquina errada não.
	motivo := url.Values{"motivo": {"chamado 4321: técnico na bancada"}}
	if r := pagina(h, "POST", maq+"/bios/1/revelar", sessaoTeste, motivo); r.Code != http.StatusForbidden {
		t.Errorf("leitura revela: %d", r.Code)
	}
	if r := pagina(h, "POST", maq+"/bios/1/revelar", sessaoAdmin, url.Values{"motivo": {"  curto   "}}); local(r) != hw+"&bios-erro=motivo" {
		t.Errorf("motivo curto: %s", local(r))
	}
	if r := pagina(h, "POST", "/painel/maquinas/7aa604f7-0002/bios/1/revelar", sessaoAdmin, motivo); r.Code != http.StatusNotFound {
		t.Errorf("guarda de outra máquina: %d", r.Code)
	}
	if ev, _ := st.AuditEvents(ctx, store.AuditFilter{Action: "bios.senha.revelar"}); len(ev) != 0 {
		t.Fatalf("revelação recusada na trilha: %+v", ev)
	}

	r = pagina(h, "POST", maq+"/bios/1/revelar", sessaoAdmin, motivo)
	m := senhaNaPagina.FindStringSubmatch(r.Body.String())
	if r.Code != http.StatusOK || m == nil || strings.ReplaceAll(m[1], " ", "") != senha || m[1] != biospw.Grouped(senha) {
		t.Fatalf("revelar: %d %v\n%s", r.Code, m, r.Body.String())
	}
	if r.Header().Get("Cache-Control") != "no-store" || !strings.Contains(r.Body.String(), "registrada na auditoria e avisada por e-mail") ||
		!strings.Contains(r.Body.String(), "Administrador") {
		t.Errorf("página: %q", r.Header().Get("Cache-Control"))
	}
	ev, _ := st.AuditEvents(ctx, store.AuditFilter{Action: "bios.senha."})
	if len(ev) != 2 || ev[0].Action != "bios.senha.revelar" || ev[0].Target != "60ef97e4-0001" ||
		ev[0].Detail != "guarda #1 (pendente); motivo: chamado 4321: técnico na bancada" || ev[1].Action != "bios.senha.preparar" {
		t.Errorf("trilha: %+v", ev)
	}
	for _, e := range ev {
		if strings.Contains(e.Detail, senha) {
			t.Error("a senha foi para a trilha")
		}
	}
	if strings.Contains(log.String(), senha) || !strings.Contains(log.String(), "senha da BIOS revelada") {
		t.Errorf("log:\n%s", log.String())
	}
	var msgs []string
	for range 100 {
		correio.mu.Lock()
		msgs = append([]string(nil), correio.msgs...)
		correio.mu.Unlock()
		if len(msgs) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(msgs) != 1 || !strings.Contains(msgs[0], "senha da BIOS revelada (vm)") || !strings.Contains(msgs[0], "Motivo: chamado 4321") ||
		strings.Contains(msgs[0], senha) {
		t.Errorf("e-mail: %q", msgs)
	}
	if b := pagina(h, "GET", hw, sessaoAdmin, nil).Body.String(); !strings.Contains(b, "1 (a última por thyago") {
		t.Error("a contagem de revelações")
	}

	// Envelope que não abre (trocado ou alterado no banco): nada aparece.
	_, _, _ = st.CreateBIOSPassword(ctx, store.BIOSPassword{MachineID: "7aa604f7-0002", Escrow: []byte("lixo"), EscrowKeyID: kid,
		CreatedAt: time.Now(), CreatedBy: "x"})
	if r := pagina(h, "POST", "/painel/maquinas/7aa604f7-0002/bios/2/revelar", sessaoAdmin, motivo); local(r) != "/painel/maquinas/7aa604f7-0002?aba=hardware&bios-erro=envelope" {
		t.Errorf("envelope ruim: %s", local(r))
	}
	// Outra chave de guarda no servidor: a senha antiga aparece como de outra chave e não abre.
	outra, _ := machinebox.Generate()
	agora := time.Now().UTC()
	src := &complianceFalso{fleet: protocol.ComplianceFleet{EvaluatedAt: agora, Machines: []protocol.ComplianceStatus{
		{MachineID: "60ef97e4-0001", Hostname: "vm-ubuntu", State: "em_dia", Reasons: []string{}, LastSeenAt: agora, EvaluatedAt: agora}}}}
	h2 := api.New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), api.WithCompliance(src), api.WithBIOSEscrow(outra))
	if b := pagina(h2, "GET", hw, sessaoAdmin, nil).Body.String(); !strings.Contains(b, "outra chave de guarda (<code>"+kid+"</code>)") {
		t.Error("outra chave: o aviso na linha")
	}
	if r := pagina(h2, "POST", maq+"/bios/1/revelar", sessaoAdmin, motivo); local(r) != hw+"&bios-erro=outra-chave" {
		t.Errorf("outra chave: %s", local(r))
	}
	// Máquina aposentada não ganha senha nova.
	if _, err := st.RetireMachine(ctx, "60ef97e4-0001", "teste", "thyago"); err != nil {
		t.Fatal(err)
	}
	if r := pagina(h, "POST", maq+"/bios/preparar", sessaoAdmin, nil); r.Code != http.StatusNotFound {
		t.Errorf("aposentada: %d", r.Code)
	}
}
