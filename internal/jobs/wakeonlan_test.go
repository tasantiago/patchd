package jobs

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWakeOnLANParams(t *testing.T) {
	ok := WakeOnLANParams{Target: "m-1", MACs: []string{"00:11:22:33:44:55"}, Network: "192.0.2.0/24"}
	raw, err := WakeOnLANParamsJSON(ok)
	if err != nil {
		t.Fatal(err)
	}
	if p, err := ParseWakeOnLANParams(raw); err != nil || p.MACs[0] != "00:11:22:33:44:55" {
		t.Fatalf("%+v %v", p, err)
	}
	for nome, c := range map[string]string{
		"campo a mais": `{"target":"m","macs":["00:11:22:33:44:55"],"network":"192.0.2.0/24","port":7}`,
		"sem MAC":      `{"target":"m","macs":[],"network":"192.0.2.0/24"}`,
		"broadcast":    `{"target":"m","macs":["ff:ff:ff:ff:ff:ff"],"network":"192.0.2.0/24"}`,
		"maiúsculas":   `{"target":"m","macs":["00:11:22:33:44:AA"],"network":"192.0.2.0/24"}`,
		"repetido":     `{"target":"m","macs":["00:11:22:33:44:55","00:11:22:33:44:55"],"network":"192.0.2.0/24"}`,
		"bits de host": `{"target":"m","macs":["00:11:22:33:44:55"],"network":"192.0.2.7/24"}`,
		"rede enorme":  `{"target":"m","macs":["00:11:22:33:44:55"],"network":"10.0.0.0/4"}`,
		"ipv6":         `{"target":"m","macs":["00:11:22:33:44:55"],"network":"2001:db8::/64"}`,
		"sem máquina":  `{"macs":["00:11:22:33:44:55"],"network":"192.0.2.0/24"}`,
		"MACs demais":  `{"target":"m","macs":[` + strings.Repeat(`"00:11:22:33:44:55",`, 8) + `"00:11:22:33:44:56"],"network":"192.0.2.0/24"}`,
	} {
		if _, err := ParseWakeOnLANParams(json.RawMessage(c)); err == nil {
			t.Errorf("%s: aceito", nome)
		}
	}
	if !KnownType(TypeWakeOnLAN) {
		t.Fatal("tipo desconhecido")
	}
}
