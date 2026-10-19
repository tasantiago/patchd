package userapp

import (
	"context"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/localapi"
	"github.com/tasantiago/patchd/internal/protocol"
)

// maquina simula o agente e o servidor: uma instalação por vez, que termina depois de
// algumas consultas da loja.
type maquina struct {
	mu      sync.Mutex
	itens   []protocol.SelfServiceItem
	aberto  int64 // o job instalando (0: livre)
	app     string
	voltas  int // consultas até o job terminar
	duracao int
	seq     int64
	pedidos []string
	juntos  bool // dois jobs ao mesmo tempo (não pode)
	travado bool // o job nunca termina
}

func (m *maquina) call(_ context.Context, r localapi.Request) (localapi.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch r.Op {
	case localapi.OpRequest:
		m.pedidos = append(m.pedidos, r.App)
		for i := range m.itens {
			it := &m.itens[i]
			if it.App != r.App {
				continue
			}
			if it.Approval {
				m.seq++
				it.Pending = m.seq
				return localapi.Response{OK: true, Result: &protocol.SelfServiceResponse{Status: protocol.SelfServiceApproval, RequestID: m.seq, Message: "com a TI"}}, nil
			}
			if m.aberto > 0 {
				m.juntos = true
				return localapi.Response{OK: true, Result: &protocol.SelfServiceResponse{Status: protocol.SelfServiceWait, JobID: m.aberto}}, nil
			}
			m.seq++
			m.aberto, m.app, m.voltas = m.seq, r.App, m.duracao
			return localapi.Response{OK: true, Result: &protocol.SelfServiceResponse{Status: protocol.SelfServiceCreated, JobID: m.seq}}, nil
		}
		return localapi.Response{OK: true, Result: &protocol.SelfServiceResponse{Status: protocol.SelfServiceRefused, Message: "fora da loja"}}, nil
	default:
		if m.aberto > 0 && !m.travado {
			if m.voltas--; m.voltas < 0 {
				for i := range m.itens {
					if m.itens[i].App == m.app {
						m.itens[i].Action, m.itens[i].Installed = "em_dia", m.itens[i].Target
					}
				}
				m.aberto, m.app = 0, ""
			}
		}
		itens := append([]protocol.SelfServiceItem(nil), m.itens...)
		return localapi.Response{OK: true, Account: `EXEMPLO\teste`, Catalog: &protocol.SelfServiceCatalog{Items: itens, OpenJob: m.aberto}}, nil
	}
}

func esperaAte(t *testing.T, cond func() bool) {
	t.Helper()
	fim := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(fim) {
			t.Fatal("tempo esgotado")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Aula 19.1b: "Atualizar tudo" põe as atualizações diretas numa fila (uma de cada vez, como
// o servidor exige) e manda à TI na hora as que passam por ela.
func TestAtualizarTudo(t *testing.T) {
	m := &maquina{duracao: 2, itens: []protocol.SelfServiceItem{
		{App: "7zip", Name: "7-Zip", Installed: "26.01", Target: "26.04", Action: "atualizar"},
		{App: "vscode", Name: "Visual Studio Code", Installed: "1.104", Target: "1.105.1", Action: "atualizar"},
		{App: "libreoffice", Name: "LibreOffice", Installed: "26.2", Target: "26.8.1.1", Action: "atualizar", Approval: true},
		{App: "chrome", Name: "Google Chrome", Installed: "141", Action: "em_dia"},
	}}
	a := New(m.call)
	a.Addr, a.queueCheck = "127.0.0.1:5555", 2*time.Millisecond
	origem := "http://127.0.0.1:5555"

	if b := get(a, a.base()).Body.String(); !strings.Contains(b, `action="`+a.base()+`atualizar-tudo"><button type="submit">Atualizar tudo (3)</button>`) {
		t.Fatalf("faltou o botão:\n%s", b)
	}
	if w := post(a, a.base()+"atualizar-tudo", url.Values{}, "http://outro.exemplo"); w.Code != 403 {
		t.Errorf("de outra origem: %d", w.Code)
	}
	w := post(a, a.base()+"atualizar-tudo", url.Values{}, origem)
	if w.Code != 303 {
		t.Fatalf("atualizar tudo: %d", w.Code)
	}
	b := get(a, a.base()).Body.String()
	if !strings.Contains(b, "2 atualizações na fila") || !strings.Contains(b, "1 pedido foi para a aprovação da TI (LibreOffice)") {
		t.Errorf("aviso:\n%s", b)
	}
	esperaAte(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.aberto == 0 && len(m.pedidos) >= 3 && m.itens[1].Action == "em_dia"
	})
	esperaAte(t, func() bool { q := a.queueView(); return len(q.Waiting) == 0 && q.Current == "" })
	m.mu.Lock()
	if strings.Join(m.pedidos, ",") != "libreoffice,7zip,vscode" || m.juntos {
		t.Errorf("pedidos: %v (dois ao mesmo tempo: %v)", m.pedidos, m.juntos)
	}
	m.mu.Unlock()
	if b := get(a, a.base()).Body.String(); strings.Contains(b, "Atualizar tudo (") || strings.Contains(b, "Atualizando tudo") {
		t.Error("terminou: sem botão e sem fila (só a do LibreOffice, que está com a TI)")
	}
}

// Cancelar tira da fila o que não foi pedido; o que já está instalando continua. Com uma só
// atualização, não há botão.
func TestCancelarAFila(t *testing.T) {
	m := &maquina{travado: true, itens: []protocol.SelfServiceItem{
		{App: "7zip", Name: "7-Zip", Installed: "26.01", Target: "26.04", Action: "atualizar"},
		{App: "vscode", Name: "Visual Studio Code", Installed: "1.104", Target: "1.105.1", Action: "atualizar"},
		{App: "notepad", Name: "Notepad++", Installed: "8.9.1", Target: "8.9.2", Action: "atualizar"},
	}}
	a := New(m.call)
	a.Addr, a.queueCheck = "127.0.0.1:5555", 2*time.Millisecond
	origem := "http://127.0.0.1:5555"
	post(a, a.base()+"atualizar-tudo", url.Values{}, origem)
	esperaAte(t, func() bool { return a.queueView().Current == "7zip" })
	b := get(a, a.base()).Body.String()
	for _, tr := range []string{`<span class="selo andamento">Instalando</span>`, `<span class="selo neutro">Na fila</span>`,
		"Atualizando tudo, uma de cada vez: 2 na fila", `action="` + a.base() + `cancelar-fila"`} {
		if !strings.Contains(b, tr) {
			t.Errorf("faltou %q", tr)
		}
	}
	post(a, a.base()+"cancelar-fila", url.Values{}, origem)
	if b := get(a, a.base()).Body.String(); !strings.Contains(b, "2 atualizações saíram da fila") {
		t.Error("aviso do cancelar")
	}
	time.Sleep(30 * time.Millisecond)
	m.mu.Lock()
	if strings.Join(m.pedidos, ",") != "7zip" {
		t.Errorf("depois de cancelar: %v", m.pedidos)
	}
	m.mu.Unlock()

	um := &maquina{itens: []protocol.SelfServiceItem{{App: "7zip", Name: "7-Zip", Installed: "26.01", Target: "26.04", Action: "atualizar"}}}
	a2 := New(um.call)
	a2.Addr = "127.0.0.1:5555"
	if b := get(a2, a2.base()).Body.String(); strings.Contains(b, "Atualizar tudo") {
		t.Error("uma atualização só: sem o botão")
	}
}
