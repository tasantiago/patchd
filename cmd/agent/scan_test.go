package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/patch"
	"github.com/tasantiago/patchd/internal/protocol"
)

type scannerFalso struct {
	scans      []protocol.ScanResult
	reboot     protocol.RebootStatus
	rebootErr  error
	history    []protocol.UpdateHistoryEntry
	historyErr error
	recebido   patch.Options
}

func (s *scannerFalso) Scan(ctx context.Context, opts patch.Options) []protocol.ScanResult {
	s.recebido = opts
	return s.scans
}

func (s *scannerFalso) Reboot(ctx context.Context) (protocol.RebootStatus, error) {
	return s.reboot, s.rebootErr
}

func (s *scannerFalso) History(ctx context.Context, max int) ([]protocol.UpdateHistoryEntry, error) {
	return s.history, s.historyErr
}

func TestScanRepassaOpcoesESchema2(t *testing.T) {
	s := &scannerFalso{scans: []protocol.ScanResult{{Source: patch.SourceWUADefault, Missing: []protocol.MissingUpdate{}}}}
	rep := collectScan(context.Background(), s, patch.Options{OfflineCatalog: `C:\patchd\wsusscn2.cab`}, "v")
	if rep.SchemaVersion != 2 || len(rep.Scans) != 1 || s.recebido.OfflineCatalog != `C:\patchd\wsusscn2.cab` {
		t.Errorf("schema, scans ou opções errados: %+v / %+v", rep, s.recebido)
	}
}

func TestScanHistoricoVazioEhListaVazia(t *testing.T) {
	var buf bytes.Buffer
	if err := printScan(context.Background(), &buf, &scannerFalso{}, patch.Options{}, "v"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"history": []`) {
		t.Errorf("histórico sem itens deveria ser []:\n%s", buf.String())
	}
}

func TestScanHistoricoNaoImplementadoEhNull(t *testing.T) {
	rep := collectScan(context.Background(), &scannerFalso{historyErr: patch.ErrNotImplemented}, patch.Options{}, "v")
	if rep.History != nil || len(rep.Errors) != 1 || !strings.HasPrefix(rep.Errors[0], "history: ") {
		t.Errorf("histórico não coletado deveria ser null com motivo: %+v", rep)
	}
}

func TestScanRebootPendenteEFalha(t *testing.T) {
	sim := true
	rep := collectScan(context.Background(), &scannerFalso{reboot: protocol.RebootStatus{Pending: &sim}}, patch.Options{}, "v")
	if rep.Reboot == nil || rep.Reboot.Pending == nil || !*rep.Reboot.Pending {
		t.Errorf("reinício pendente deveria vir no relatório: %+v", rep.Reboot)
	}

	rep = collectScan(context.Background(), &scannerFalso{rebootErr: errors.New("COM indisponível")}, patch.Options{}, "v")
	if rep.Reboot != nil || len(rep.Errors) != 1 || !strings.HasPrefix(rep.Errors[0], "reboot: ") {
		t.Errorf("falha ao verificar o reinício: null e motivo: %+v", rep)
	}
}
