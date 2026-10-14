package hoststate

import (
	"encoding/binary"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

// registro monta um registro do utmp como o glibc grava.
func registro(typ uint16, pid int32, line, user, host string) []byte {
	r := make([]byte, utmpSize)
	binary.LittleEndian.PutUint16(r[0:], typ)
	binary.LittleEndian.PutUint32(r[utmpOffPID:], uint32(pid))
	copy(r[utmpOffLine:utmpOffLine+utmpLenLine], line)
	copy(r[utmpOffUser:utmpOffUser+utmpLenUser], user)
	copy(r[utmpOffHost:utmpOffHost+utmpLenHost], host)
	return r
}

func TestUtmp(t *testing.T) {
	var b []byte
	b = append(b, registro(2, 0, "~", "reboot", "6.8.0")...) // BOOT_TIME: fora
	b = append(b, registro(6, 900, "tty2", "LOGIN", "")...)  // LOGIN_PROCESS: fora
	b = append(b, registro(utmpUser, 1001, "tty1", "fulano", "")...)
	b = append(b, registro(utmpUser, 1002, "pts/0", "fulano", "")...) // janela de terminal
	b = append(b, registro(utmpUser, 1003, "pts/1", "fulano", "")...) // outra janela: junta
	b = append(b, registro(utmpUser, 1004, "pts/2", "suporte", "192.0.2.50")...)
	b = append(b, registro(utmpUser, 1005, "pts/3", "caiu", "192.0.2.51")...) // processo morto
	b = append(b, registro(utmpUser, 1006, ":1", "bia", ":1")...)
	b = append(b, registro(8, 1007, "pts/4", "", "")...) // DEAD_PROCESS
	b = append(b, make([]byte, 100)...)                  // registro cortado no fim

	vivos := map[int32]bool{1001: true, 1002: true, 1003: true, 1004: true, 1006: true}
	gecos := map[string]string{"fulano": "Fulano de Tal", "bia": "Bia\x1bLima"}
	got := utmpUsers(parseUtmp(b), func(p int32) bool { return vivos[p] }, func(u string) string { return gecos[u] })
	want := []protocol.LoggedUser{
		{Account: "bia", DisplayName: "BiaLima", Session: protocol.SessionConsole, Active: true},
		{Account: "fulano", DisplayName: "Fulano de Tal", Session: protocol.SessionConsole, Active: true},
		{Account: "suporte", Session: protocol.SessionSSH, Active: true},
		{Account: "fulano", DisplayName: "Fulano de Tal", Session: protocol.SessionTerminal, Active: true},
	}
	if len(got) != len(want) {
		t.Fatalf("usuários:\n%+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d: %+v, quer %+v", i, got[i], want[i])
		}
	}
	if s := (protocol.MachineState{CollectedAt: time.Now(), Users: got}); s.Check() != nil {
		t.Errorf("o protocolo recusa: %v", s.Check())
	}
}

func TestSessoesDoLogind(t *testing.T) {
	casos := []struct {
		arquivo string
		quer    protocol.LoggedUser
		ok      bool
	}{
		{"# This is private data. Do not parse.\nUID=1000\nUSER=fulano\nACTIVE=1\nSTATE=active\nREMOTE=0\nTYPE=wayland\nCLASS=user\nSEAT=seat0\nSERVICE=gdm-password\n",
			protocol.LoggedUser{Account: "fulano", Session: protocol.SessionConsole, Active: true}, true},
		{"USER=suporte\nACTIVE=1\nSTATE=active\nREMOTE=1\nREMOTE_HOST=192.0.2.50\nTYPE=tty\nCLASS=user\nSERVICE=sshd\nTTY=pts/0\n",
			protocol.LoggedUser{Account: "suporte", Session: protocol.SessionSSH, Active: true}, true},
		{"USER=bia\nACTIVE=0\nSTATE=online\nTYPE=x11\nCLASS=user\nSEAT=seat0\n",
			protocol.LoggedUser{Account: "bia", Session: protocol.SessionConsole}, true},
		{"USER=ana\nACTIVE=1\nSTATE=active\nCLASS=user\nSERVICE=xrdp-sesman\n",
			protocol.LoggedUser{Account: "ana", Session: protocol.SessionRDP, Active: true}, true},
		{"USER=gdm\nACTIVE=1\nSTATE=active\nTYPE=wayland\nCLASS=greeter\n", protocol.LoggedUser{}, false},
		{"USER=fulano\nSTATE=closing\nCLASS=user\n", protocol.LoggedUser{}, false},
		{"USER=fulano\nCLASS=manager\n", protocol.LoggedUser{}, false},
	}
	for i, c := range casos {
		got, ok := logindSession(c.arquivo)
		if ok != c.ok || got != c.quer {
			t.Errorf("%d: %+v %t, quer %+v %t", i, got, ok, c.quer, c.ok)
		}
	}
}

func TestOrganizaUsuarios(t *testing.T) {
	in := []protocol.LoggedUser{
		{Account: `EX\ana`, Session: protocol.SessionRDP, Active: false},
		{Account: `EX\caio`, Session: protocol.SessionRDP, Active: false},
		{Account: `EX\Bia`, Session: protocol.SessionConsole, Active: true},
		{Account: `EX\ana`, Session: protocol.SessionRDP, Active: true, DisplayName: "Ana Souza"},
	}
	// A desconectada da ana junta com a ativa (e ganha o nome); console antes de RDP.
	got := tidyUsers(in)
	if len(got) != 3 || got[2].Account != `EX\caio` || got[0].Account != `EX\Bia` || got[1].Account != `EX\ana` || !got[1].Active || got[1].DisplayName != "Ana Souza" {
		t.Errorf("%+v", got)
	}
	muitos := make([]protocol.LoggedUser, protocol.MaxStateUsers+5)
	for i := range muitos {
		muitos[i] = protocol.LoggedUser{Account: strings.Repeat("u", i+1), Session: protocol.SessionSSH}
	}
	if n := len(tidyUsers(muitos)); n != protocol.MaxStateUsers {
		t.Errorf("corte: %d", n)
	}
	if got := clip(strings.Repeat("é", 200)); len(got) > 256 || !strings.HasSuffix(got, "é") {
		t.Errorf("clip quebrou um caractere: %d bytes", len(got))
	}
}

func TestCacheDoNome(t *testing.T) {
	agora := time.Date(2026, 10, 14, 8, 0, 0, 0, time.UTC)
	c := newNameCache()
	c.now = func() time.Time { return agora }
	c.wait = 50 * time.Millisecond
	var buscas atomic.Int32
	rapido := func() string { buscas.Add(1); return "Fulano de Tal" }

	if n := c.get(`EX\fulano`, rapido); n != "Fulano de Tal" {
		t.Fatalf("primeira: %q", n)
	}
	if n := c.get(`ex\FULANO`, rapido); n != "Fulano de Tal" || buscas.Load() != 1 {
		t.Errorf("guardado (sem diferença de maiúsculas): %q, %d buscas", n, buscas.Load())
	}
	agora = agora.Add(nameTTL + time.Minute)
	c.get(`EX\fulano`, rapido)
	if buscas.Load() != 2 {
		t.Errorf("vencido: %d buscas", buscas.Load())
	}

	// O AD fora de alcance: o check-in não espera; o nome chega no seguinte.
	libera := make(chan struct{})
	lento := func() string { <-libera; return "Bia Lima" }
	inicio := time.Now()
	if n := c.get(`EX\bia`, lento); n != "" || time.Since(inicio) > time.Second {
		t.Errorf("lento: %q em %s", n, time.Since(inicio))
	}
	if n := c.get(`EX\bia`, func() string { t.Error("segunda busca com uma em andamento"); return "" }); n != "" {
		t.Errorf("em andamento: %q", n)
	}
	close(libera)
	for range 100 {
		if c.get(`EX\bia`, lento) == "Bia Lima" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if n := c.get(`EX\bia`, lento); n != "Bia Lima" {
		t.Errorf("depois: %q", n)
	}

	// Sem nome: tenta de novo depois de uma hora, não de 12.
	vazio := func() string { buscas.Add(1); return "" }
	antes := buscas.Load()
	c.get(`EX\semnome`, vazio)
	agora = agora.Add(nameMissTTL + time.Minute)
	c.get(`EX\semnome`, vazio)
	if buscas.Load()-antes != 2 {
		t.Errorf("sem nome: %d buscas", buscas.Load()-antes)
	}
}
