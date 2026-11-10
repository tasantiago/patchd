package jobs

import (
	"strings"
	"testing"
)

func TestCommandParams(t *testing.T) {
	ok := `{"shell":"powershell","command":"Get-Service Spooler","timeout_sec":60}`
	if p, err := ParseCommandParams([]byte(ok)); err != nil || p.Shell != ShellPowerShell || p.Timeout().Seconds() != 60 {
		t.Fatalf("%+v %v", p, err)
	}
	for nome, raw := range map[string]string{
		"interpretador":  `{"shell":"python","command":"x","timeout_sec":60}`,
		"vazio":          `{"shell":"cmd","command":"   ","timeout_sec":60}`,
		"duas linhas":    `{"shell":"bash","command":"a\nb","timeout_sec":60}`,
		"tempo curto":    `{"shell":"bash","command":"a","timeout_sec":1}`,
		"tempo longo":    `{"shell":"bash","command":"a","timeout_sec":601}`,
		"campo a mais":   `{"shell":"bash","command":"a","timeout_sec":60,"user":"root"}`,
		"comando enorme": `{"shell":"bash","command":"` + strings.Repeat("a", MaxCommandLen+1) + `","timeout_sec":60}`,
		"caractere nulo": `{"shell":"bash","command":"a\u0000b","timeout_sec":60}`,
	} {
		if _, err := ParseCommandParams([]byte(raw)); err == nil {
			t.Errorf("%s: aceito", nome)
		}
	}
	if !KnownType(TypeCommand) || CommandTTL < MinTTL {
		t.Error("tipo ou prazo")
	}
}
