package patch

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/version"
)

// rebootDecision consolida os sinais: pendente se algum sinal forte disse que sim; não
// pendente se ao menos um sinal forte foi lido e nenhum disse que sim; nulo se nenhum
// sinal forte pôde ser lido. Sinais fracos nunca decidem sozinhos.
func rebootDecision(signals []protocol.RebootSignal) *bool {
	read := false
	for _, s := range signals {
		if s.Weak || s.Error != "" {
			continue
		}
		if s.Pending {
			t := true
			return &t
		}
		read = true
	}
	if !read {
		return nil
	}
	f := false
	return &f
}

// parseCIMDateTime lê o datetime do CIM/WMI: "AAAAMMDDHHMMSS.mmmmmm±UUU", com o
// deslocamento do fuso em MINUTOS no fim (ex.: -240 = UTC-4). Devolve em UTC.
func parseCIMDateTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if len(s) != 25 || s[14] != '.' || (s[21] != '+' && s[21] != '-') {
		return time.Time{}, fmt.Errorf("datetime do CIM fora do formato: %q", s)
	}
	base, err := time.Parse("20060102150405", s[:14])
	if err != nil {
		return time.Time{}, fmt.Errorf("datetime do CIM inválido: %q", s)
	}
	micro, err := strconv.Atoi(s[15:21])
	if err != nil {
		return time.Time{}, fmt.Errorf("datetime do CIM inválido: %q", s)
	}
	minutes, err := strconv.Atoi(s[22:25])
	if err != nil {
		return time.Time{}, fmt.Errorf("datetime do CIM inválido: %q", s)
	}
	offset := minutes * 60
	if s[21] == '-' {
		offset = -offset
	}
	t := time.Date(base.Year(), base.Month(), base.Day(), base.Hour(), base.Minute(), base.Second(),
		micro*1000, time.FixedZone("", offset))
	return t.UTC(), nil
}

// parseProcStatBtime lê a linha "btime <segundos Unix>" do /proc/stat.
func parseProcStatBtime(data []byte) (time.Time, error) {
	for _, line := range strings.Split(string(data), "\n") {
		v, ok := strings.CutPrefix(line, "btime ")
		if !ok {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil || n <= 0 {
			return time.Time{}, fmt.Errorf("btime inválido: %q", line)
		}
		return time.Unix(n, 0).UTC(), nil
	}
	return time.Time{}, fmt.Errorf("btime não encontrado em /proc/stat")
}

// kernBoottimeSec acha os segundos em "{ sec = 1791065810, usec = 452031 } Sat Oct  3 18:16:50 2026".
var kernBoottimeSec = regexp.MustCompile(`sec = (\d+)`)

// parseKernBoottime lê a saída do "sysctl -n kern.boottime" do macOS.
func parseKernBoottime(data []byte) (time.Time, error) {
	m := kernBoottimeSec.FindSubmatch(data)
	if m == nil {
		return time.Time{}, fmt.Errorf("kern.boottime fora do formato: %q", strings.TrimSpace(string(data)))
	}
	n, err := strconv.ParseInt(string(m[1]), 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(n, 0).UTC(), nil
}

// newestKernel acha o kernel mais novo entre os arquivos de /boot ("vmlinuz-<versão>").
// Ignora o link "vmlinuz", cópias ".old" e o kernel de recuperação do Fedora ("-rescue-").
func newestKernel(bootNames []string) (string, bool) {
	newest := ""
	for _, n := range bootNames {
		v, ok := strings.CutPrefix(n, "vmlinuz-")
		if !ok || v == "" || strings.Contains(v, "-rescue-") || strings.HasSuffix(v, ".old") {
			continue
		}
		if newest == "" {
			newest = v
			continue
		}
		if c, err := version.CompareRPM(v, newest); err == nil && c > 0 {
			newest = v
		}
	}
	return newest, newest != ""
}

// kernelNewer diz se o kernel instalado mais novo é mais recente que o em execução.
func kernelNewer(newest, running string) bool {
	c, err := version.CompareRPM(newest, running)
	return err == nil && c > 0
}

// parseRebootRequiredPkgs lê o /run/reboot-required.pkgs: um pacote por linha, sem repetição.
func parseRebootRequiredPkgs(data []byte) []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		p := strings.TrimSpace(line)
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}
