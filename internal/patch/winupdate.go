package patch

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/wincom"
	"github.com/tasantiago/patchd/internal/wuerr"
)

// Instalação de atualizações do Windows pelo WUA (job windows_update, Aula 16.1). Aqui fica
// o que não depende do COM, testável em qualquer SO: o critério da busca, a conferência do
// que a busca devolveu contra o pedido e a leitura dos códigos de resultado.

// WinUpdater instala as atualizações de um job windows_update. O erro que satisfaz
// errors.Is(err, jobs.ErrNotPending) é recusa (a fonte da máquina não oferece o pedido);
// jobs.ErrRetry é "agora não" (outra instalação em andamento); qualquer outro é falha.
// O job diz a fonte: a da política (o padrão) ou a Microsoft (a reserva).
type WinUpdater func(ctx context.Context, jobID int64, p jobs.WinUpdateParams) (detail string, err error)

// WinCache: instalar com os arquivos do cache do servidor (docs/cache-windows.md), no lugar
// do download do próprio Windows Update (que viria do WSUS ou da Microsoft).
type WinCache struct {
	// Fetch traz do servidor os arquivos (endereço que o Windows Update deu → arquivo local
	// conferido).
	Fetch func(ctx context.Context, urls []string) (map[string]string, error)
	// Catalog: o catálogo offline (wsusscn2.cab) conferido; "" se ainda não há, e a busca vai
	// à Microsoft.
	Catalog func() string
}

// cachePlan: os arquivos de cada parte de uma atualização (a cumulativa traz várias), na
// ordem do Windows Update. Testável fora do Windows.
type cachePart struct {
	URLs []string
}

// cacheURLs: os endereços de todas as partes, sem repetir. Erro: nenhum arquivo (o Windows
// Update não disse de onde vem a atualização) ou arquivos demais.
func cacheURLs(parts []cachePart) ([]string, error) {
	var out []string
	for _, p := range parts {
		for _, u := range p.URLs {
			if u != "" && !slices.Contains(out, u) {
				out = append(out, u)
			}
		}
	}
	switch {
	case len(out) == 0:
		return nil, errors.New("o Windows Update não informou os arquivos desta atualização")
	case len(out) > 64:
		return nil, fmt.Errorf("a atualização tem %d arquivos, mais que os 64 que o cache aceita por vez", len(out))
	}
	return out, nil
}

// cacheFiles: os arquivos locais de cada parte, na ordem; erro se algum endereço ficou sem
// arquivo.
func cacheFiles(parts []cachePart, local map[string]string) ([][]string, error) {
	out := make([][]string, len(parts))
	for i, p := range parts {
		for _, u := range p.URLs {
			f, ok := local[u]
			if !ok || f == "" {
				return nil, fmt.Errorf("o arquivo de %s não veio do servidor", u)
			}
			out[i] = append(out[i], f)
		}
	}
	return out, nil
}

// wuaTarget é uma atualização devolvida pela busca do instalador.
type wuaTarget struct {
	UpdateID  string
	Title     string
	KBs       []string
	Installed bool
}

func (t wuaTarget) label() string {
	if len(t.KBs) > 0 {
		return "KB" + t.KBs[0]
	}
	return clipTitle(t.Title)
}

// wuaOp é o resultado de uma atualização numa fase (download ou instalação):
// IUpdateDownloadResult / IUpdateInstallationResult.
type wuaOp struct {
	ResultCode     int
	HResult        int64
	RebootRequired bool
}

// OperationResultCode do WUA.
const (
	orcNotStarted         = 0
	orcInProgress         = 1
	orcSucceeded          = 2
	orcSucceededWithError = 3
	orcFailed             = 4
	orcAborted            = 5
)

// planWinUpdate confere o pedido contra a busca do instalador, que é a mesma da busca
// online (IsInstalled=0 and IsHidden=0): cada UpdateID tem de estar lá, com o KB pedido.
// Aula 16.1b: a busca não filtra por UpdateID. O critério UpdateID='…' devolveu SOAP fault
// (0x80244007) na fonte da VM do lab; o critério da busca de sempre funciona nela, e o
// filtro passou para cá. Devolve os índices (em found) a instalar e os já instalados (só
// aparecem se a busca os trouxer). Faltou algum: recusa o job inteiro, sem instalar nada.
func planWinUpdate(req []jobs.WinUpdate, found []wuaTarget) (todo, already []int, err error) {
	var problems []string
	for _, r := range req {
		i := slices.IndexFunc(found, func(t wuaTarget) bool { return strings.EqualFold(t.UpdateID, r.ID) })
		if r.ID == "" { // fonte Microsoft, pelo KB: o primeiro pendente desse KB
			i = slices.IndexFunc(found, func(t wuaTarget) bool { return slices.Contains(t.KBs, r.KB) })
		}
		switch {
		case i < 0:
			problems = append(problems, r.Label()+" não está pendente na fonte do Windows Update desta máquina (já instalada, substituída por uma mais nova ou WSUS sem aprovação?)")
		case r.KB != "" && !slices.Contains(found[i].KBs, r.KB):
			problems = append(problems, fmt.Sprintf("o UpdateID %s é de %s, não do KB%s pedido", r.ID[:8], found[i].label(), r.KB))
		case found[i].Installed:
			already = append(already, i)
		default:
			todo = append(todo, i)
		}
	}
	if len(problems) > 0 {
		return nil, nil, fmt.Errorf("%w: %s", jobs.ErrNotPending, strings.Join(problems, "; "))
	}
	return todo, already, nil
}

// HRESULTs que querem dizer "agora não": o job volta como adiado, não como falha.
var retryHResults = map[uint32]bool{
	0x80240016: true, // WU_E_INSTALL_NOT_ALLOWED: outra instalação em andamento, ou reinício pendente
	0x8024001E: true, // WU_E_SERVICE_STOP: o serviço está parando
}

// #nosec G115 -- o HRESULT tem 32 bits; o WUA o entrega com sinal
func hresultText(h int64) string { return wuerr.Text(uint32(h)) }

// comFailure classifica a falha de uma chamada COM (Download, Install): um HRESULT de
// "agora não" vira jobs.ErrRetry.
func comFailure(phase string, err error) error {
	var ce *wincom.Error
	if errors.As(err, &ce) {
		if retryHResults[ce.HResult] {
			return fmt.Errorf("%w: %s: %s", jobs.ErrRetry, phase, hresultText(int64(ce.HResult)))
		}
		return fmt.Errorf("%s: %s", phase, hresultText(int64(ce.HResult)))
	}
	return fmt.Errorf("%s: %w", phase, err)
}

// phaseFailures lista as atualizações que não passaram numa fase, e diz se todas as falhas
// são de "agora não".
func phaseFailures(targets []wuaTarget, ops []wuaOp) (failed []string, allRetry bool) {
	allRetry = true
	for i, t := range targets {
		if i >= len(ops) {
			failed, allRetry = append(failed, t.label()+" sem resultado"), false
			continue
		}
		op := ops[i]
		if op.ResultCode == orcSucceeded || op.ResultCode == orcSucceededWithError {
			continue
		}
		failed = append(failed, fmt.Sprintf("%s %s (%s)", t.label(), resultName(op.ResultCode), hresultText(op.HResult)))
		// #nosec G115 -- o HRESULT tem 32 bits; o WUA o entrega com sinal
		allRetry = allRetry && retryHResults[uint32(op.HResult)]
	}
	return failed, allRetry && len(failed) > 0
}

func resultName(code int) string {
	switch code {
	case orcNotStarted:
		return "não iniciada"
	case orcInProgress:
		return "em andamento"
	case orcFailed:
		return "falhou"
	case orcAborted:
		return "abortada"
	}
	return fmt.Sprintf("código %d", code)
}

// downloadResult: o download de alguma falhou → o job falha (ou é adiado) sem instalar.
func downloadResult(targets []wuaTarget, ops []wuaOp) error {
	failed, retry := phaseFailures(targets, ops)
	switch {
	case len(failed) == 0:
		return nil
	case retry:
		return fmt.Errorf("%w: download: %s", jobs.ErrRetry, strings.Join(failed, "; "))
	}
	return fmt.Errorf("download: %s", strings.Join(failed, "; "))
}

// installResult monta o detalhe do job depois da instalação. O reinício nunca é feito
// aqui: o detalhe diz se o Windows pede, e o operador agenda pelo job reboot.
func installResult(targets, already []wuaTarget, ops []wuaOp, reboot bool) (string, error) {
	var ok []string
	for i, t := range targets {
		if i < len(ops) && (ops[i].ResultCode == orcSucceeded || ops[i].ResultCode == orcSucceededWithError) {
			ok = append(ok, t.label())
		}
	}
	var parts []string
	if len(ok) > 0 {
		parts = append(parts, "instalada(s): "+strings.Join(ok, ", "))
	}
	if len(already) > 0 {
		labels := make([]string, len(already))
		for i, t := range already {
			labels[i] = t.label()
		}
		parts = append(parts, "já instalada(s): "+strings.Join(labels, ", "))
	}
	if reboot {
		parts = append(parts, "o Windows pede reinício para concluir (peça pelo painel: Reiniciar)")
	} else if len(ok) > 0 {
		parts = append(parts, "sem reinício")
	}
	detail := strings.Join(parts, "; ")
	failed, retry := phaseFailures(targets, ops)
	switch {
	case len(failed) == 0:
		return detail, nil
	case retry:
		return detail, fmt.Errorf("%w: instalação: %s", jobs.ErrRetry, strings.Join(failed, "; "))
	}
	return detail, fmt.Errorf("instalação: %s", strings.Join(failed, "; "))
}

// clipTitle encurta o título para o detalhe do job.
func clipTitle(s string) string {
	if r := []rune(s); len(r) > 60 {
		return string(r[:60]) + "…"
	}
	return s
}

// policyDidNotDeliver: a falha é da fonte (a busca falhou, o pedido não aparece nela, o
// download falhou), não da máquina (outra instalação em andamento, prazo): vale tentar a
// Microsoft.
func policyDidNotDeliver(err error) bool {
	var se sourceError
	return err != nil && !errors.Is(err, jobs.ErrRetry) && (errors.Is(err, jobs.ErrNotPending) || errors.As(err, &se))
}

// sourceError marca a falha da fonte (busca ou download), sem mudar a mensagem.
type sourceError struct{ error }

func (e sourceError) Unwrap() error { return e.error }
