package api

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"

	"github.com/tasantiago/patchd/internal/catalog/wsusscan"
	"github.com/tasantiago/patchd/internal/protocol"
)

// ContentSource diz qual é a versão atual de um arquivo distribuído (a tabela content_files).
type ContentSource interface {
	ContentFile(ctx context.Context, name string) (wsusscan.File, bool, error)
}

// sha256Pattern: o SHA-256 em hexadecimal minúsculo, como o servidor anuncia.
var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// WithContent liga a distribuição do catálogo offline do Windows (Aula 6.6): o check-in
// anuncia o wsusscn2.cab atual, e as máquinas autenticadas o baixam de dir.
func WithContent(dir string, src ContentSource) Option {
	return func(a *api) {
		a.contentDir = dir
		a.content = src
	}
}

// offlineCatalog é o anúncio do check-in; nil quando o servidor não distribui o catálogo,
// ainda não o baixou ou o arquivo sumiu do disco (anunciar daria 404 em todos os agentes).
func (a *api) offlineCatalog(r *http.Request) *protocol.OfflineCatalog {
	if a.content == nil {
		return nil
	}
	f, ok, err := a.content.ContentFile(r.Context(), "wsusscn2")
	if err != nil {
		a.logger.Warn("catálogo offline: leitura da versão atual falhou; check-in sem o anúncio", "error", err, "request_id", RequestID(r.Context()))
		return nil
	}
	if !ok || !sha256Pattern.MatchString(f.SHA256) {
		return nil
	}
	st, err := os.Stat(filepath.Join(a.contentDir, wsusscan.FileName(f.SHA256)))
	if err != nil || st.Size() != f.Size {
		a.logger.Warn("catálogo offline: o arquivo atual não está no diretório de conteúdo; check-in sem o anúncio",
			"file", f.Name, "request_id", RequestID(r.Context()))
		return nil
	}
	return &protocol.OfflineCatalog{SHA256: f.SHA256, Size: f.Size, PublishedAt: f.LastModified}
}

// offlineCatalogFile entrega o wsusscn2.cab pelo SHA-256. Só existem no diretório o atual e o
// anterior (o anterior fica para quem estava no meio do download dele). O ETag é o próprio
// SHA-256: com ele, o If-Range do agente retoma do ponto certo, e um arquivo diferente
// nunca é emendado no pedaço já baixado.
func (a *api) offlineCatalogFile(w http.ResponseWriter, r *http.Request, id string) {
	sum := r.PathValue("sha256")
	if a.contentDir == "" || !sha256Pattern.MatchString(sum) {
		writeError(w, http.StatusNotFound, "content_not_found", "catálogo offline não encontrado")
		return
	}
	f, err := os.Open(filepath.Join(a.contentDir, wsusscan.FileName(sum)))
	if errors.Is(err, fs.ErrNotExist) {
		writeError(w, http.StatusNotFound, "content_not_found", "catálogo offline não encontrado (substituído por um mais novo?)")
		return
	}
	if err != nil {
		a.internalError(w, r, "abrir catálogo offline", err)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, "content_not_found", "catálogo offline não encontrado")
		return
	}
	w.Header().Set("Content-Type", "application/vnd.ms-cab-compressed")
	w.Header().Set("ETag", `"`+sum+`"`)
	a.logger.Info("download do catálogo offline", "machine_id", id, "sha256", sum[:16], "range", r.Header.Get("Range"),
		"request_id", RequestID(r.Context()))
	http.ServeContent(w, r, "", fi.ModTime(), f)
}
