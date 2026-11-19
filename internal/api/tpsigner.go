package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/tasantiago/patchd/internal/authsig"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/pkgcache"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
)

// Quem assinou cada instalador guardado, lido do próprio arquivo (internal/authsig, sem
// conferir: quem confere é o agente). A página do programa mostra, avisa quando o programa
// exige outra organização (o agente recusaria) e, nos programas incluídos pelo painel, oferece
// "Exigir esta assinatura": a regra passa a exigir o O= do arquivo.

// signerCache: o signatário de cada instalador, pelo SHA-256 (o arquivo não muda).
type signerCache struct {
	mu sync.Mutex
	m  map[string]pkgSigner
}

type pkgSigner struct {
	Signer authsig.Signer
	Note   string // "" com Signer; senão, por quê ("não assinado", "não deu para ler: …")
}

func (c *signerCache) get(sha string) (pkgSigner, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.m[sha]
	return s, ok
}

func (c *signerCache) put(sha string, s pkgSigner) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil || len(c.m) > 2000 {
		c.m = map[string]pkgSigner{}
	}
	c.m[sha] = s
}

const noSignatureNote = "não assinado"

// packageSigner: quem assinou o instalador guardado. ok false: sem o arquivo no servidor.
func (a *api) packageSigner(p store.ThirdPartyPackage) (pkgSigner, bool) {
	if s, ok := a.signers.get(p.SHA256); ok {
		return s, true
	}
	dir := a.packagesDir()
	if dir == "" {
		return pkgSigner{}, false
	}
	f, err := os.Open(filepath.Join(dir, pkgcache.FileName(p.SHA256, p.MSI())))
	if err != nil {
		return pkgSigner{}, false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return pkgSigner{}, false
	}
	var out pkgSigner
	s, err := authsig.Read(f, st.Size())
	switch {
	case errors.Is(err, authsig.ErrUnsigned):
		out.Note = noSignatureNote
	case err != nil:
		out.Note = "não deu para ler a assinatura: " + err.Error()
	case s.Organization == "":
		out.Note = "assinado por " + s.Subject + ", sem O="
	default:
		out.Signer = s
	}
	a.signers.put(p.SHA256, out)
	return out, true
}

// tpSignerNote: o aviso do cartão de instaladores: o programa exige uma organização e um
// instalador guardado tem outra (ou nenhuma), e o agente vai recusar. Adopt: o O= que o botão
// grava (só nos incluídos pelo painel), com o SHA-256 do arquivo de onde veio. O programa
// "sem assinatura" não ganha aviso: quem marcou deu o motivo, e uma assinatura que o servidor
// lê pode não valer no Windows (a do BirdID, de um certificado da Apple).
type tpSignerNote struct {
	Mismatch      bool
	Required      string // o que a regra exige ("" sem assinatura)
	Found         string // o O= do instalador (ou o motivo)
	Version, Type string
	AdoptSHA      string
	CanAdopt      bool
}

// signerNote: a primeira divergência entre a regra e os instaladores guardados, da versão mais
// nova para a mais antiga.
func signerNote(rule thirdparty.App, pkgs []store.ThirdPartyPackage, read func(store.ThirdPartyPackage) (pkgSigner, bool)) *tpSignerNote {
	list := slices.Clone(pkgs)
	slices.SortFunc(list, func(x, y store.ThirdPartyPackage) int { return compareTPVersion(y.Version, x.Version) })
	for _, p := range list {
		if p.AppID != rule.ID {
			continue
		}
		s, ok := read(p)
		if !ok || (s.Note != "" && s.Note != noSignatureNote) {
			continue
		}
		n := &tpSignerNote{Required: rule.Install.Signer, Version: p.Version, Type: p.Type, Found: s.Signer.Organization}
		switch {
		case rule.Install.Signer != "" && s.Note == noSignatureNote:
			n.Mismatch, n.Found = true, ""
			return n
		case rule.Install.Signer != "" && s.Signer.Organization != rule.Install.Signer:
			n.Mismatch, n.AdoptSHA = true, p.SHA256
			return n
		}
	}
	return nil
}

// adoptSignerAction: POST /painel/patch/terceiros/assinante (app, sha). O programa incluído
// pelo painel passa a exigir o O= do instalador guardado com aquele SHA-256 (lido do arquivo
// no servidor, não do formulário).
func (a *api) adoptSignerAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	app, sha := strings.TrimSpace(r.PostFormValue("app")), strings.TrimSpace(r.PostFormValue("sha"))
	back := tpProgramPath(app)
	if a.custom == nil || a.tpManifest == nil {
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	_, rules, err := a.customList(r.Context())
	if err != nil {
		a.internalError(w, r, "listar os programas incluídos", err)
		return
	}
	rule, ok := rules[app]
	if !ok || rule.Install == nil {
		http.Redirect(w, r, back+"&erro=assinante-regra", http.StatusSeeOther)
		return
	}
	pkgs, err := a.store.ThirdPartyPackages(r.Context())
	if err != nil {
		a.internalError(w, r, "listar instaladores", err)
		return
	}
	i := slices.IndexFunc(pkgs, func(p store.ThirdPartyPackage) bool { return p.AppID == app && p.SHA256 == sha })
	if i < 0 {
		http.Redirect(w, r, back+"&erro=assinante-arquivo", http.StatusSeeOther)
		return
	}
	sig, ok := a.packageSigner(pkgs[i])
	if !ok || sig.Signer.Organization == "" {
		http.Redirect(w, r, back+"&erro=assinante-arquivo", http.StatusSeeOther)
		return
	}
	before := rule.Install.Signer
	if before == "" {
		before = "sem assinatura (" + rule.Install.UnsignedReason + ")"
	}
	in := *rule.Install
	in.Signer, in.UnsignedReason = sig.Signer.Organization, ""
	rule.Install = &in
	data, err := json.Marshal(rule)
	if err != nil {
		a.internalError(w, r, "guardar o programa", err)
		return
	}
	if _, errs := thirdparty.WithCustom(a.custom.base, []json.RawMessage{data}); len(errs) > 0 {
		http.Redirect(w, r, back+"&erro=assinante-regra", http.StatusSeeOther)
		return
	}
	if err := a.store.SaveCustomApp(r.Context(), store.CustomApp{ID: app, Rule: data, UpdatedBy: s.Username}); err != nil {
		a.internalError(w, r, "guardar o programa", err)
		return
	}
	if err := a.reloadRules(r.Context()); err != nil {
		a.internalError(w, r, "recarregar as regras", err)
		return
	}
	a.audit(r, s, "terceiros.editar", app, fmt.Sprintf("assinatura %s (do instalador %s %s, SHA-256 %s…); antes: %s",
		in.Signer, pkgs[i].Version, pkgs[i].Type, sha[:12], before))
	http.Redirect(w, r, back+"&assinante=1", http.StatusSeeOther)
}
