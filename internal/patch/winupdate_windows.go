package patch

import (
	"context"
	"errors"
	"fmt"
	"time"

	ole "github.com/go-ole/go-ole"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/wincom"
)

// winUpdateTimeout: busca, download e instalação de uma atualização cumulativa passam fácil
// de meia hora numa máquina lenta. Cabe na tolerância do resultado (jobs.ResultGrace).
const winUpdateTimeout = 100 * time.Minute

// NewWinUpdater liga o job windows_update (Aula 16.1).
func NewWinUpdater() WinUpdater { return installWindowsUpdates }

// installWindowsUpdates faz, numa sessão COM identificada como patchd-agent (é o nome que o
// histórico do Windows Update mostra): busca as pendentes na fonte da política, separa os
// UpdateIDs pedidos, confere, aceita a licença, baixa e instala. Nunca reinicia.
func installWindowsUpdates(ctx context.Context, jobID int64, req []jobs.WinUpdate) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, winUpdateTimeout)
	defer cancel()

	var detail string
	err := wincom.Do(ctx, func() error {
		session, err := wincom.CreateDispatch("Microsoft.Update.Session")
		if err != nil {
			return err
		}
		defer session.Release()
		if err := wincom.Put(session, "ClientApplicationID", "patchd-agent"); err != nil {
			return err
		}

		// 1. Busca: a mesma fonte e o mesmo critério da busca online (WSUS quando a política
		// define). O filtro pelos UpdateIDs pedidos é feito na conferência (Aula 16.1b).
		searcher, err := wincom.CallObject(session, "CreateUpdateSearcher")
		if err != nil {
			return err
		}
		defer searcher.Release()
		result, err := wincom.CallObject(searcher, "Search", onlineCriteria)
		if err != nil {
			return comFailure("busca", err)
		}
		defer result.Release()
		updates, err := wincom.GetObject(result, "Updates")
		if err != nil {
			return err
		}
		defer updates.Release()
		n, err := wincom.GetInt(updates, "Count")
		if err != nil {
			return err
		}
		found := make([]wuaTarget, 0, n)
		objs := make([]*ole.IDispatch, 0, n)
		defer func() {
			for _, o := range objs {
				o.Release()
			}
		}()
		for i := 0; i < int(n); i++ {
			u, err := wincom.GetObject(updates, "Item", i)
			if err != nil {
				return err
			}
			objs = append(objs, u)
			t, err := readTarget(u)
			if err != nil {
				return fmt.Errorf("atualização %d: %w", i, err)
			}
			found = append(found, t)
		}

		// 2. Conferência: tudo o que foi pedido, com o KB pedido.
		todo, already, err := planWinUpdate(req, found)
		if err != nil {
			return err
		}
		pick := func(idx []int) []wuaTarget {
			out := make([]wuaTarget, len(idx))
			for i, j := range idx {
				out[i] = found[j]
			}
			return out
		}
		targets, done := pick(todo), pick(already)
		if len(targets) == 0 {
			detail, err = installResult(nil, done, nil, false)
			return err
		}

		coll, err := wincom.CreateDispatch("Microsoft.Update.UpdateColl")
		if err != nil {
			return err
		}
		defer coll.Release()
		for _, j := range todo {
			// Sem aceitar a licença, o WUA recusa instalar; quem aprovou foi o operador.
			if ok, err := wincom.GetBool(objs[j], "EulaAccepted"); err == nil && !ok {
				if _, err := wincom.Call(objs[j], "AcceptEula"); err != nil {
					return comFailure("aceitar a licença", err)
				}
			}
			if _, err := wincom.Call(coll, "Add", objs[j]); err != nil {
				return err
			}
		}

		installer, err := wincom.CallObject(session, "CreateUpdateInstaller")
		if err != nil {
			return err
		}
		defer installer.Release()
		if busy, err := wincom.GetBool(installer, "IsBusy"); err == nil && busy {
			return fmt.Errorf("%w: outra instalação do Windows Update em andamento", jobs.ErrRetry)
		}

		// 3. Download.
		downloader, err := wincom.CallObject(session, "CreateUpdateDownloader")
		if err != nil {
			return err
		}
		defer downloader.Release()
		if err := wincom.Put(downloader, "Updates", coll); err != nil {
			return err
		}
		dres, err := wincom.CallObject(downloader, "Download")
		if err != nil {
			return comFailure("download", err)
		}
		dops, err := readOps(dres, len(targets))
		dres.Release()
		if err != nil {
			return err
		}
		if err := downloadResult(targets, dops); err != nil {
			return err
		}

		// 4. Instalação, sem interface (o serviço roda como SYSTEM, sem ninguém para responder).
		if err := wincom.Put(installer, "Updates", coll); err != nil {
			return err
		}
		_ = wincom.Put(installer, "ForceQuiet", true) // IUpdateInstaller2; sem ela, segue igual
		ires, err := wincom.CallObject(installer, "Install")
		if err != nil {
			return comFailure("instalação", err)
		}
		defer ires.Release()
		reboot, err := wincom.GetBool(ires, "RebootRequired")
		if err != nil {
			return err
		}
		iops, err := readOps(ires, len(targets))
		if err != nil {
			return err
		}
		detail, err = installResult(targets, done, iops, reboot)
		return err
	})
	if errors.Is(err, context.DeadlineExceeded) {
		return "", fmt.Errorf("passou de %v; o Windows Update pode terminar sozinho, e a próxima busca mostra o resultado", winUpdateTimeout)
	}
	if err != nil && ctx.Err() != nil {
		return "", err
	}
	return detail, err
}

// readTarget lê o que a conferência usa de um IUpdate.
func readTarget(u *ole.IDispatch) (wuaTarget, error) {
	var t wuaTarget
	var err error
	if t.Title, err = wincom.GetString(u, "Title"); err != nil {
		return t, err
	}
	identity, err := wincom.GetObject(u, "Identity")
	if err != nil {
		return t, err
	}
	t.UpdateID, err = wincom.GetString(identity, "UpdateID")
	identity.Release()
	if err != nil {
		return t, err
	}
	if t.KBs, err = wincom.Strings(u, "KBArticleIDs"); err != nil {
		return t, err
	}
	t.Installed, err = wincom.GetBool(u, "IsInstalled")
	return t, err
}

// readOps lê o resultado de cada atualização (GetUpdateResult) de um download ou de uma
// instalação, na ordem da coleção.
func readOps(res *ole.IDispatch, n int) ([]wuaOp, error) {
	ops := make([]wuaOp, n)
	for i := range n {
		r, err := wincom.CallObject(res, "GetUpdateResult", i)
		if err != nil {
			return nil, err
		}
		code, err := wincom.GetInt(r, "ResultCode")
		if err == nil {
			ops[i].ResultCode = int(code)
			ops[i].HResult, err = wincom.GetInt(r, "HResult")
		}
		if err == nil {
			// Só a instalação tem RebootRequired; no download a leitura falha e fica false.
			ops[i].RebootRequired, _ = wincom.GetBool(r, "RebootRequired")
		}
		r.Release()
		if err != nil {
			return nil, err
		}
	}
	return ops, nil
}
