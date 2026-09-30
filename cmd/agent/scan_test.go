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
	missing    []protocol.MissingUpdate
	missingErr error
	history    []protocol.UpdateHistoryEntry
	historyErr error
}

func (s scannerFalso) Missing(ctx context.Context) ([]protocol.MissingUpdate, error) {
	return s.missing, s.missingErr
}

func (s scannerFalso) History(ctx context.Context, max int) ([]protocol.UpdateHistoryEntry, error) {
	return s.history, s.historyErr
}

func TestScanNadaFaltandoEhListaVazia(t *testing.T) {
	var buf bytes.Buffer
	if err := printScan(context.Background(), &buf, scannerFalso{}, "v"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"missing": []`) || !strings.Contains(buf.String(), `"history": []`) {
		t.Errorf("sem itens, as listas deveriam ser []:\n%s", buf.String())
	}
}

func TestScanNaoImplementadoEhNull(t *testing.T) {
	s := scannerFalso{missingErr: patch.ErrNotImplemented, historyErr: patch.ErrNotImplemented}
	rep := collectScan(context.Background(), s, "v")
	if rep.Missing != nil || rep.History != nil || len(rep.Errors) != 2 {
		t.Errorf("seções não coletadas deveriam ser null com motivo: %+v", rep)
	}
}

func TestScanParcialMantemLista(t *testing.T) {
	s := scannerFalso{
		missing:    []protocol.MissingUpdate{{ID: "a"}},
		missingErr: errors.New("busca concluída com erros (ResultCode 3)"),
	}
	rep := collectScan(context.Background(), s, "v")
	if len(rep.Missing) != 1 || len(rep.Errors) != 1 || !strings.HasPrefix(rep.Errors[0], "missing: ") {
		t.Errorf("em falha parcial, a lista e o erro deveriam vir juntos: %+v", rep)
	}
}
