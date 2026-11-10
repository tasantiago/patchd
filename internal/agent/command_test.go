package agent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/cmdexec"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
)

// O comando do terminal: o resultado leva o código de saída e a saída; o que passa do tempo
// volta como falha, com o que saiu; o interpretador que a máquina não tem é recusa.
func TestRunCommands(t *testing.T) {
	a := &Agent{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	a.defaults()
	a.Command = func(_ context.Context, p jobs.CommandParams) (cmdexec.Result, error) {
		switch p.Command {
		case "lento":
			return cmdexec.Result{Output: "antes", TimedOut: true, ExitCode: -1}, nil
		case "sem":
			return cmdexec.Result{}, fmt.Errorf("%w: esta máquina não tem o interpretador cmd", jobs.ErrRefused)
		}
		return cmdexec.Result{Output: "Running  Spooler", ExitCode: 3, Truncated: true}, nil
	}
	out := a.runCommands(context.Background(), []acceptedCommand{
		{id: 1, p: jobs.CommandParams{Shell: "powershell", Command: "Get-Service", TimeoutSec: 60}},
		{id: 2, p: jobs.CommandParams{Shell: "bash", Command: "lento", TimeoutSec: 5}},
		{id: 3, p: jobs.CommandParams{Shell: "cmd", Command: "sem", TimeoutSec: 5}},
	})
	if len(out) != 3 {
		t.Fatalf("%+v", out)
	}
	if r := out[0].Result; r.Status != protocol.JobResultOK || r.ExitCode == nil || *r.ExitCode != 3 || r.Output != "Running  Spooler" ||
		!strings.Contains(r.Detail, "código de saída 3") || !strings.Contains(r.Detail, "saída cortada") {
		t.Errorf("normal: %+v", r)
	}
	if r := out[1].Result; r.Status != protocol.JobResultFailed || r.ExitCode != nil || r.Output != "antes" || !strings.Contains(r.Detail, "tempo máximo de 5 s") {
		t.Errorf("tempo: %+v", r)
	}
	if r := out[2].Result; r.Status != protocol.JobResultRejected || !strings.Contains(r.Detail, "não tem o interpretador") {
		t.Errorf("recusa: %+v", r)
	}
}
