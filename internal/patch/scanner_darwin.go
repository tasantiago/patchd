package patch

import (
	"context"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/platform"
	"github.com/tasantiago/patchd/internal/protocol"
)

// darwinScanner busca atualizações pelo softwareupdate e lê o histórico pelo system_profiler.
type darwinScanner struct{ run platform.Runner }

// New devolve o scanner do SO atual.
func New(run platform.Runner) Scanner { return darwinScanner{run: run} }

// Scan consulta o softwareupdate. A listagem vai aos servidores da Apple (ou ao cache de
// conteúdo da rede) na hora: a data da verificação é a própria data da busca.
func (s darwinScanner) Scan(ctx context.Context, opts Options) []protocol.ScanResult {
	results := []protocol.ScanResult{s.scanSoftwareUpdate(ctx)}
	if opts.OfflineCatalog != "" {
		results = append(results, offlineNotWindows())
	}
	return results
}

func (s darwinScanner) scanSoftwareUpdate(ctx context.Context) protocol.ScanResult {
	start := time.Now()
	r := protocol.ScanResult{Source: SourceSoftwareUpdate}

	ver, err := s.run.Run(ctx, swVersPath, "-productVersion")
	if err != nil {
		r.Error = err.Error()
		r.DurationMS = time.Since(start).Milliseconds()
		return r
	}
	out, err := s.run.Run(ctx, softwareUpdatePath, "--list")
	r.DurationMS = time.Since(start).Milliseconds()
	if err != nil {
		r.Error = err.Error()
		return r
	}

	list := parseSoftwareUpdateList(out, strings.TrimSpace(string(ver)))
	if list == nil {
		list = []protocol.MissingUpdate{}
	}
	r.Missing = list
	return r
}

// History lê o histórico de instalações concluídas.
func (s darwinScanner) History(ctx context.Context, max int) ([]protocol.UpdateHistoryEntry, error) {
	out, err := s.run.Run(ctx, darwinProfilerPath, "SPInstallHistoryDataType", "-json")
	if err != nil {
		return nil, err
	}
	return parseInstallHistory(out, max)
}
