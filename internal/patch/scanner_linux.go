package patch

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/tasantiago/patchd/internal/inventory"
	"github.com/tasantiago/patchd/internal/platform"
	"github.com/tasantiago/patchd/internal/protocol"
)

// linuxScanner busca atualizações pelo gerenciador da distribuição (apt ou dnf).
type linuxScanner struct {
	run      platform.Runner
	readFile func(string) ([]byte, error)
	listDir  func(string) ([]string, error)
	modTime  func(string) (time.Time, error)
	// inContainer: nil nos testes (nunca em container).
	inContainer func() (bool, string)
}

// New devolve o scanner do SO atual.
func New(run platform.Runner) Scanner {
	return linuxScanner{run: run, readFile: os.ReadFile, listDir: listDirNames, modTime: fileModTime, inContainer: platform.InContainer}
}

// Scan escolhe a fonte pelo os-release (a mesma decisão do inventário) e executa a busca.
func (s linuxScanner) Scan(ctx context.Context, opts Options) []protocol.ScanResult {
	var results []protocol.ScanResult

	manager, id, err := inventory.DetectPackageManager(s.readFile)
	switch {
	case err != nil:
		results = append(results, protocol.ScanResult{Source: "linux", Error: err.Error()})
	case manager == "dpkg":
		results = append(results, s.scanApt(ctx))
	case manager == "rpm":
		results = append(results, s.scanDnf(ctx))
	default:
		results = append(results, protocol.ScanResult{
			Source: "linux",
			Error:  fmt.Sprintf("distribuição %q sem busca de atualizações: %v", id, ErrNotImplemented),
		})
	}

	if opts.OfflineCatalog != "" {
		results = append(results, offlineNotWindows())
	}
	return results
}

// scanApt simula a atualização completa, sem atualizar as listas nem instalar nada.
// As datas do catálogo vão no resultado: listas velhas produzem falso negativo.
func (s linuxScanner) scanApt(ctx context.Context) protocol.ScanResult {
	start := time.Now()
	r := protocol.ScanResult{Source: SourceApt}
	r.CatalogModifiedAt, r.CatalogCheckedAt = aptMetadataDates(s.readFile, s.listDir, s.modTime)

	out, err := s.run.Run(ctx, aptGetPath, "-s", "dist-upgrade")
	r.DurationMS = time.Since(start).Milliseconds()
	if err != nil {
		r.Error = err.Error()
		return r
	}
	list, perr := parseAptSimulation(out)
	if list == nil {
		list = []protocol.MissingUpdate{}
	}
	r.Missing = list
	if perr != nil {
		r.Error = perr.Error()
	}
	return r
}

// scanDnf lista os advisories de segurança pendentes. O dnf baixa metadados sozinho
// quando os locais vencem: a busca pode levar mais de um minuto.
func (s linuxScanner) scanDnf(ctx context.Context) protocol.ScanResult {
	start := time.Now()
	r := protocol.ScanResult{Source: SourceDnf}

	out, err := s.run.Run(ctx, dnfPath, "advisory", "list", "--security", "--json")
	r.DurationMS = time.Since(start).Milliseconds()
	if err != nil {
		r.Error = err.Error()
		return r
	}
	list, perr := parseDnfAdvisories(out)
	r.Missing = list
	if perr != nil {
		r.Error = perr.Error()
	}
	return r
}

// History: o histórico do dpkg e do dnf entra numa etapa seguinte.
func (linuxScanner) History(ctx context.Context, max int) ([]protocol.UpdateHistoryEntry, error) {
	return nil, ErrNotImplemented
}
