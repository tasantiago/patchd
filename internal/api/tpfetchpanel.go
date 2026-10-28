package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/pkgcache"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
)

// Instaladores de terceiros pelo painel (Aula 23.3): o admin baixa uma versão (a mais nova do
// winget, ou a que digitar) e exclui as que não servem mais, sem a linha de comando do
// servidor. O download é o mesmo do "patchd-server package fetch" (internal/tpfetch), roda em
// segundo plano no servidor, um por vez, e a página mostra o andamento.

// PackageFetcher baixa, confere e registra o instalador da regra (versão vazia: a mais nova),
// escrevendo o andamento em out. O main liga o de verdade; os testes, um falso.
type PackageFetcher func(ctx context.Context, rule thirdparty.App, version string, out io.Writer) bool

// WithPackageFetch liga o botão Baixar (Aula 23.3). Sem ele (sem pasta de conteúdo ou sem
// PostgreSQL), o painel continua indicando a linha de comando.
func WithPackageFetch(f PackageFetcher) Option { return func(a *api) { a.tpFetch = f } }

// fetchTimeout: o teto de um download; o do package fetch para todos é 2 h, aqui é um só.
const fetchTimeout = time.Hour

// fetchMaxLines: as últimas linhas do andamento que a página mostra.
const fetchMaxLines = 30

// tpVersionPattern: a versão digitada, como o winget a escreve (26.04, 155.0.8059.40, 1.2.3-beta).
var tpVersionPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._+-]{0,63}$`)

// tpFetchStatus: um download, em andamento ou terminado.
type tpFetchStatus struct {
	App, Version, By  string // Version vazia: a mais nova
	Started, Finished time.Time
	Running, OK       bool
	Lines             []string
	partial           []byte // o pedaço de linha ainda sem quebra
}

// tpFetchBoard: o download em andamento (um por vez) e o último de cada programa. Só na
// memória: reiniciar o servidor interrompe o download e esquece o andamento (o que já foi
// registrado fica).
type tpFetchBoard struct {
	mu   sync.Mutex
	busy string
	last map[string]*tpFetchStatus
}

// start marca o programa como baixando; false: já há um download em andamento.
func (b *tpFetchBoard) start(st *tpFetchStatus) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.busy != "" {
		return false
	}
	if b.last == nil {
		b.last = map[string]*tpFetchStatus{}
	}
	b.busy = st.App
	b.last[st.App] = st
	return true
}

func (b *tpFetchBoard) finish(st *tpFetchStatus, ok bool, at time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(st.partial) > 0 {
		st.addLine(string(st.partial))
		st.partial = nil
	}
	st.Running, st.OK, st.Finished = false, ok, at
	b.busy = ""
}

// busyWith: o programa baixando agora (vazio: nenhum).
func (b *tpFetchBoard) busyWith() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.busy
}

// view: uma cópia do último download do programa e o programa ocupado, se for outro.
func (b *tpFetchBoard) view(app string) (*tpFetchStatus, string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	other := ""
	if b.busy != app {
		other = b.busy
	}
	st, ok := b.last[app]
	if !ok {
		return nil, other
	}
	c := *st
	c.Lines = append([]string(nil), st.Lines...)
	c.partial = nil
	return &c, other
}

func (st *tpFetchStatus) addLine(l string) {
	l = strings.TrimRight(l, "\r ")
	if strings.TrimSpace(l) == "" {
		return
	}
	if len(l) > 300 {
		l = l[:300]
		for !utf8.ValidString(l) {
			l = l[:len(l)-1]
		}
		l += "…"
	}
	st.Lines = append(st.Lines, l)
	if len(st.Lines) > fetchMaxLines {
		st.Lines = st.Lines[len(st.Lines)-fetchMaxLines:]
	}
}

// boardWriter: o io.Writer do andamento, linha a linha, sob o mutex do quadro.
type boardWriter struct {
	b  *tpFetchBoard
	st *tpFetchStatus
}

func (w boardWriter) Write(p []byte) (int, error) {
	w.b.mu.Lock()
	defer w.b.mu.Unlock()
	data := append(w.st.partial, p...)
	for {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			break
		}
		w.st.addLine(string(data[:i]))
		data = data[i+1:]
	}
	w.st.partial = append([]byte(nil), data...)
	return len(p), nil
}

// fetchThirdPartyAction: POST /painel/patch/terceiros/baixar (app, versao opcional).
func (a *api) fetchThirdPartyAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	app, ver := strings.TrimSpace(r.PostFormValue("app")), strings.TrimSpace(r.PostFormValue("versao"))
	rule := a.installRule(app)
	if rule == nil || a.tpFetch == nil {
		http.Redirect(w, r, "/painel/patch?aba=terceiros", http.StatusSeeOther)
		return
	}
	back := tpProgramPath(app)
	if ver != "" && !tpVersionPattern.MatchString(ver) {
		http.Redirect(w, r, back+"&erro=versao", http.StatusSeeOther)
		return
	}
	st := &tpFetchStatus{App: app, Version: ver, By: s.Username, Started: a.now(), Running: true}
	if !a.fetches.start(st) {
		http.Redirect(w, r, back+"&erro=baixando", http.StatusSeeOther)
		return
	}
	pedido := ver
	if pedido == "" {
		pedido = "a mais nova"
	}
	a.logger.Info("download de instalador pedido pelo painel", "app", app, "version", pedido, "username", s.Username,
		"request_id", RequestID(r.Context()))
	a.audit(r, s, "terceiros.baixar", app, pedido)
	// WithoutCancel: o download continua depois da resposta (e se o admin fechar a página),
	// com o prazo próprio.
	base := context.WithoutCancel(r.Context())
	go func(rule thirdparty.App) {
		ctx, cancel := context.WithTimeout(base, fetchTimeout)
		defer cancel()
		ok := a.tpFetch(ctx, rule, ver, boardWriter{b: &a.fetches, st: st})
		a.fetches.finish(st, ok, a.now())
		a.logger.Info("download de instalador pelo painel terminado", "app", rule.ID, "version", pedido, "ok", ok,
			"username", st.By)
	}(*rule)
	http.Redirect(w, r, back+"&baixando=1", http.StatusSeeOther)
}

// deleteThirdPartyAction: POST /painel/patch/terceiros/excluir (app, version). Apaga os
// registros da versão e os arquivos que nenhum outro registro usa (o mesmo SHA-256 pode
// servir a duas regras).
func (a *api) deleteThirdPartyAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	app, ver := strings.TrimSpace(r.PostFormValue("app")), strings.TrimSpace(r.PostFormValue("version"))
	if app == "" || ver == "" || len(app) > 64 || len(ver) > 64 || a.contentDir == "" {
		http.Redirect(w, r, "/painel/patch?aba=terceiros", http.StatusSeeOther)
		return
	}
	back := tpProgramPath(app)
	// O arquivo pode estar sendo escrito agora.
	if a.fetches.busyWith() == app {
		http.Redirect(w, r, back+"&erro=baixando", http.StatusSeeOther)
		return
	}
	gone, err := a.store.DeleteThirdPartyVersion(r.Context(), app, ver)
	var inUse *store.PackageInUseError
	switch {
	case errors.Is(err, store.ErrPackageApproved):
		http.Redirect(w, r, back+"&erro=aprovada", http.StatusSeeOther)
		return
	case errors.As(err, &inUse):
		http.Redirect(w, r, back+"&erro=em-uso", http.StatusSeeOther)
		return
	case err != nil:
		a.internalError(w, r, "excluir a versão", err)
		return
	case len(gone) == 0:
		http.Redirect(w, r, back+"&erro=nada", http.StatusSeeOther)
		return
	}
	rest, err := a.store.ThirdPartyPackages(r.Context())
	if err != nil {
		a.internalError(w, r, "listar instaladores", err)
		return
	}
	used := map[string]bool{}
	for _, p := range rest {
		used[p.SHA256] = true
	}
	var freed int64
	for _, g := range gone {
		if used[g.SHA256] {
			continue
		}
		path := filepath.Join(a.contentDir, "pacotes", pkgcache.FileName(g.SHA256, g.MSI()))
		switch err := os.Remove(path); {
		case err == nil:
			freed += g.Size
		case !errors.Is(err, fs.ErrNotExist):
			// O registro já saiu: o arquivo órfão não é oferecido a ninguém; fica o aviso.
			a.logger.Warn("instalador excluído do registro, mas o arquivo ficou", "file", filepath.Base(path), "error", err)
		}
	}
	a.logger.Info("versão de instalador de terceiros excluída", "app", app, "version", ver, "installers", len(gone),
		"bytes_freed", freed, "username", s.Username, "request_id", RequestID(r.Context()))
	a.audit(r, s, "terceiros.excluir", app, ver)
	http.Redirect(w, r, back+"&excluido=1", http.StatusSeeOther)
}
