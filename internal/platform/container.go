package platform

import "strings"

// cgroupMarkers são trechos do /proc/1/cgroup que só aparecem dentro de containers
// (cgroup v1 ou híbrido). Com cgroup v2 e namespace próprio, o container vê só "0::/",
// e por isso os arquivos-marca vêm primeiro.
var cgroupMarkers = []string{"/docker/", "/docker-", "/kubepods", "/containerd", "/lxc/", "/libpod-"}

// detectContainer reconhece um container pelos sinais que os motores deixam: o
// /.dockerenv (Docker), o /run/.containerenv (Podman), a variável "container" (systemd-
// nspawn, Podman, LXC) e as marcas no cgroup do processo 1. Devolve o primeiro sinal
// encontrado, para a mensagem dizer por quê. Recebe as leituras por parâmetro: testável.
func detectContainer(exists func(string) bool, readFile func(string) ([]byte, error), getenv func(string) string) (bool, string) {
	if exists("/.dockerenv") {
		return true, "/.dockerenv"
	}
	if exists("/run/.containerenv") {
		return true, "/run/.containerenv"
	}
	if v := getenv("container"); v != "" {
		return true, "variável container=" + v
	}
	if data, err := readFile("/proc/1/cgroup"); err == nil {
		s := string(data)
		for _, m := range cgroupMarkers {
			if strings.Contains(s, m) {
				return true, "/proc/1/cgroup contém " + strings.Trim(m, "/-")
			}
		}
	}
	return false, ""
}
