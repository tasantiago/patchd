package patch

import (
	"context"
	"fmt"
	"time"

	ole "github.com/go-ole/go-ole"

	"github.com/tasantiago/patchd/internal/platform"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/wincom"
)

// Critérios de busca: aplicável e não instalada. A busca online também ignora as ocultas.
const (
	onlineCriteria  = "IsInstalled=0 and IsHidden=0"
	offlineCriteria = "IsInstalled=0"
)

// ssOthers: o searcher consulta o serviço indicado em ServiceID (o catálogo registrado).
const ssOthers = 3

// wuaScanner consulta o Windows Update Agent via COM.
type wuaScanner struct{}

// New devolve o scanner do SO atual.
func New(run platform.Runner) Scanner { return wuaScanner{} }

// Scan executa a busca na fonte da política e, se houver catálogo, a busca offline.
func (wuaScanner) Scan(ctx context.Context, opts Options) []protocol.ScanResult {
	results := []protocol.ScanResult{search(ctx, SourceWUADefault, onlineCriteria, nil)}

	if opts.OfflineCatalog != "" {
		sum, mod, err := catalogInfo(opts.OfflineCatalog)
		var r protocol.ScanResult
		if err != nil {
			r = protocol.ScanResult{Source: SourceWUAOffline, Error: err.Error()}
		} else {
			r = search(ctx, SourceWUAOffline, offlineCriteria, registerCatalog(opts.OfflineCatalog))
		}
		r.CatalogSHA256 = sum
		if !mod.IsZero() {
			r.CatalogModifiedAt = &mod
		}
		results = append(results, r)
	}
	return results
}

// search executa uma busca e mede o tempo.
func search(ctx context.Context, source, criteria string, configure func(*ole.IDispatch) (func(), error)) protocol.ScanResult {
	start := time.Now()
	raw, rc, err := runSearch(ctx, criteria, configure)
	return scanResult(source, raw, rc, time.Since(start), err)
}

// newSearcher cria a sessão (identificada como patchd-agent) e o searcher.
// A função devolvida libera os dois objetos.
func newSearcher() (*ole.IDispatch, func(), error) {
	session, err := wincom.CreateDispatch("Microsoft.Update.Session")
	if err != nil {
		return nil, nil, err
	}
	// Aparece no histórico como quem pediu as operações feitas por esta sessão.
	if err := wincom.Put(session, "ClientApplicationID", "patchd-agent"); err != nil {
		session.Release()
		return nil, nil, err
	}
	searcher, err := wincom.CallObject(session, "CreateUpdateSearcher")
	if err != nil {
		session.Release()
		return nil, nil, err
	}
	return searcher, func() { searcher.Release(); session.Release() }, nil
}

// registerCatalog registra o wsusscn2.cab como serviço de varredura, aponta o searcher
// para ele e devolve a limpeza, que remove o serviço: sem ela, cada execução deixaria
// um serviço órfão registrado no Windows. Registrar exige administrador.
func registerCatalog(cab string) func(*ole.IDispatch) (func(), error) {
	return func(searcher *ole.IDispatch) (func(), error) {
		mgr, err := wincom.CreateDispatch("Microsoft.Update.ServiceManager")
		if err != nil {
			return nil, err
		}
		// Terceiro argumento como no exemplo da documentação da Microsoft.
		svc, err := wincom.CallObject(mgr, "AddScanPackageService", "patchd offline", cab, 1)
		if err != nil {
			mgr.Release()
			return nil, fmt.Errorf("registrar catálogo offline: %w", err)
		}
		id, err := wincom.GetString(svc, "ServiceID")
		svc.Release()
		if err != nil {
			mgr.Release()
			return nil, err
		}

		cleanup := func() {
			_, _ = wincom.Call(mgr, "RemoveService", id)
			mgr.Release()
		}
		if err := wincom.Put(searcher, "ServerSelection", ssOthers); err != nil {
			cleanup()
			return nil, err
		}
		if err := wincom.Put(searcher, "ServiceID", id); err != nil {
			cleanup()
			return nil, err
		}
		return cleanup, nil
	}
}

// runSearch cria sessão e searcher, aplica configure (se houver), busca e lê as atualizações.
func runSearch(ctx context.Context, criteria string, configure func(*ole.IDispatch) (func(), error)) ([]wuaUpdate, int, error) {
	var raw []wuaUpdate
	var resultCode int64

	err := wincom.Do(ctx, func() error {
		searcher, release, err := newSearcher()
		if err != nil {
			return err
		}
		defer release()

		if configure != nil {
			cleanup, err := configure(searcher)
			if err != nil {
				return err
			}
			// Registrado depois do release: roda antes dele.
			defer cleanup()
		}

		result, err := wincom.CallObject(searcher, "Search", criteria)
		if err != nil {
			return fmt.Errorf("busca no WUA: %w", err)
		}
		defer result.Release()

		if resultCode, err = wincom.GetInt(result, "ResultCode"); err != nil {
			return err
		}
		updates, err := wincom.GetObject(result, "Updates")
		if err != nil {
			return err
		}
		defer updates.Release()

		n, err := wincom.GetInt(updates, "Count")
		if err != nil {
			return err
		}
		for i := 0; i < int(n); i++ {
			u, err := wincom.GetObject(updates, "Item", i)
			if err != nil {
				return err
			}
			w, err := readUpdate(u)
			u.Release()
			if err != nil {
				return fmt.Errorf("atualização %d: %w", i, err)
			}
			raw = append(raw, w)
		}
		return nil
	})
	return raw, int(resultCode), err
}

// readUpdate lê os campos de um IUpdate.
func readUpdate(u *ole.IDispatch) (wuaUpdate, error) {
	var w wuaUpdate
	var err error

	if w.Title, err = wincom.GetString(u, "Title"); err != nil {
		return w, err
	}
	identity, err := wincom.GetObject(u, "Identity")
	if err != nil {
		return w, err
	}
	w.UpdateID, err = wincom.GetString(identity, "UpdateID")
	if err == nil {
		var rev int64
		rev, err = wincom.GetInt(identity, "RevisionNumber")
		w.Revision = int(rev)
	}
	identity.Release()
	if err != nil {
		return w, err
	}

	if w.KBs, err = wincom.Strings(u, "KBArticleIDs"); err != nil {
		return w, err
	}
	if w.CVEs, err = wincom.Strings(u, "CveIDs"); err != nil {
		return w, err
	}
	if w.Severity, err = wincom.GetString(u, "MsrcSeverity"); err != nil {
		return w, err
	}
	typ, err := wincom.GetInt(u, "Type")
	if err != nil {
		return w, err
	}
	w.Type = int(typ)
	if w.Released, err = wincom.GetTime(u, "LastDeploymentChangeTime"); err != nil {
		return w, err
	}
	if w.Downloaded, err = wincom.GetBool(u, "IsDownloaded"); err != nil {
		return w, err
	}

	cats, err := wincom.GetObject(u, "Categories")
	if err != nil {
		return w, err
	}
	defer cats.Release()
	n, err := wincom.GetInt(cats, "Count")
	if err != nil {
		return w, err
	}
	for i := 0; i < int(n); i++ {
		c, err := wincom.GetObject(cats, "Item", i)
		if err != nil {
			return w, err
		}
		var cat wuaCategory
		cat.ID, err = wincom.GetString(c, "CategoryID")
		if err == nil {
			cat.Name, err = wincom.GetString(c, "Name")
		}
		if err == nil {
			cat.Type, err = wincom.GetString(c, "Type")
		}
		c.Release()
		if err != nil {
			return w, err
		}
		w.Categories = append(w.Categories, cat)
	}
	return w, nil
}

// History lê as últimas max entradas do histórico do WUA.
func (wuaScanner) History(ctx context.Context, max int) ([]protocol.UpdateHistoryEntry, error) {
	var raw []wuaHistory

	err := wincom.Do(ctx, func() error {
		searcher, release, err := newSearcher()
		if err != nil {
			return err
		}
		defer release()

		total, err := wincom.CallInt(searcher, "GetTotalHistoryCount")
		if err != nil {
			return err
		}
		n := min(int(total), max)
		if n == 0 {
			return nil
		}
		entries, err := wincom.CallObject(searcher, "QueryHistory", 0, n)
		if err != nil {
			return err
		}
		defer entries.Release()

		count, err := wincom.GetInt(entries, "Count")
		if err != nil {
			return err
		}
		for i := 0; i < int(count); i++ {
			e, err := wincom.GetObject(entries, "Item", i)
			if err != nil {
				return err
			}
			h, err := readHistory(e)
			e.Release()
			if err != nil {
				return fmt.Errorf("histórico %d: %w", i, err)
			}
			raw = append(raw, h)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	list := make([]protocol.UpdateHistoryEntry, 0, len(raw))
	for _, h := range raw {
		list = append(list, toHistory(h))
	}
	return list, nil
}

// readHistory lê os campos de um IUpdateHistoryEntry.
func readHistory(e *ole.IDispatch) (wuaHistory, error) {
	var h wuaHistory
	var err error
	var n int64

	if h.Date, err = wincom.GetTime(e, "Date"); err != nil {
		return h, err
	}
	if n, err = wincom.GetInt(e, "Operation"); err != nil {
		return h, err
	}
	h.Operation = int(n)
	if n, err = wincom.GetInt(e, "ResultCode"); err != nil {
		return h, err
	}
	h.ResultCode = int(n)
	if h.HResult, err = wincom.GetInt(e, "HResult"); err != nil {
		return h, err
	}
	if h.Title, err = wincom.GetString(e, "Title"); err != nil {
		return h, err
	}
	if h.ClientApplicationID, err = wincom.GetString(e, "ClientApplicationID"); err != nil {
		return h, err
	}
	if n, err = wincom.GetInt(e, "ServerSelection"); err != nil {
		return h, err
	}
	h.ServerSelection = int(n)

	identity, err := wincom.GetObject(e, "UpdateIdentity")
	if err != nil {
		return h, err
	}
	defer identity.Release()
	if h.UpdateID, err = wincom.GetString(identity, "UpdateID"); err != nil {
		return h, err
	}
	if n, err = wincom.GetInt(identity, "RevisionNumber"); err != nil {
		return h, err
	}
	h.Revision = int(n)
	return h, nil
}
