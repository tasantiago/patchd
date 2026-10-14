package protocol

import (
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"
)

// Endereços de documentação (RFC 5737 e 3849): nenhum é de rede real.
func estadoValido() MachineState {
	return MachineState{
		CollectedAt: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC),
		Interfaces: []NetInterface{
			{Name: "docker0", MAC: "02:42:ac:11:00:01", IPv4: []string{"172.17.0.1/16"}, Up: true, LocalMAC: true},
			{Name: "eth0", MAC: "00:15:5d:01:02:03", IPv4: []string{"192.0.2.10/24"}, IPv6: []string{"2001:db8::10/64"}, Up: true},
			{Name: "wlan0", MAC: "00:11:22:33:44:55", IPv4: []string{"198.51.100.7/24"}},
		},
		Disks: []Disk{{Mount: "/", FS: "ext4", Device: "/dev/sda2", TotalBytes: 100, FreeBytes: 40}},
		Users: []LoggedUser{{Account: `EXEMPLO\fulano`, DisplayName: "Fulano de Tal", Session: SessionConsole, Active: true},
			{Account: "suporte", Session: SessionSSH, Active: true}},
	}
}

func TestCheckEstado(t *testing.T) {
	if err := estadoValido().Check(); err != nil {
		t.Fatalf("estado válido recusado: %v", err)
	}
	muitas := func(n int) []NetInterface {
		l := make([]NetInterface, n)
		for i := range l {
			l[i] = NetInterface{Name: "veth"}
		}
		return l
	}
	casos := map[string]func(*MachineState){
		"sem data":              func(s *MachineState) { s.CollectedAt = time.Time{} },
		"MAC torto":             func(s *MachineState) { s.Interfaces[0].MAC = "02:42:zz" },
		"IPv4 sem prefixo":      func(s *MachineState) { s.Interfaces[1].IPv4 = []string{"192.0.2.10"} },
		"IPv6 na lista v4":      func(s *MachineState) { s.Interfaces[1].IPv4 = []string{"2001:db8::1/64"} },
		"IPv4 na lista v6":      func(s *MachineState) { s.Interfaces[1].IPv6 = []string{"192.0.2.1/24"} },
		"interface sem nome":    func(s *MachineState) { s.Interfaces[0].Name = "" },
		"nome com controle":     func(s *MachineState) { s.Interfaces[0].Name = "eth0\x1b[31m" },
		"montagem enorme":       func(s *MachineState) { s.Disks[0].Mount = "/" + strings.Repeat("a", 300) },
		"livre > total":         func(s *MachineState) { s.Disks[0].FreeBytes = 101 },
		"modelo com controle":   func(s *MachineState) { s.Disks[0].Model = "WDC\x00" },
		"interfaces demais":     func(s *MachineState) { s.Interfaces = muitas(MaxStateInterfaces + 1) },
		"conta vazia":           func(s *MachineState) { s.Users[0].Account = "" },
		"exibição com controle": func(s *MachineState) { s.Users[0].DisplayName = "Fulano\x1b]0;x" },
		"sessão desconhecida":   func(s *MachineState) { s.Users[1].Session = "telnet" },
		"usuários demais":       func(s *MachineState) { s.Users = make([]LoggedUser, MaxStateUsers+1) },
	}
	for nome, muda := range casos {
		s := estadoValido()
		muda(&s)
		if err := s.Check(); err == nil {
			t.Errorf("%s: aceito", nome)
		}
	}
}

func TestPrimaryIPv4(t *testing.T) {
	s := estadoValido()
	casos := []struct{ visto, quer string }{
		// O endereço do check-in, quando é da máquina, ganha (mesmo o do docker0).
		{"172.17.0.1", "172.17.0.1"},
		{"::ffff:198.51.100.7", "198.51.100.7"}, // IPv4 mapeado em IPv6 (socket dual-stack)
		// Visto de fora (NAT): a primeira interface ativa com MAC de fabricante.
		{"203.0.113.50", "192.0.2.10"},
		{"", "192.0.2.10"},
	}
	for _, c := range casos {
		if got := s.PrimaryIPv4(c.visto); got != c.quer {
			t.Errorf("visto %q: %q, quer %q", c.visto, got, c.quer)
		}
	}
	// Só sobrou a bridge: ela serve, na falta de outra.
	s.Interfaces = s.Interfaces[:1]
	if got := s.PrimaryIPv4(""); got != "172.17.0.1" {
		t.Errorf("só a bridge: %q", got)
	}
	// Sem DHCP (169.254) não representa a máquina.
	s.Interfaces = []NetInterface{{Name: "eth0", IPv4: []string{"169.254.10.1/16"}, Up: true}}
	if got := s.PrimaryIPv4(""); got != "" {
		t.Errorf("169.254: %q", got)
	}
}

func TestHasAddr(t *testing.T) {
	s := estadoValido()
	for addr, quer := range map[string]bool{"192.0.2.10": true, "2001:db8::10": true, "::ffff:192.0.2.10": true, "192.0.2.11": false, "lixo": false} {
		if s.HasAddr(addr) != quer {
			t.Errorf("%s: quer %t", addr, quer)
		}
	}
}

func TestIsLocalMAC(t *testing.T) {
	for mac, quer := range map[string]bool{"02:42:ac:11:00:02": true, "52:54:00:12:34:56": true, "00:15:5d:01:02:03": false, "f0:de:f1:00:00:01": false} {
		m, _ := net.ParseMAC(mac)
		if IsLocalMAC(m) != quer {
			t.Errorf("%s: quer %t", mac, quer)
		}
	}
}

// Aula 15.6: null (o agente não coleta) e [] (ninguém conectado) são diferentes no JSON.
func TestUsuariosNuloEVazio(t *testing.T) {
	var s MachineState
	if err := json.Unmarshal([]byte(`{"collected_at":"2026-10-13T12:00:00Z","interfaces":[],"disks":[]}`), &s); err != nil || s.Users != nil {
		t.Errorf("agente anterior: %+v %v", s.Users, err)
	}
	if err := json.Unmarshal([]byte(`{"collected_at":"2026-10-13T12:00:00Z","interfaces":[],"disks":[],"users":[]}`), &s); err != nil || s.Users == nil || len(s.Users) != 0 {
		t.Errorf("ninguém conectado: %+v %v", s.Users, err)
	}
}

// Um servidor anterior à 12.1 recebe o campo state e o ignora; um agente anterior não manda.
func TestCheckinSemEstadoNoJSON(t *testing.T) {
	b, _ := json.Marshal(CheckinRequest{AgentVersion: "v", InventoryHash: "h"})
	if strings.Contains(string(b), "state") {
		t.Errorf("sem estado, o campo não vai: %s", b)
	}
}
