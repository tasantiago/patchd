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
func NewWinUpdater() WinUpdater { return NewWinUpdaterWith(nil) }

// NewWinUpdaterWith liga o job, com o cache do servidor (docs/cache-windows.md). nil: sem
// cache, e o job que o pede é recusado.
func NewWinUpdaterWith(c *WinCache) WinUpdater {
	return func(ctx context.Context, jobID int64, p jobs.WinUpdateParams) (string, error) {
		return installWindowsUpdates(ctx, p, c)
	}
}

// installWindowsUpdates faz, numa sessão COM identificada como patchd-agent (é o nome que o
// histórico do Windows Update mostra): busca as pendentes na fonte da política, separa os
// UpdateIDs pedidos, confere, aceita a licença, baixa e instala. Nunca reinicia.
func installWindowsUpdates(ctx context.Context, p jobs.WinUpdateParams, c *WinCache) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, winUpdateTimeout)
	defer cancel()
	if p.Cache {
		return installOnce(ctx, p, c)
	}
	detail, err := installOnce(ctx, p, nil)
	if !p.Fallback || p.Source != jobs.WinSourcePolicy || !policyDidNotDeliver(err) || ctx.Err() != nil {
		return detail, err
	}
	// A reserva no job: a política não entregou; o mesmo pedido, direto da Microsoft.
	if info := MicrosoftInfo(); info == nil || !info.Available {
		return detail, err
	}
	why := err.Error()
	mp := p
	mp.Source, mp.Fallback = jobs.WinSourceMicrosoft, false
	d2, err2 := installOnce(ctx, mp, nil)
	if err2 != nil {
		return "", fmt.Errorf("a fonte da política não entregou (%s); %w", why, err2)
	}
	return "a fonte da política não entregou (" + why + "); " + d2, nil
}

func installOnce(ctx context.Context, p jobs.WinUpdateParams, cache *WinCache) (string, error) {
	req := p.Updates
	// Pelo cache do servidor: a busca pelo catálogo offline (sem ele, na Microsoft), e os
	// arquivos do servidor. O agente sem o cache recusa antes de tocar no Windows Update.
	catalog := ""
	if p.Cache {
		if cache == nil || cache.Fetch == nil {
			return "", fmt.Errorf("%w: este agente não sabe usar o cache do servidor", jobs.ErrRefused)
		}
		if cache.Catalog != nil {
			catalog = cache.Catalog()
		}
		if catalog == "" {
			if info := MicrosoftInfo(); info == nil || !info.Available {
				return "", fmt.Errorf("%w: sem o catálogo offline (ainda não baixado do servidor) e sem a Microsoft para procurar a atualização", jobs.ErrRetry)
			}
		}
	}
	// Fonte reserva: direto da Microsoft. A máquina que bloqueia (GPO) recusa antes de tentar.
	if p.Source == jobs.WinSourceMicrosoft {
		if info := MicrosoftInfo(); info == nil || !info.Available {
			reason := "indisponível"
			if info != nil {
				reason = info.Reason
			}
			return "", fmt.Errorf("%w: a fonte Microsoft não serve nesta máquina: %s", jobs.ErrNotPending, reason)
		}
	}

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
		criteria := onlineCriteria
		switch {
		case p.Cache && catalog != "":
			// docs/cache-windows.md: o catálogo offline que o servidor distribui. Não depende
			// do WSUS nem da Microsoft; os endereços dos arquivos vêm dele.
			cleanup, err := registerCatalog(catalog)(searcher)
			if err != nil {
				return err
			}
			defer cleanup()
			criteria = offlineCriteria
		case p.Source == jobs.WinSourceMicrosoft, p.Cache:
			// ssWindowsUpdate: a busca (e o download do que ela trouxer) vai à Microsoft, não
			// ao WSUS da política.
			if err := wincom.Put(searcher, "ServerSelection", ssWindowsUpdate); err != nil {
				return err
			}
		}
		result, err := wincom.CallObject(searcher, "Search", criteria)
		if err != nil {
			return sourceError{comFailure("busca", err)}
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

		// 3. Os arquivos: do cache do servidor (entregues ao Windows Update, que confere cada
		// um contra a metadado assinado), ou o download do próprio Windows Update.
		if p.Cache {
			if err := copyFromCache(ctx, cache, objs, todo); err != nil {
				return err
			}
		} else if err := wuaDownload(session, coll, targets); err != nil {
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
	switch {
	case p.Cache && err != nil:
		err = fmt.Errorf("pelo cache do servidor: %w", err)
	case p.Cache:
		detail = "pelo cache do servidor: " + detail
	case p.Source == jobs.WinSourceMicrosoft && err != nil:
		err = fmt.Errorf("fonte Microsoft Update: %w", err)
	case p.Source == jobs.WinSourceMicrosoft:
		detail = "fonte Microsoft Update: " + detail
	}
	return detail, err
}

// wuaDownload: o download do próprio Windows Update (do WSUS ou da Microsoft).
func wuaDownload(session, coll *ole.IDispatch, targets []wuaTarget) error {
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
		return sourceError{comFailure("download", err)}
	}
	dops, err := readOps(dres, len(targets))
	dres.Release()
	if err != nil {
		return err
	}
	if err := downloadResult(targets, dops); err != nil {
		return sourceError{err}
	}
	return nil
}

// copyFromCache entrega ao Windows Update os arquivos das atualizações escolhidas, trazidos do
// cache do servidor: as partes de cada uma (BundledUpdates; sem partes, a própria), os
// endereços de cada parte (DownloadContents), o Fetch do agente, e o CopyToCache de cada parte
// (IUpdate2), que confere os arquivos contra a metadado assinado da atualização.
func copyFromCache(ctx context.Context, cache *WinCache, objs []*ole.IDispatch, todo []int) error {
	var parts []cachePart
	var partObjs []*ole.IDispatch
	defer func() {
		for _, o := range partObjs {
			o.Release()
		}
	}()
	for _, j := range todo {
		u := objs[j]
		bundled, err := wincom.GetObject(u, "BundledUpdates")
		if err != nil {
			return err
		}
		n, err := wincom.GetInt(bundled, "Count")
		if err != nil {
			bundled.Release()
			return err
		}
		if n == 0 {
			u.AddRef()
			partObjs = append(partObjs, u)
		}
		for i := 0; i < int(n); i++ {
			b, err := wincom.GetObject(bundled, "Item", i)
			if err != nil {
				bundled.Release()
				return err
			}
			partObjs = append(partObjs, b)
		}
		bundled.Release()
	}
	for _, o := range partObjs {
		urls, err := downloadURLs(o)
		if err != nil {
			return err
		}
		parts = append(parts, cachePart{URLs: urls})
	}
	all, err := cacheURLs(parts)
	if err != nil {
		return err
	}
	local, err := cache.Fetch(ctx, all)
	if err != nil {
		return err
	}
	files, err := cacheFiles(parts, local)
	if err != nil {
		return err
	}
	for i, o := range partObjs {
		if len(files[i]) == 0 {
			continue
		}
		coll, err := wincom.CreateDispatch("Microsoft.Update.StringColl")
		if err != nil {
			return err
		}
		for _, f := range files[i] {
			if _, err := wincom.Call(coll, "Add", f); err != nil {
				coll.Release()
				return err
			}
		}
		_, err = wincom.Call(o, "CopyToCache", coll)
		coll.Release()
		if err != nil {
			return comFailure("entregar os arquivos do cache ao Windows Update", err)
		}
	}
	return nil
}

// downloadURLs: os endereços dos arquivos de uma parte (IUpdate.DownloadContents).
func downloadURLs(u *ole.IDispatch) ([]string, error) {
	contents, err := wincom.GetObject(u, "DownloadContents")
	if err != nil {
		return nil, err
	}
	defer contents.Release()
	n, err := wincom.GetInt(contents, "Count")
	if err != nil {
		return nil, err
	}
	var out []string
	for i := 0; i < int(n); i++ {
		c, err := wincom.GetObject(contents, "Item", i)
		if err != nil {
			return nil, err
		}
		url, err := wincom.GetString(c, "DownloadUrl")
		c.Release()
		if err != nil {
			return nil, err
		}
		out = append(out, url)
	}
	return out, nil
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
