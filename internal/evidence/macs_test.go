package evidence

import (
	"net"
	"reflect"
	"testing"
)

func TestFactoryMACs(t *testing.T) {
	mac := func(s string) net.HardwareAddr { m, _ := net.ParseMAC(s); return m }
	ifaces := []net.Interface{
		{Name: "lo", Flags: net.FlagLoopback},
		{Name: "eth0", HardwareAddr: mac("00:1A:2B:3C:4D:5E")},
		{Name: "docker0", HardwareAddr: mac("02:42:ac:11:00:02")},
		{Name: "wlan0", HardwareAddr: mac("da:a1:19:00:00:01")},
		{Name: "eth1", HardwareAddr: mac("00:0c:29:aa:bb:cc")},
		{Name: "eth0.10", HardwareAddr: mac("00:1a:2b:3c:4d:5e")},
		{Name: "tun0"},
	}
	got := factoryMACs(ifaces)
	quer := []string{"00:0c:29:aa:bb:cc", "00:1a:2b:3c:4d:5e"}
	if !reflect.DeepEqual(got, quer) {
		t.Errorf("MACs = %v, esperado %v", got, quer)
	}
}
