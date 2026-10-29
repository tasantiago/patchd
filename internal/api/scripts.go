package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/bundle"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/pkgcache"
	"github.com/tasantiago/patchd/internal/store"
)

// Execução de scripts remotos (Aula 23.8): o admin sobe um ZIP com o executável, o .ini e o
// script que começa a execução, escolhe por onde começar e em quais máquinas, e cada máquina
// recebe um job 'script' assinado com o SHA-256 do ZIP.
//
// O ZIP é conferido na subida (internal/bundle) e guardado no diretório de conteúdo, em
// bundles/<sha256>.zip — o mesmo lugar e a mesma ideia dos instaladores de terceiros. O job
// não leva linha de comando: leva o hash, o ponto de entrada e os argumentos conferidos.

const (
	maxBundleUpload = bundle.MaxZipBytes + (1 << 20) // o ZIP mais a folga do multipart
	scriptJobTTL    = 24 * time.Hour
	defaultTimeout  = 600
	maxBundleNote   = 200
)

// bundlesDir: onde os ZIPs ficam no servidor.
func (a *api) bundlesDir() string { return filepath.Join(a.contentDir, "bundles") }

// --- Página ------------------------------------------------------------------------------

type scriptsView struct {
	pageBase
	Bundles  []bundleRow
	Chosen   *bundleRow // o bundle aberto para executar
	Machines []scriptMachineRow
	Rings    []int
	NoStore  bool // sem PATCHD_CONTENT_DIR: não dá para guardar bundle
	NoJobKey bool // sem chave de jobs: o painel mostra, mas não dispara
	Timeout  int
	MaxMiB   int
}

type bundleRow struct {
	ID              int64
	Name, SHA, Note string
	Size, Files     string
	OS, OSLabel     string
	Uploaded, By    string
	Entries         []string
}

type scriptMachineRow struct {
	ID, Name, OS string
	Ring         int
	LastSeen     string
}

// scriptsPage: GET /painel/scripts[?bundle=N], só admin.
func (a *api) scriptsPage(w http.ResponseWriter, r *http.Request, s panel.Session) {
	v := scriptsView{pageBase: a.baseFor(r.Context(), s, "Scripts", "scripts"),
		NoStore: a.contentDir == "", NoJobKey: a.jobKey == nil, Timeout: defaultTimeout, MaxMiB: bundle.MaxZipBytes >> 20}
	v.Sub = "scripts"
	scriptsFlash(r.URL.Query(), &v.pageBase)

	list, err := a.store.ScriptBundles(r.Context())
	if err != nil {
		a.internalError(w, r, "ler os bundles", err)
		return
	}
	for _, b := range list {
		v.Bundles = append(v.Bundles, a.bundleRowOf(b))
	}
	// Com ?bundle=N, a página abre o formulário de execução daquele bundle.
	if q := r.URL.Query().Get("bundle"); q != "" {
		id, err := strconv.ParseInt(q, 10, 64)
		if err == nil {
			if i := slices.IndexFunc(v.Bundles, func(b bundleRow) bool { return b.ID == id }); i >= 0 {
				v.Chosen = &v.Bundles[i]
				if err := a.scriptTargets(r, v.Chosen.OS, &v); err != nil {
					a.internalError(w, r, "listar as máquinas", err)
					return
				}
			}
		}
	}
	a.render(w, r, http.StatusOK, "scripts", v)
}

func (a *api) bundleRowOf(b store.ScriptBundle) bundleRow {
	return bundleRow{ID: b.ID, Name: b.Name, SHA: b.SHA256[:12], Note: b.Note,
		Size: pkgcache.Bytes(b.Size), Files: fmt.Sprintf("%d", len(b.Files)),
		OS: b.OS, OSLabel: bundleOSLabel(b.OS), Uploaded: a.stamp(b.UploadedAt), By: dash(b.UploadedBy),
		Entries: b.Entries()}
}

func bundleOSLabel(os string) string {
	switch os {
	case bundle.OSWindows:
		return "Windows"
	case bundle.OSUnix:
		return "Linux/macOS"
	}
	return "Windows e Linux/macOS"
}

// scriptTargets lista as máquinas que podem receber este bundle: as do sistema dele.
func (a *api) scriptTargets(r *http.Request, bundleOS string, v *scriptsView) error {
	list, err := a.store.InventoryList(r.Context())
	if err != nil {
		return err
	}
	rings := map[int]bool{}
	for _, m := range list {
		if mos := machineOS(m.OSName); bundleOS != "" && mos != bundleOS {
			continue
		}
		rings[m.Ring] = true
		v.Machines = append(v.Machines, scriptMachineRow{ID: m.ID, Name: dash(m.Hostname),
			OS: strings.TrimSpace(m.OSName + " " + m.OSVersion), Ring: m.Ring, LastSeen: a.stamp(m.LastSeenAt)})
	}
	for ring := range rings {
		v.Rings = append(v.Rings, ring)
	}
	slices.Sort(v.Rings)
	return nil
}

// machineOS traduz o nome do SO do inventário para o do bundle.
func machineOS(name string) string {
	if strings.Contains(strings.ToLower(name), "windows") {
		return bundle.OSWindows
	}
	if name == "" {
		return ""
	}
	return bundle.OSUnix
}

// --- Subida do bundle --------------------------------------------------------------------

// uploadBundleAction: POST /painel/scripts, multipart com o ZIP.
func (a *api) uploadBundleAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	back := func(q string) { http.Redirect(w, r, "/painel/scripts?"+q, http.StatusSeeOther) }
	if a.contentDir == "" {
		back("erro=sem-pasta")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBundleUpload)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		back("erro=grande")
		return
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()

	note := strings.TrimSpace(r.PostFormValue("nota"))
	if len([]rune(note)) > maxBundleNote || strings.ContainsAny(note, "\r\n\t") {
		back("erro=nota")
		return
	}
	f, fh, err := r.FormFile("arquivo")
	if err != nil {
		back("erro=arquivo")
		return
	}
	defer f.Close()

	name := bundleNameOf(r.PostFormValue("nome"), fh.Filename)
	if name == "" {
		back("erro=nome")
		return
	}
	// Grava num temporário enquanto calcula o SHA-256; o nome final é o hash.
	if err := os.MkdirAll(a.bundlesDir(), 0o700); err != nil {
		a.internalError(w, r, "criar a pasta dos bundles", err)
		return
	}
	tmp, err := os.CreateTemp(a.bundlesDir(), ".subindo-*")
	if err != nil {
		a.internalError(w, r, "guardar o bundle", err)
		return
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	h := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(f, bundle.MaxZipBytes+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		a.internalError(w, r, "guardar o bundle", err)
		return
	}
	if size > bundle.MaxZipBytes {
		back("erro=grande")
		return
	}
	// Confere o ZIP antes de aceitar: nomes, tetos, e se há ponto de entrada.
	zf, err := os.Open(tmp.Name()) // #nosec G304 -- arquivo temporário criado aqui
	if err != nil {
		a.internalError(w, r, "ler o bundle", err)
		return
	}
	info, ierr := bundle.Inspect(zf, size)
	_ = zf.Close()
	if ierr != nil {
		back("erro=zip&motivo=" + url.QueryEscape(shortError(ierr.Error(), 120)))
		return
	}
	sum := hex.EncodeToString(h.Sum(nil))
	b := store.ScriptBundle{Name: name, SHA256: sum, Size: size, OS: info.OS, Files: info.Files,
		Note: note, UploadedBy: s.Username}
	id, err := a.store.AddScriptBundle(r.Context(), b)
	switch {
	case errors.Is(err, store.ErrBundleExists):
		back("erro=repetido")
		return
	case err != nil:
		a.internalError(w, r, "cadastrar o bundle", err)
		return
	}
	// Só agora o arquivo vai para o nome definitivo.
	final := filepath.Join(a.bundlesDir(), sum+".zip")
	if err := os.Rename(tmp.Name(), final); err != nil {
		a.internalError(w, r, "guardar o bundle", err)
		return
	}
	a.logger.Info("bundle de script subido", "bundle_id", id, "name", name, "sha256", sum[:12], "size", size,
		"files", len(info.Files), "os", info.OS, "username", s.Username, "request_id", RequestID(r.Context()))
	a.audit(r, s, "script.bundle", name, fmt.Sprintf("%d arquivo(s) · %s · sha256 %s…", len(info.Files), pkgcache.Bytes(size), sum[:12]))
	back("subiu=" + strconv.FormatInt(id, 10))
}

// bundleNameOf: o nome que o admin digitou, ou o do arquivo sem a extensão.
func bundleNameOf(typed, filename string) string {
	name := strings.TrimSpace(typed)
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(filepath.FromSlash(filename)), ".zip")
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 || !bundle.ValidEntry(name) {
		return ""
	}
	return name
}

// deleteBundleAction: POST /painel/scripts/{id}/excluir.
func (a *api) deleteBundleAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	back := func(q string) { http.Redirect(w, r, "/painel/scripts?"+q, http.StatusSeeOther) }
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	b, ok, err := a.store.DeleteScriptBundle(r.Context(), id)
	if err != nil {
		a.internalError(w, r, "remover o bundle", err)
		return
	}
	if !ok {
		back("erro=sumiu")
		return
	}
	if a.contentDir != "" {
		if err := os.Remove(filepath.Join(a.bundlesDir(), b.SHA256+".zip")); err != nil && !os.IsNotExist(err) {
			a.logger.Warn("bundle removido do banco, mas o arquivo ficou", "sha256", b.SHA256[:12], "error", err)
		}
	}
	a.audit(r, s, "script.bundle", b.Name, "removido")
	back("removido=1")
}

// --- Execução ----------------------------------------------------------------------------

// runScriptAction: POST /painel/scripts/{id}/executar. Um job por máquina escolhida.
func (a *api) runScriptAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	back := func(q string) {
		http.Redirect(w, r, fmt.Sprintf("/painel/scripts?bundle=%d&%s", id, q), http.StatusSeeOther)
	}
	if a.jobKey == nil {
		back("erro=sem-chave")
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulário inválido", http.StatusBadRequest)
		return
	}
	b, ok, err := a.store.ScriptBundle(r.Context(), id)
	if err != nil {
		a.internalError(w, r, "ler o bundle", err)
		return
	}
	if !ok {
		back("erro=sumiu")
		return
	}
	entry := r.PostFormValue("entrada")
	if !slices.Contains(b.Entries(), entry) {
		back("erro=entrada")
		return
	}
	timeout, _ := strconv.Atoi(r.PostFormValue("prazo"))
	if timeout == 0 {
		timeout = defaultTimeout
	}
	args, err := splitScriptArgs(r.PostFormValue("argumentos"))
	if err != nil {
		back("erro=args&motivo=" + url.QueryEscape(shortError(err.Error(), 120)))
		return
	}
	p := jobs.ScriptParams{Bundle: b.Name, SHA256: b.SHA256, Size: b.Size, Entry: entry,
		Runner: bundle.RunnerFor(entry), Args: args, Timeout: timeout}
	if err := p.Validate(); err != nil {
		back("erro=params&motivo=" + url.QueryEscape(shortError(err.Error(), 120)))
		return
	}
	ids := r.PostForm["maquina"]
	if len(ids) == 0 || len(ids) > maxBatch {
		back("erro=nenhuma")
		return
	}
	raw, err := json.Marshal(p)
	if err != nil {
		a.internalError(w, r, "montar o job", err)
		return
	}
	criados, pulados := 0, 0
	for _, mid := range ids {
		if !machineIDPattern.MatchString(mid) {
			pulados++
			continue
		}
		_, err := jobs.Issue(r.Context(), a.store, a.jobKey,
			jobs.Request{MachineID: mid, Type: jobs.TypeScript, TTL: scriptJobTTL, By: s.Username, Params: raw}, a.now())
		switch {
		case errors.Is(err, jobs.ErrMachineUnavailable):
			pulados++
		case err != nil:
			a.internalError(w, r, "criar o job", err)
			return
		default:
			criados++
		}
	}
	a.logger.Info("bundle de script disparado", "bundle", b.Name, "entry", entry, "jobs", criados, "skipped", pulados,
		"username", s.Username, "request_id", RequestID(r.Context()))
	a.audit(r, s, "script.executar", b.Name+" · "+entry,
		fmt.Sprintf("%d job(s) criado(s), %d máquina(s) fora", criados, pulados))
	back(fmt.Sprintf("criados=%d&pulados=%d", criados, pulados))
}

// splitScriptArgs separa a linha de argumentos em tokens e confere cada um pelas regras do
// pacote jobs (sem aspas, espaços, barras invertidas ou caminhos).
func splitScriptArgs(line string) ([]string, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, nil
	}
	if strings.ContainsAny(line, `"'`) {
		return nil, errors.New("argumentos com aspas não são aceitos")
	}
	args := strings.Fields(line)
	if len(args) > jobs.MaxScriptArgs {
		return nil, fmt.Errorf("mais de %d argumentos", jobs.MaxScriptArgs)
	}
	p := jobs.ScriptParams{Bundle: "x", SHA256: strings.Repeat("0", 64), Size: 1, Entry: "x.sh",
		Runner: bundle.RunnerBash, Args: args, Timeout: jobs.MinScriptTimeout}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return args, nil
}

// bundleName: o nome do bundle no servidor, como o agente o pede.
var bundleName = regexp.MustCompile(`^[0-9a-f]{64}\.zip$`)

// bundleFile: GET /api/v1/agent/content/bundles/{file}, com a credencial da máquina. O nome
// é o SHA-256 do job mais ".zip".
func (a *api) bundleFile(w http.ResponseWriter, r *http.Request, id string) {
	name := r.PathValue("file")
	if a.contentDir == "" || !bundleName.MatchString(name) {
		writeError(w, http.StatusNotFound, "content_not_found", "bundle não encontrado")
		return
	}
	f, err := os.Open(filepath.Join(a.bundlesDir(), name)) // #nosec G304 -- nome conferido pelo padrão
	if os.IsNotExist(err) {
		writeError(w, http.StatusNotFound, "content_not_found", "bundle não encontrado no servidor")
		return
	}
	if err != nil {
		a.internalError(w, r, "abrir bundle", err)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, "content_not_found", "bundle não encontrado")
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("ETag", `"`+strings.TrimSuffix(name, ".zip")+`"`)
	a.logger.Info("download de bundle", "machine_id", id, "file", name[:16], "request_id", RequestID(r.Context()))
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

// scriptsFlash traduz os parâmetros de volta em mensagem.
func scriptsFlash(q url.Values, b *pageBase) {
	switch {
	case q.Has("subiu"):
		b.Flash = "Bundle guardado. Escolha o ponto de entrada e as máquinas para executar."
	case q.Has("removido"):
		b.Flash = "Bundle removido."
	case q.Has("criados"):
		n, _ := strconv.Atoi(q.Get("criados"))
		msg := fmt.Sprintf("%d job(s) criado(s). O script roda no próximo check-in de cada máquina; acompanhe em Automação › Jobs.", n)
		if p, _ := strconv.Atoi(q.Get("pulados")); p > 0 {
			msg += fmt.Sprintf(" %d máquina(s) ficou(ram) de fora (aposentada ou inexistente).", p)
		}
		b.Flash = msg
	case q.Get("erro") == "sem-pasta":
		b.Error = "Sem PATCHD_CONTENT_DIR: o servidor não tem onde guardar o bundle."
	case q.Get("erro") == "sem-chave":
		b.Error = "Sem chave de jobs: o painel mostra os bundles, mas não dispara execução."
	case q.Get("erro") == "grande":
		b.Error = fmt.Sprintf("O ZIP passa de %d MiB.", bundle.MaxZipBytes>>20)
	case q.Get("erro") == "arquivo":
		b.Error = "Escolha um arquivo .zip."
	case q.Get("erro") == "nome":
		b.Error = "Nome do bundle inválido (até 64 caracteres, sem barras nem acentos estranhos)."
	case q.Get("erro") == "nota":
		b.Error = fmt.Sprintf("Descrição: até %d caracteres, numa linha.", maxBundleNote)
	case q.Get("erro") == "repetido":
		b.Error = "Esse ZIP já está cadastrado (mesmo SHA-256)."
	case q.Get("erro") == "zip":
		b.Error = "O ZIP foi recusado: " + shortError(q.Get("motivo"), 120)
	case q.Get("erro") == "entrada":
		b.Error = "Escolha um ponto de entrada da lista do bundle."
	case q.Get("erro") == "args", q.Get("erro") == "params":
		b.Error = "Parâmetros recusados: " + shortError(q.Get("motivo"), 120)
	case q.Get("erro") == "nenhuma":
		b.Error = "Escolha pelo menos uma máquina."
	case q.Get("erro") == "sumiu":
		b.Error = "Esse bundle não existe mais."
	}
}
