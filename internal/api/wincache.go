package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"

	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/wucache"
)

// O cache das atualizações do Windows (docs/cache-windows.md): o agente pede os arquivos de
// uma atualização, o servidor os baixa da Microsoft uma vez e os entrega a todas as máquinas.

// WithWinCache liga o cache.
func WithWinCache(c *wucache.Cache) Option {
	return func(a *api) { a.winCache = c }
}

// winCacheEnsure: POST WinCachePath. Responde o estado de cada arquivo e começa a baixar o
// que falta; o agente pergunta de novo até ficar pronto.
func (a *api) winCacheEnsure(w http.ResponseWriter, r *http.Request, id string) {
	if a.winCache == nil {
		writeError(w, http.StatusNotFound, "not_found", "o servidor não tem o cache do Windows ligado")
		return
	}
	var req protocol.WinCacheRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if len(req.URLs) == 0 || len(req.URLs) > wucache.MaxURLs {
		writeError(w, http.StatusBadRequest, "invalid", "de 1 a 64 arquivos por pedido")
		return
	}
	resp := protocol.WinCacheResponse{Files: []protocol.WinCacheFile{}}
	for _, s := range a.winCache.Ensure(r.Context(), req.URLs) {
		resp.Files = append(resp.Files, protocol.WinCacheFile{URL: s.URL, Key: s.Key, State: s.State, Size: s.Size, Done: s.Done,
			SHA256: s.SHA256, Detail: s.Detail})
		if s.State == wucache.StateRefused {
			a.logger.Warn("cache do Windows: endereço recusado", "machine_id", id, "url", s.URL, "motivo", s.Detail,
				"request_id", RequestID(r.Context()))
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// winCacheFile: GET WinCachePath/{key}, com retomada (Range).
func (a *api) winCacheFile(w http.ResponseWriter, r *http.Request, id string) {
	key := r.PathValue("key")
	if a.winCache == nil || !wucache.ValidKey(key) {
		http.NotFound(w, r)
		return
	}
	f, m, err := a.winCache.Open(key)
	if errors.Is(err, os.ErrNotExist) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		a.internalError(w, r, "abrir arquivo do cache do Windows", err)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Patchd-SHA256", m.SHA256)
	http.ServeContent(w, r, key, m.Fetched, io.ReadSeeker(f))
}

// WinCacheKey: quem instala as atualizações do Windows pelo cache do servidor.
const WinCacheKey = "PATCHD_WUCACHE"

// Valores do WinCacheKey.
const (
	WinCacheOff   = "0"
	WinCachePilot = "piloto" // só o grupo de implantação piloto
	WinCacheAll   = "todos"
)

// WinCacheDaysKey: a limpeza do cache (o que nenhuma máquina pediu nesse prazo sai).
const WinCacheDaysKey = "PATCHD_WUCACHE_DAYS"

// WinCacheFor diz se o job do Windows desta máquina vai pelo cache: o cache existe, o
// parâmetro inclui a máquina e o agente dela sabe usar. O mesmo para o painel e as campanhas.
func WinCacheFor(ctx context.Context, mode string, st interface {
	MachineCapabilities(ctx context.Context, id string) ([]string, error)
	MachineRing(ctx context.Context, id string) (int, bool, error)
}, id string) (bool, error) {
	if mode != WinCachePilot && mode != WinCacheAll {
		return false, nil
	}
	caps, err := st.MachineCapabilities(ctx, id)
	if err != nil || !slices.Contains(caps, protocol.CapabilityWinCache) {
		return false, err
	}
	if mode == WinCachePilot {
		ring, ok, err := st.MachineRing(ctx, id)
		if err != nil || !ok || ring != store.PilotRing {
			return false, err
		}
	}
	return true, nil
}

// winCacheFor: o WinCacheFor com o parâmetro de agora.
func (a *api) winCacheFor(ctx context.Context, id string) (bool, error) {
	if a.winCache == nil || a.params == nil {
		return false, nil
	}
	return WinCacheFor(ctx, a.params.Value(WinCacheKey), a.store, id)
}

// winCacheView: o cartão do cache na aba Sistema da Configuração.
type winCacheView struct {
	Mode     string
	Dir      string
	Files    int
	Size     string
	Fetching []winCacheRow
	Recent   []winCacheRow
	More     int
	Days     string
}

type winCacheRow struct {
	Name, Size, When, Progress string
}

var winCacheModeLabel = map[string]string{
	WinCacheOff: "desligado: as máquinas seguem com o WSUS ou a Microsoft", WinCachePilot: "ligado só no grupo de implantação piloto",
	WinCacheAll: "ligado para todas as máquinas",
}

// winCacheCard monta o cartão (nil: o servidor está sem o cache).
func (a *api) winCacheCard() (*winCacheView, error) {
	if a.winCache == nil {
		return nil, nil
	}
	st, err := a.winCache.Stats()
	if err != nil {
		return nil, err
	}
	v := &winCacheView{Dir: a.winCache.Dir, Files: st.Files, Size: humanBytes(st.Bytes)}
	if a.params != nil {
		v.Mode = winCacheModeLabel[a.params.Value(WinCacheKey)]
		v.Days = a.params.Value(WinCacheDaysKey)
	}
	if v.Mode == "" {
		v.Mode = winCacheModeLabel[WinCacheOff]
	}
	for _, f := range st.Fetching {
		row := winCacheRow{Name: winCacheName(f.Key), Progress: humanBytes(f.Done)}
		if f.Size > 0 {
			row.Progress = fmt.Sprintf("%s de %s (%d%%)", humanBytes(f.Done), humanBytes(f.Size), f.Done*100/f.Size)
		}
		v.Fetching = append(v.Fetching, row)
	}
	for i, m := range st.Recent {
		if i == 10 {
			v.More = len(st.Recent) - 10
			break
		}
		v.Recent = append(v.Recent, winCacheRow{Name: winCacheName(m.Key), Size: humanBytes(m.Size), When: a.stamp(m.Used)})
	}
	return v, nil
}

// winCacheName: o nome do arquivo sem o prefixo da chave.
func winCacheName(key string) string {
	if _, name, ok := strings.Cut(key, "-"); ok {
		return name
	}
	return key
}

// humanBytes: o tamanho em B, KB, MB ou GB, com vírgula.
func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return strings.Replace(fmt.Sprintf("%.1f GB", float64(n)/(1<<30)), ".", ",", 1)
	case n >= 1<<20:
		return strings.Replace(fmt.Sprintf("%.1f MB", float64(n)/(1<<20)), ".", ",", 1)
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%d B", n)
}
