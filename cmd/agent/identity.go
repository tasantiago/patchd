package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/tasantiago/patchd/internal/evidence"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/platform"
	"github.com/tasantiago/patchd/internal/protocol"
)

// identityReport é a saída de "patchd-agent identity": o que a máquina declararia no
// registro e como o servidor vai enxergar. Serve para conferir a coleta sem servidor.
type identityReport struct {
	Evidence  protocol.IdentityEvidence `json:"evidence"`
	Keys      identityKeys              `json:"keys"`
	Strong    bool                      `json:"strong"`    // o servidor aceitaria o registro
	Discarded []string                  `json:"discarded"` // o que o servidor descartaria, e por quê
	Problems  []string                  `json:"problems"`  // fontes que não puderam ser lidas
}

type identityKeys struct {
	InstallID   string `json:"install_id"`
	HardwareKey string `json:"hardware_key"`
	Serial      string `json:"serial"`
}

// runIdentity imprime as evidências de identidade em JSON e sai. Não fala com o servidor.
func runIdentity(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintln(stderr, "uso: patchd-agent identity")
		return exitConfig
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	ev, problems := evidence.Collect(ctx, platform.ExecRunner{})
	keys, discarded := identity.Normalize(ev)
	rep := identityReport{
		Evidence:  ev,
		Keys:      identityKeys{InstallID: keys.InstallID, HardwareKey: keys.HardwareKey, Serial: keys.Serial},
		Strong:    keys.Strong(),
		Discarded: nonNil(discarded),
		Problems:  nonNil(problems),
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(rep); err != nil {
		fmt.Fprintf(stderr, "patchd-agent: %v\n", err)
		return exitRuntime
	}
	return exitOK
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
