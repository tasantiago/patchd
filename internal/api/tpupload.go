package api

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tasantiago/patchd/internal/httpx"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/pkgcache"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/tpupload"
	"github.com/tasantiago/patchd/internal/winget"
)

// Instalador próprio (internal/tpupload): na página do programa, o admin envia o .msi ou
// .exe de uma versão. Com "aprovar", a versão já vale para a Central, a padronização e as
// campanhas; com "substituir", as versões anteriores saem (a aprovação é revogada e o
// instalador apagado), como pede o caso de quem troca o instalador a cada atualização (o
// IRPF do ano, por exemplo). Uma versão anterior com job em aberto ou campanha não encerrada
// fica até eles acabarem, com a aprovação já revogada.

const (
	// uploadIdle: sem nenhum byte por esse tempo, o envio cai (o mesmo critério dos downloads).
	uploadIdle = 2 * time.Minute
	// uploadField: o maior campo de texto do formulário de envio.
	uploadField = 4 << 10
)

// uploadForm: os campos de texto, que vêm antes do arquivo no formulário.
type uploadForm struct {
	Version, Arch, Args, Note string
	Approve, Replace          bool
}

// readUploadField lê um campo de texto do multipart, com teto e texto válido.
func readUploadField(p io.Reader) (string, error) {
	b, err := io.ReadAll(io.LimitReader(p, uploadField+1))
	switch {
	case err != nil:
		return "", err
	case len(b) > uploadField:
		return "", errors.New("campo grande demais")
	case !utf8.Valid(b) || slices.Contains(b, 0):
		return "", errFormText
	}
	return strings.TrimSpace(string(b)), nil
}

// uploadInstallerAction: POST /painel/patch/terceiros/enviar?app=ID (multipart). Os campos de
// texto vêm antes do arquivo (a ordem do formulário): o arquivo é gravado direto no cache,
// em streaming, já com a versão e os argumentos conferidos.
func (a *api) uploadInstallerAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	app := r.URL.Query().Get("app")
	rule := a.installRule(app)
	if rule == nil || !rule.Uploaded() {
		http.Redirect(w, r, "/painel/patch?aba=terceiros", http.StatusSeeOther)
		return
	}
	back := tpProgramPath(app)
	fail := func(msg string) {
		http.Redirect(w, r, back+"&"+url.Values{"erro": {"envio"}, "motivo": {shortError(msg, 300)}}.Encode(), http.StatusSeeOther)
	}
	if a.contentDir == "" {
		fail("o servidor não guarda instaladores (PATCHD_CONTENT_DIR)")
		return
	}
	httpx.ExtendReads(w, r, uploadIdle)
	r.Body = http.MaxBytesReader(w, r.Body, tpupload.MaxSize+(1<<20))
	mr, err := r.MultipartReader()
	if err != nil {
		fail("o formulário não veio como envio de arquivo")
		return
	}
	var f uploadForm
	var pk *store.ThirdPartyPackage
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			fail("o envio foi interrompido: " + err.Error())
			return
		}
		if part.FormName() == "arquivo" {
			if pk != nil || part.FileName() == "" {
				_ = part.Close()
				continue
			}
			in := tpupload.Input{Version: f.Version, Arch: f.Arch, Args: f.Args, FileName: part.FileName()}
			saved, err := tpupload.Save(r.Context(), a.store, a.packagesDir(), *rule, in, part)
			_ = part.Close()
			var pr *tpupload.Problem
			switch {
			case errors.As(err, &pr):
				fail(pr.Msg)
				return
			case err != nil:
				if _, big := errors.AsType[*http.MaxBytesError](err); big {
					fail("o arquivo passa de " + pkgcache.Bytes(tpupload.MaxSize))
					return
				}
				a.internalError(w, r, "guardar o instalador", err)
				return
			}
			pk = &saved
			continue
		}
		v, err := readUploadField(part)
		_ = part.Close()
		if err != nil {
			fail("campo " + part.FormName() + ": " + err.Error())
			return
		}
		switch part.FormName() {
		case "versao":
			f.Version = v
		case "arquitetura":
			f.Arch = v
		case "argumentos":
			f.Args = v
		case "nota":
			f.Note = v
		case "aprovar":
			f.Approve = v == "1"
		case "substituir":
			f.Replace = v == "1"
		}
	}
	if pk == nil {
		fail("escolha o arquivo do instalador (.msi ou .exe)")
		return
	}
	if len([]rune(f.Note)) > maxNoteLen {
		f.Note = string([]rune(f.Note)[:maxNoteLen])
	}
	a.logger.Info("instalador próprio enviado", "app", app, "version", pk.Version, "arch", pk.Architecture, "sha256", pk.SHA256[:12],
		"size", pk.Size, "username", s.Username, "request_id", RequestID(r.Context()))
	a.audit(r, s, "terceiros.enviar", app, fmt.Sprintf("%s · %s · %s · %s · sha256 %s… · argumentos %q", pk.Version, pk.Type, pk.Architecture,
		pkgcache.Bytes(pk.Size), pk.SHA256[:12], pk.Arguments))
	q := url.Values{"enviado": {pk.Version}}
	if f.Approve {
		note := f.Note
		if note == "" {
			note = "instalador próprio enviado pelo painel"
		}
		created, err := a.store.ApproveThirdParty(r.Context(), store.ThirdPartyApproval{AppID: app, Version: pk.Version, ApprovedBy: s.Username, Note: note})
		if err != nil {
			a.internalError(w, r, "aprovar a versão", err)
			return
		}
		if created {
			a.audit(r, s, "terceiros.aprovar", app, pk.Version+" · "+note)
		}
		q.Set("aprovada", "1")
	}
	if f.Replace {
		gone, kept, err := a.replaceVersions(r, s, app, pk.Version)
		if err != nil {
			a.internalError(w, r, "substituir as versões anteriores", err)
			return
		}
		if len(gone) > 0 {
			q.Set("substituidas", strings.Join(gone, ", "))
		}
		if len(kept) > 0 {
			q.Set("ficaram", strings.Join(kept, ", "))
		}
		// A referência é a mais nova que ficou (a enviada pode ser mais antiga: a volta atrás).
		if err := tpupload.SaveReference(r.Context(), a.store, *rule); err != nil {
			a.logger.Warn("instalador próprio: versão de referência não atualizada", "app", app, "error", err)
		}
	}
	http.Redirect(w, r, back+"&"+q.Encode(), http.StatusSeeOther)
}

// replaceVersions tira as outras versões do programa: revoga a aprovação e apaga os
// instaladores. A que tem job em aberto ou campanha não encerrada fica (sem aprovação), com o
// motivo em kept.
func (a *api) replaceVersions(r *http.Request, s panel.Session, app, keep string) (gone, kept []string, err error) {
	pkgs, err := a.store.ThirdPartyPackages(r.Context())
	if err != nil {
		return nil, nil, err
	}
	var versions []string
	for _, p := range pkgs {
		if p.AppID == app && p.Version != keep && !slices.Contains(versions, p.Version) {
			versions = append(versions, p.Version)
		}
	}
	slices.SortFunc(versions, compareTPVersion)
	for _, v := range versions {
		revoked, err := a.store.RevokeThirdParty(r.Context(), app, v)
		if err != nil {
			return gone, kept, err
		}
		if revoked {
			a.audit(r, s, "terceiros.revogar", app, v+" · substituída pelo instalador "+keep)
		}
		removed, err := a.store.DeleteThirdPartyVersion(r.Context(), app, v)
		var inUse *store.PackageInUseError
		switch {
		case errors.As(err, &inUse):
			kept = append(kept, fmt.Sprintf("%s (em uso: %d job(s), %d campanha(s))", v, inUse.Jobs, inUse.Campaigns))
			continue
		case err != nil:
			return gone, kept, err
		}
		freed := a.removePackageFiles(r, removed)
		a.logger.Info("versão de instalador substituída", "app", app, "version", v, "by", keep, "bytes_freed", freed,
			"username", s.Username, "request_id", RequestID(r.Context()))
		a.audit(r, s, "terceiros.excluir", app, v+" · substituída pelo instalador "+keep)
		gone = append(gone, v)
	}
	return gone, kept, nil
}

// removePackageFiles apaga do cache os arquivos dos instaladores que saíram do registro,
// menos os que outro registro ainda usa (o mesmo SHA-256). Devolve os bytes liberados.
func (a *api) removePackageFiles(r *http.Request, gone []store.ThirdPartyPackage) int64 {
	rest, err := a.store.ThirdPartyPackages(r.Context())
	if err != nil {
		a.logger.Warn("instaladores excluídos do registro; os arquivos ficaram (a lista falhou)", "error", err)
		return 0
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
		path := filepath.Join(a.packagesDir(), pkgcache.FileName(g.SHA256, g.MSI()))
		switch err := os.Remove(path); {
		case err == nil:
			freed += g.Size
		case !errors.Is(err, fs.ErrNotExist):
			// O registro já saiu: o arquivo órfão não é oferecido a ninguém; fica o aviso.
			a.logger.Warn("instalador excluído do registro, mas o arquivo ficou", "file", filepath.Base(path), "error", err)
		}
	}
	return freed
}

// uploadDefaults: os argumentos para o formulário (os do envio mais recente, ou o padrão do tipo).
func uploadDefaults(installType string, pkgs []store.ThirdPartyPackage, app string) (args, arch string) {
	var newest *store.ThirdPartyPackage
	for i := range pkgs {
		p := &pkgs[i]
		if p.AppID == app && (newest == nil || compareTPVersion(p.Version, newest.Version) > 0) {
			newest = p
		}
	}
	if newest != nil {
		return newest.Arguments, newest.Architecture
	}
	return winget.SilentDefault(installType), "x64"
}
