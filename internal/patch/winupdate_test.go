package patch

import (
	"errors"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/wincom"
)

const (
	idCU  = "0b9b6f0c-1d2e-4f3a-9b8c-7d6e5f4a3b2c"
	idNET = "a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d"
)

func TestWinCriteria(t *testing.T) {
	got := winCriteria([]jobs.WinUpdate{{ID: strings.ToUpper(idCU)}, {ID: idNET}})
	if got != "UpdateID='"+idCU+"' or UpdateID='"+idNET+"'" {
		t.Errorf("critério: %s", got)
	}
}

func TestPlanWinUpdate(t *testing.T) {
	found := []wuaTarget{
		{UpdateID: idNET, KBs: []string{"5130001"}, Installed: true},
		{UpdateID: strings.ToUpper(idCU), KBs: []string{"5127648"}},
	}
	todo, already, err := planWinUpdate([]jobs.WinUpdate{{ID: idCU, KB: "5127648"}, {ID: idNET, KB: "5130001"}}, found)
	if err != nil || len(todo) != 1 || todo[0] != 1 || len(already) != 1 || already[0] != 0 {
		t.Fatalf("plano: %v %v %v", todo, already, err)
	}
	// A fonte não devolveu (WSUS sem aprovação): recusa, sem instalar nada.
	_, _, err = planWinUpdate([]jobs.WinUpdate{{ID: idCU, KB: "5127648"}, {ID: "11111111-2222-4333-8444-555555555555", KB: "5999999"}}, found)
	if !errors.Is(err, jobs.ErrNotPending) || !strings.Contains(err.Error(), "KB5999999 não é oferecida") {
		t.Errorf("faltando: %v", err)
	}
	// O UpdateID veio, mas de outro KB: recusa.
	_, _, err = planWinUpdate([]jobs.WinUpdate{{ID: idCU, KB: "5000000"}}, found)
	if !errors.Is(err, jobs.ErrNotPending) || !strings.Contains(err.Error(), "é de KB5127648, não do KB5000000") {
		t.Errorf("KB trocado: %v", err)
	}
}

func TestResultadoDaInstalacao(t *testing.T) {
	cu := wuaTarget{UpdateID: idCU, KBs: []string{"5127648"}}
	net := wuaTarget{UpdateID: idNET, KBs: []string{"5130001"}}

	d, err := installResult([]wuaTarget{cu}, []wuaTarget{net}, []wuaOp{{ResultCode: orcSucceeded, RebootRequired: true}}, true)
	if err != nil || d != "instalada(s): KB5127648; já instalada(s): KB5130001; o Windows pede reinício para concluir (peça pelo painel: Reiniciar)" {
		t.Errorf("sucesso: %q %v", d, err)
	}
	d, err = installResult(nil, []wuaTarget{net}, nil, false)
	if err != nil || d != "já instalada(s): KB5130001" {
		t.Errorf("nada a fazer: %q %v", d, err)
	}
	// Uma instalou, outra falhou: falha, com o que deu certo no detalhe.
	d, err = installResult([]wuaTarget{cu, net}, nil, []wuaOp{{ResultCode: orcSucceeded}, {ResultCode: orcFailed, HResult: -2146498270}}, false)
	if err == nil || errors.Is(err, jobs.ErrRetry) || !strings.Contains(err.Error(), "KB5130001 falhou (0x800F0922 CBS_E_INSTALLERS_FAILED)") ||
		d != "instalada(s): KB5127648; sem reinício" {
		t.Errorf("parcial: %q %v", d, err)
	}
	// Outra instalação em andamento: adiado.
	_, err = installResult([]wuaTarget{cu}, nil, []wuaOp{{ResultCode: orcFailed, HResult: int64(int32(-2145124330))}}, false)
	if !errors.Is(err, jobs.ErrRetry) {
		t.Errorf("WU_E_INSTALL_NOT_ALLOWED vira adiado: %v", err)
	}
	if err := downloadResult([]wuaTarget{cu}, []wuaOp{{ResultCode: orcFailed, HResult: 0x8024402C}}); err == nil ||
		!strings.Contains(err.Error(), "download: KB5127648 falhou (0x8024402C WU_E_PT_WINHTTP_NAME_NOT_RESOLVED)") {
		t.Errorf("download: %v", err)
	}
	if err := comFailure("instalação", &wincom.Error{Op: "Install", HResult: 0x80240016}); !errors.Is(err, jobs.ErrRetry) {
		t.Errorf("chamada COM recusada por estar ocupado: %v", err)
	}
}
