package api

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/pkgcache"
	"github.com/tasantiago/patchd/internal/release"
)

// Configuração (Aula 15.2): a área do admin. A aba Sistema mostra como o servidor está
// configurado (só leitura: a configuração continua no ambiente e nos arquivos de segredo);
// a aba Agente distribui o agente: os executáveis da versão publicada, a CA que as máquinas
// devem confiar e os tokens de registro, com o comando de instalação pronto.

// SystemInfo é o que só o processo do servidor sabe sobre si (a partida, o TLS, a
// retenção); o main preenche.
type SystemInfo struct {
	Version, Commit, GoVersion string
	StartedAt                  time.Time
	Database                   bool // PostgreSQL; falso: memória
	Schema                     int  // a migração mais nova (a do banco depois da partida)
	TLSNotAfter                func() time.Time
	Retention                  string
	Catalog                    string // o agendamento do catálogo, como o log da partida diz
	// Live (Aula 19.2): o catálogo e a retenção que valem agora (mudam pela tela); nil: os de cima.
	Live func() (catalog, retention string)
	// Backups (Aula 20.8, F4): a cópia e o ensaio de restauração mais recentes; nil: sem banco.
	Backups func(ctx context.Context) (BackupStatus, error)
}

// WithSystemInfo liga a aba Sistema da Configuração.
func WithSystemInfo(si SystemInfo) Option {
	return func(a *api) { a.system = &si }
}

// WithAgentCA: a CA que as máquinas devem confiar (PATCHD_AGENT_CA_FILE), em PEM, para a
// aba Agente oferecer o download e montar o comando com -ca-file.
func WithAgentCA(pem []byte) Option {
	return func(a *api) { a.agentCA = pem }
}

// Limites dos tokens criados pelo painel: os mesmos da linha de comando.
const (
	panelTokenMaxUses = 10000
	panelTokenMaxNote = 100
)

// tokenTTLs: as validades oferecidas, em horas.
var tokenTTLs = []int{1, 24, 72, 168, 720}

type settingRow struct {
	Label, Value, Level string // Level: "", "atencao", "erro"
}

type agentFileRow struct {
	OS, Arch, Name, Size, SHA, Href string
}

type tokenRow struct {
	ID                             int64
	Note, Created, Expires, Status string
	Uses, MaxUses                  int
	CanRevoke                      bool
}

type settingsView struct {
	Discovery *discoveryConfigView
	pageBase
	Tab         string
	System      []settingRow
	Version     string // a versão publicada do agente
	Files       []agentFileRow
	FilesNote   string
	CA          bool
	CASHA       string
	Tokens      []tokenRow
	TTLs        []hourOption
	MaxUses     int
	MaxNote     int
	Access      *accessView      // aba Acesso (Aula 15.3)
	Audit       *auditView       // aba Auditoria (Aula 15.14)
	SelfService *selfServiceView // aba Autoatendimento (Aula 17.1)
	NotifyOn    bool             // aba Sistema: notificações ligadas, com o botão de teste (Aula 18.5)
	Appearance  *logoView        // aba Sistema: o cartão Aparência (Aula 19.1)
	Params      []paramGroup     // aba Parâmetros (Aula 19.2)
	Assets      *assetView       // aba Patrimônio (Aula 23.6)
}

func (a *api) settingsTabs(tab string) string {
	switch tab {
	case "agente", "acesso", "auditoria", "autoatendimento", "patrimonio", "descoberta":
		return tab
	case "parametros":
		if a.params != nil {
			return tab
		}
	}
	return "sistema"
}

// settingsPage: GET /painel/configuracao[?aba=agente], só admin.
func (a *api) settingsPage(w http.ResponseWriter, r *http.Request, s panel.Session) {
	q := r.URL.Query()
	v := settingsView{pageBase: a.baseFor(r.Context(), s, "Configuração", "configuracao"), Tab: a.settingsTabs(q.Get("aba")),
		MaxUses: panelTokenMaxUses, MaxNote: panelTokenMaxNote, TTLs: hourOptions(tokenTTLs, 24)}
	v.Sub = v.Tab
	switch {
	case q.Has("revogado"):
		v.Flash = fmt.Sprintf("Token %s revogado: nenhuma máquina nova se registra com ele.", q.Get("revogado"))
	case q.Get("erro") == "revogar":
		v.Error = "Esse token não existe; nada mudou."
	}
	var err error
	switch v.Tab {
	case "descoberta":
		w.Header().Set("Cache-Control", "no-store")
		v.Discovery, err = a.discoveryConfig(r.Context())
	case "sistema":
		notifyFlash(q, &v.pageBase)
		logoFlash(q, &v.pageBase)
		v.NotifyOn = a.notifyOn()
		if v.System, err = a.systemRows(r); err == nil {
			v.Appearance, err = a.logoCard(r.Context())
		}
	case "agente":
		err = a.agentView(r, &v)
	case "patrimonio":
		assetsFlash(q, &v.pageBase)
		err = a.assetsTab(r, &v)
	case "acesso":
		accessFlash(q, &v.pageBase)
		err = a.accessView(r, s, &v)
	case "auditoria":
		err = a.auditTab(r, &v)
	case "autoatendimento":
		selfServiceFlash(q, &v.pageBase)
		err = a.selfServiceTab(r, &v)
	case "parametros":
		a.paramsFlash(q, &v.pageBase)
		notifyFlash(q, &v.pageBase) // o botão de teste do cartão Notificações volta aqui
		v.NotifyOn = a.notifyOn()
		v.Params = a.paramsView(nil, nil)
	}
	if err != nil {
		a.internalError(w, r, "montar a configuração", err)
		return
	}
	a.render(w, r, http.StatusOK, "configuracao", v)
}

// systemRows: o estado do servidor, linha a linha. Nada de segredo nem de endereço do banco.
func (a *api) systemRows(r *http.Request) ([]settingRow, error) {
	var rows []settingRow
	add := func(label, value, level string) { rows = append(rows, settingRow{label, value, level}) }
	now := a.now()
	if si := a.system; si != nil {
		add("Servidor", fmt.Sprintf("%s · commit %s · %s", si.Version, shortCommit(si.Commit), si.GoVersion), "")
		add("Em execução desde", si.StartedAt.UTC().Format("02/01/2006 15:04")+" UTC ("+ago(now.Sub(si.StartedAt))+")", "")
		if si.Database {
			add("Banco", fmt.Sprintf("PostgreSQL · migração %d", si.Schema), "")
		} else {
			add("Banco", "memória: nada sobrevive à reinicialização", "atencao")
		}
		if si.Backups != nil {
			b, err := si.Backups(r.Context())
			if err != nil {
				return nil, err
			}
			v, l := backupRow(b, now)
			add("Cópias do banco", v, l)
		}
		switch {
		case si.TLSNotAfter == nil:
			add("TLS", "desligado: agentes e painel por HTTP", "erro")
		default:
			exp := si.TLSNotAfter()
			days := int(exp.Sub(now).Hours() / 24)
			level := ""
			if days < 30 {
				level = "atencao"
			}
			add("TLS", fmt.Sprintf("ligado · o certificado vence em %s (%d dias)", exp.UTC().Format("02/01/2006"), days), level)
		}
		catalog, retention := si.Catalog, si.Retention
		if si.Live != nil {
			catalog, retention = si.Live()
		}
		add("Catálogo", dash(catalog), "")
		add("Retenção", dash(retention), "")
	}
	if a.publicURL == "" {
		add("URL pública", "— (sem PATCHD_PUBLIC_URL: o comando de instalação sai com o endereço a preencher)", "atencao")
	} else {
		add("URL pública", a.publicURL, "")
	}
	nv, nl := a.notifyRow()
	add("Notificações", nv, nl)
	if a.jobKey != nil {
		add("Jobs", "ligados · chave "+jobs.KeyID(a.jobKey.Public().(ed25519.PublicKey)), "")
	} else {
		add("Jobs", "desligados: sem PATCHD_JOB_SIGNING_KEY_FILE, o painel não cria jobs e as campanhas param", "erro")
	}
	if a.escrow != nil { // Aula 21.4
		add("Guarda da senha da BIOS", "ligada · chave "+a.escrow.id+" (revelar: só admin, com motivo, auditoria e aviso)", "")
	} else {
		add("Guarda da senha da BIOS", "desligada (sem PATCHD_BIOS_ESCROW_KEY_FILE)", "")
	}
	kv, kl, err := a.fleetKeysRow(r.Context(), now)
	if err != nil {
		return nil, err
	}
	add("Chaves na frota", kv, kl)
	switch v, err := a.publishedVersion(); {
	case a.releases == "":
		add("Atualização do agente", "desligada (sem PATCHD_RELEASES_DIR)", "atencao")
	case err != nil:
		add("Atualização do agente", "pasta de versões ilegível: "+err.Error(), "erro")
	case v == "":
		add("Atualização do agente", "nenhuma versão publicada", "atencao")
	default:
		add("Atualização do agente", "publicada "+v, "")
	}
	if a.contentDir == "" {
		add("Conteúdo", "desligado (sem PATCHD_CONTENT_DIR): sem catálogo offline e sem instaladores de terceiros", "atencao")
	} else {
		pkgs, err := a.store.ThirdPartyPackages(r.Context())
		if err != nil {
			return nil, err
		}
		var size int64
		for _, p := range pkgs {
			size += p.Size
		}
		add("Conteúdo", fmt.Sprintf("ligado · %d instalador(es) de terceiros guardado(s), %s", len(pkgs), pkgcache.Bytes(size)), "")
	}
	if a.tpManifest != nil {
		rules, install := 0, 0
		for _, app := range a.tpManifest.For("windows") {
			rules++
			if app.Install != nil {
				install++
			}
		}
		add("Regras de terceiros", fmt.Sprintf("%d programa(s) do Windows, %d com instalação pelo patchd", rules, install), "")
	}
	login := []string{"usuários locais (contingência)"}
	if a.directory != nil {
		login = append([]string{"AD (LDAPS)"}, login...)
	}
	if a.oidc != nil {
		login = append([]string{"RHBK (OIDC)"}, login...)
	}
	add("Login do painel", strings.Join(login, " · "), "")
	return rows, nil
}

func shortCommit(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return dash(c)
}

// publishedVersion: a versão publicada na pasta ("" nenhuma).
func (a *api) publishedVersion() (string, error) {
	if a.releases == "" {
		return "", nil
	}
	return a.releases.Current()
}

// agentView monta a aba Agente.
func (a *api) agentView(r *http.Request, v *settingsView) error {
	v.CA = len(a.agentCA) > 0
	if v.CA {
		sum := sha256.Sum256(a.agentCA)
		v.CASHA = hex.EncodeToString(sum[:])[:16]
	}
	switch ver, err := a.publishedVersion(); {
	case a.releases == "":
		v.FilesNote = "A atualização do agente está desligada (PATCHD_RELEASES_DIR): não há executáveis para distribuir pelo painel."
	case err != nil:
		return err
	case ver == "":
		v.FilesNote = "Nenhuma versão publicada (patchd-server release publish)."
	default:
		m, err := a.releases.Manifest(ver)
		if err != nil {
			v.FilesNote = "A versão publicada não pôde ser lida: " + err.Error()
			break
		}
		v.Version = ver
		for _, f := range m.Files {
			v.Files = append(v.Files, agentFileRow{OS: f.OS, Arch: f.Arch, Name: f.Name, Size: pkgcache.Bytes(f.Size), SHA: f.SHA256[:16],
				Href: "/painel/configuracao/agente/arquivo/" + f.Name})
		}
	}
	list, err := a.store.EnrollmentTokens(r.Context())
	if err != nil {
		return err
	}
	now := a.now()
	for _, t := range list {
		st := t.Status(now)
		v.Tokens = append(v.Tokens, tokenRow{ID: t.ID, Note: dash(t.Note), Created: a.stamp(t.CreatedAt),
			Expires: a.stamp(t.ExpiresAt), Status: st, Uses: t.Uses, MaxUses: t.MaxUses, CanRevoke: st == "válido"})
	}
	slices.SortFunc(v.Tokens, func(x, y tokenRow) int { return int(y.ID - x.ID) })
	return nil
}

// agentFile: GET /painel/configuracao/agente/arquivo/{name}, só admin. Só os arquivos da
// versão publicada, pelo nome do manifesto (release.Dir.Open confere o formato).
func (a *api) agentFile(w http.ResponseWriter, r *http.Request, s panel.Session) {
	name := r.PathValue("name")
	ver, err := a.publishedVersion()
	if err != nil || ver == "" {
		http.NotFound(w, r)
		return
	}
	m, err := a.releases.Manifest(ver)
	if err != nil || !slices.ContainsFunc(m.Files, func(f release.File) bool { return f.Name == name }) {
		http.NotFound(w, r)
		return
	}
	f, err := a.releases.Open(ver, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		a.internalError(w, r, "ler o executável", err)
		return
	}
	a.logger.Info("agente baixado pelo painel", "file", name, "version", ver, "username", s.Username, "request_id", RequestID(r.Context()))
	a.audit(r, s, "agente.baixar", name, ver)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	http.ServeContent(w, r, name, st.ModTime(), f)
}

// agentCAFile: GET /painel/configuracao/agente/ca.crt, só admin.
func (a *api) agentCAFile(w http.ResponseWriter, r *http.Request, s panel.Session) {
	if len(a.agentCA) == 0 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Header().Set("Content-Disposition", `attachment; filename="ca.crt"`)
	_, _ = w.Write(a.agentCA)
}

type tokenCreatedView struct {
	pageBase
	ID                     int64
	Token, Expires, Note   string
	Uses                   int
	Server, CAPathWin      string
	CA                     bool
	WinCommand, LinCommand string
	Version                string
}

// createTokenAction: POST /painel/configuracao/agente/token. O token aparece uma única vez,
// na resposta (sem redirecionamento: ele não vai para URL, histórico nem log).
func (a *api) createTokenAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	r.Body = http.MaxBytesReader(w, r.Body, maxLoginBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulário inválido", http.StatusBadRequest)
		return
	}
	note := strings.TrimSpace(r.PostFormValue("nota"))
	hours, _ := strconv.Atoi(r.PostFormValue("validade"))
	uses, err := strconv.Atoi(r.PostFormValue("usos"))
	problem := ""
	switch {
	case !slices.Contains(tokenTTLs, hours):
		problem = "Escolha a validade da lista."
	case err != nil || uses < 1 || uses > panelTokenMaxUses:
		problem = fmt.Sprintf("Usos: de 1 a %d.", panelTokenMaxUses)
	case len([]rune(note)) > panelTokenMaxNote || strings.ContainsAny(note, "\r\n\t"):
		problem = fmt.Sprintf("Descrição: até %d caracteres, numa linha.", panelTokenMaxNote)
	}
	if problem != "" {
		v := settingsView{pageBase: a.baseFor(r.Context(), s, "Configuração", "configuracao"), Tab: "agente",
			MaxUses: panelTokenMaxUses, MaxNote: panelTokenMaxNote, TTLs: hourOptions(tokenTTLs, 24)}
		v.Error = problem
		if err := a.agentView(r, &v); err != nil {
			a.internalError(w, r, "montar a configuração", err)
			return
		}
		a.render(w, r, http.StatusUnprocessableEntity, "configuracao", v)
		return
	}
	plain, hash := identity.NewSecret(identity.EnrollmentPrefix)
	expires := a.now().Add(time.Duration(hours) * time.Hour).UTC()
	id, err := a.store.CreateEnrollmentToken(r.Context(), hash, note, expires, uses)
	if err != nil {
		a.internalError(w, r, "criar token", err)
		return
	}
	a.logger.Info("token de registro criado pelo painel", "token_id", id, "uses", uses, "expires_at", expires.Format(time.RFC3339),
		"username", s.Username, "request_id", RequestID(r.Context()))
	a.audit(r, s, "token.criar", fmt.Sprintf("token %d", id), fmt.Sprintf("%d uso(s), vence %s", uses, expires.Format(time.RFC3339)))
	v := tokenCreatedView{pageBase: a.baseFor(r.Context(), s, "Token de registro", "configuracao"), ID: id, Token: plain,
		Expires: expires.Format("02/01/2006 15:04") + " UTC", Note: note, Uses: uses, CA: len(a.agentCA) > 0}
	v.Server = a.publicURL
	if v.Server == "" {
		v.Server = "https://<endereço-do-servidor>"
	}
	v.Version, _ = a.publishedVersion()
	v.WinCommand, v.LinCommand = installCommands(v.Server, plain, v.CA)
	w.Header().Set("Cache-Control", "no-store")
	a.render(w, r, http.StatusOK, "token-criado", v)
}

// installCommands: o passo a passo de instalação, com o token num arquivo temporário (o
// agente lê o token de arquivo; na linha de comando ele ficaria no histórico e na lista
// de processos) apagado logo depois.
func installCommands(server, token string, ca bool) (win, lin string) {
	var wb, lb strings.Builder
	withCA := ""
	if ca {
		withCA = " e o ca.crt"
	}
	wb.WriteString("# PowerShell como administrador, na pasta com o patchd-agent-windows-amd64.exe" + withCA + "\n")
	caWin := ""
	if ca {
		wb.WriteString("New-Item -ItemType Directory -Force C:\\ProgramData\\patchd | Out-Null\n")
		wb.WriteString("Copy-Item .\\ca.crt C:\\ProgramData\\patchd\\ca.crt\n")
		caWin = " -ca-file C:\\ProgramData\\patchd\\ca.crt"
	}
	wb.WriteString("$t = Join-Path $env:TEMP 'patchd-token.txt'; Set-Content -Path $t -Value '" + token + "' -NoNewline\n")
	wb.WriteString(".\\patchd-agent-windows-amd64.exe install -server " + server + " -enroll-token-file $t" + caWin + "\n")
	wb.WriteString("Remove-Item $t")
	lb.WriteString("# na pasta com o patchd-agent-linux-amd64" + withCA + "\n")
	caLin := ""
	if ca {
		lb.WriteString("sudo install -d -m 0755 /etc/patchd && sudo install -m 0644 ca.crt /etc/patchd/ca.crt\n")
		caLin = " -ca-file /etc/patchd/ca.crt"
	}
	lb.WriteString("t=$(mktemp) && printf '%s' '" + token + "' > \"$t\"\n")
	lb.WriteString("chmod +x patchd-agent-linux-amd64 && sudo ./patchd-agent-linux-amd64 install -server " + server + " -enroll-token-file \"$t\"" + caLin + "\n")
	lb.WriteString("rm -f \"$t\"")
	return wb.String(), lb.String()
}

// revokeTokenAction: POST /painel/configuracao/agente/token/{id}/revogar.
func (a *api) revokeTokenAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	ok, err := a.store.RevokeEnrollmentToken(r.Context(), id)
	if err != nil {
		a.internalError(w, r, "revogar token", err)
		return
	}
	if !ok {
		http.Redirect(w, r, "/painel/configuracao?aba=agente&erro=revogar", http.StatusSeeOther)
		return
	}
	a.logger.Info("token de registro revogado pelo painel", "token_id", id, "username", s.Username, "request_id", RequestID(r.Context()))
	a.audit(r, s, "token.revogar", fmt.Sprintf("token %d", id), "")
	http.Redirect(w, r, fmt.Sprintf("/painel/configuracao?aba=agente&revogado=%d", id), http.StatusSeeOther)
}
