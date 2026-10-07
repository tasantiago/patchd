package api

import (
	"errors"
	"io/fs"
	"net/http"

	"github.com/tasantiago/patchd/internal/httpx"
	"github.com/tasantiago/patchd/internal/release"
	"github.com/tasantiago/patchd/internal/version"
)

// Option ajusta a API na criação.
type Option func(*api)

// WithReleases liga a atualização automática do agente (Aula 5.4): o check-in passa a
// oferecer a versão publicada na pasta, e os arquivos dela ficam disponíveis para as
// máquinas autenticadas. serverVersion é a versão deste servidor: uma versão do agente
// mais nova que o servidor nunca é oferecida (o servidor é atualizado antes dos agentes).
func WithReleases(dir release.Dir, serverVersion string) Option {
	return func(a *api) {
		a.releases = dir
		a.serverVersion = serverVersion
	}
}

// offeredVersion é a versão publicada que pode ser oferecida agora; "" quando nenhuma.
func (a *api) offeredVersion(r *http.Request) string {
	if a.releases == "" {
		return ""
	}
	v, err := a.releases.Current()
	if err != nil {
		a.logger.Warn("versão publicada do agente ilegível", "error", err, "request_id", RequestID(r.Context()))
		return ""
	}
	if v == "" || !agentNotNewerThanServer(v, a.serverVersion) {
		return ""
	}
	return v
}

// agentNotNewerThanServer: versões que não seguem o formato (um servidor "dev", por
// exemplo) nunca liberam a oferta.
func agentNotNewerThanServer(agent, server string) bool {
	c, err := version.CompareSemver(agent, server)
	return err == nil && c <= 0
}

// releaseFile entrega um arquivo da versão publicada: o manifesto, a assinatura ou um
// executável. Outras versões (não publicadas ou retiradas) não são servidas.
func (a *api) releaseFile(w http.ResponseWriter, r *http.Request, id string) {
	v, name := r.PathValue("version"), r.PathValue("file")
	if v == "" || v != a.offeredVersion(r) {
		writeError(w, http.StatusNotFound, "release_not_found", "versão não publicada")
		return
	}
	f, err := a.releases.Open(v, name)
	if errors.Is(err, fs.ErrNotExist) {
		writeError(w, http.StatusNotFound, "release_not_found", "arquivo não encontrado na versão")
		return
	}
	if err != nil {
		a.internalError(w, r, "abrir arquivo da versão", err)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, "release_not_found", "arquivo não encontrado na versão")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	a.logger.Info("download de versão do agente", "machine_id", id, "release", v, "file", name, "request_id", RequestID(r.Context()))
	http.ServeContent(httpx.ExtendWrites(w, downloadIdle), r, name, fi.ModTime(), f)
}
