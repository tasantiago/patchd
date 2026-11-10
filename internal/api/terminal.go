package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/texto"
)

// O terminal do administrador (docs/terminal.md): uma aba na página da máquina em que o admin,
// com a sessão aberta (motivo obrigatório), digita um comando; ele vai como job assinado,
// roda na máquina com a conta do agente e a saída volta para a tela. Sem JavaScript: não é um
// terminal interativo, é um comando por vez, com a página se atualizando enquanto espera.
// Tudo fica registrado antes de o comando sair: a auditoria que não grava não deixa rodar.

// TerminalKey: o interruptor do terminal (Configuração › Parâmetros). Vem desligado.
const TerminalKey = "PATCHD_TERMINAL"

const (
	// terminalSessionFor: quanto a sessão vale sem comando novo (cada comando a renova).
	terminalSessionFor = 15 * time.Minute
	minTerminalReason  = 10
	maxTerminalReason  = 200
	// terminalShown: quantos comandos a aba mostra (os mais novos, de baixo para cima como
	// num terminal); terminalPage, os do histórico por página.
	terminalShown = 30
	terminalPage  = 100
	// terminalRefresh: a página se recarrega, em segundos, enquanto há comando esperando.
	terminalRefresh = 3
	// terminalRefreshFor: depois disso sem resposta (máquina desligada), a página para de se
	// recarregar sozinha: o que o admin estiver digitando não some a cada 3 segundos.
	terminalRefreshFor = 90 * time.Second
)

func (a *api) terminalOn() bool { return a.params != nil && a.params.Value(TerminalKey) == "1" }

type shellOption struct {
	Value, Label string
	Selected     bool
}

type terminalRow struct {
	ID                                       int64
	Prompt, Command, User, When, Reason      string
	State, Class, Note, Output, MachineID    string
	Machine, Remote                          string
	Pending, Truncated, HasOutput, ShowExtra bool
	Fresh                                    bool // pendente há pouco: a página se recarrega
}

type terminalView struct {
	MachineID string
	// Why: por que o terminal não está disponível aqui ("" = disponível).
	Why      string
	Session  bool
	Reason   string
	Until    string
	Shells   []shellOption
	Timeouts []int
	Timeout  int
	MaxLen   int
	MinR     int
	MaxR     int
	Rows     []terminalRow
	Pending  bool // há comando esperando a máquina
	Refresh  bool // e é recente: a página se recarrega sozinha
}

var terminalTimeouts = []int{30, 60, 120, 300, 600}

func shellLabel(s string) string {
	switch s {
	case jobs.ShellPowerShell:
		return "PowerShell"
	case jobs.ShellCmd:
		return "cmd"
	}
	return "bash"
}

func shellPrompt(s string) string {
	switch s {
	case jobs.ShellPowerShell:
		return "PS>"
	case jobs.ShellCmd:
		return "C:\\>"
	}
	return "#"
}

// terminalShells: os interpretadores da máquina, pelo sistema do inventário (sem inventário,
// os três: o agente recusa o que não tem).
func (a *api) terminalShells(ctx context.Context, id string) []string {
	inv, ok, err := a.store.LatestInventory(ctx, id)
	switch {
	case err != nil || !ok:
		return jobs.CommandShells
	case inv.OS.Family == "windows":
		return []string{jobs.ShellPowerShell, jobs.ShellCmd}
	}
	return []string{jobs.ShellBash}
}

func terminalRowOf(c store.TerminalCommand, stamp func(time.Time) string, now time.Time) terminalRow {
	row := terminalRow{ID: c.ID, Prompt: shellPrompt(c.Shell), Command: c.Command, User: c.Username, When: stamp(c.CreatedAt),
		Reason: c.Reason, Output: c.Output, Truncated: c.Truncated, HasOutput: c.Output != "", MachineID: c.MachineID, Remote: c.Remote, Note: c.Note}
	switch {
	case c.FinishedAt != nil && c.ExitCode != nil && *c.ExitCode == 0:
		row.State, row.Class = "código 0", "concluido"
	case c.FinishedAt != nil && c.ExitCode != nil:
		row.State, row.Class = "código "+strconv.Itoa(*c.ExitCode), "falhou"
	case c.FinishedAt != nil:
		row.State, row.Class = "não concluído", "falhou"
	case c.JobState == jobs.StateExpired:
		row.State, row.Class, row.Note = "expirou", "expirado", "a máquina não pegou o comando em "+strconv.Itoa(int(jobs.CommandTTL/time.Minute))+" min (desligada ou sem o canal em tempo real)"
	case c.JobState == jobs.StateCanceled:
		row.State, row.Class = "cancelado", "cancelado"
	case c.JobState == jobs.StateDelivered:
		row.State, row.Class, row.Pending = "rodando", "entregue", true
	case c.JobID == 0 || c.JobState == "":
		row.State, row.Class = "sem resultado", "expirado"
	default:
		row.State, row.Class, row.Pending = "esperando a máquina", "pendente", now.Sub(c.CreatedAt) < jobs.CommandTTL+time.Minute
	}
	row.Fresh = row.Pending && now.Sub(c.CreatedAt) < terminalRefreshFor
	return row
}

// terminalTabView monta a aba Terminal da máquina.
func (a *api) terminalTabView(r *http.Request, s panel.Session, id string) (*terminalView, error) {
	ctx := r.Context()
	v := &terminalView{MachineID: id, Timeouts: terminalTimeouts, Timeout: jobs.DefaultCommandTimeout, MaxLen: jobs.MaxCommandLen,
		MinR: minTerminalReason, MaxR: maxTerminalReason}
	caps, err := a.store.MachineCapabilities(ctx, id)
	if err != nil {
		return nil, err
	}
	switch {
	case !a.terminalOn():
		v.Why = "O terminal está desligado. Liga em Configuração › Parâmetros › Agentes › Terminal do administrador."
	case a.jobKey == nil:
		v.Why = "O servidor está sem a chave de jobs (PATCHD_JOB_SIGNING_KEY_FILE): o comando vai como job assinado."
	case !slices.Contains(caps, protocol.CapabilityTerminal):
		v.Why = "O agente desta máquina não aceita o terminal: é anterior a esta versão (atualize o agente) ou foi instalado com -no-terminal."
	}
	list, err := a.store.TerminalCommands(ctx, store.TerminalFilter{MachineID: id, Limit: terminalShown})
	if err != nil {
		return nil, err
	}
	now := a.now()
	for i := len(list) - 1; i >= 0; i-- { // o mais novo embaixo, como num terminal
		row := terminalRowOf(list[i], a.stamp, now)
		v.Pending, v.Refresh = v.Pending || row.Pending, v.Refresh || row.Fresh
		v.Rows = append(v.Rows, row)
	}
	if v.Why != "" {
		return v, nil
	}
	sess, ok, err := a.store.ActiveTerminalSession(ctx, id, s.Username, now)
	if err != nil {
		return nil, err
	}
	if ok {
		v.Session, v.Reason, v.Until = true, sess.Reason, a.stamp(sess.ExpiresAt)
	}
	sel := r.URL.Query().Get("shell")
	shells := a.terminalShells(ctx, id)
	if !slices.Contains(shells, sel) {
		sel = shells[0]
	}
	for _, sh := range shells {
		v.Shells = append(v.Shells, shellOption{Value: sh, Label: shellLabel(sh), Selected: sh == sel})
	}
	if t, err := strconv.Atoi(r.URL.Query().Get("tempo")); err == nil && slices.Contains(terminalTimeouts, t) {
		v.Timeout = t
	}
	return v, nil
}

func terminalBack(id string, q url.Values) string {
	q.Set("aba", "terminal")
	return machinePath(id) + "?" + q.Encode() + "#terminal"
}

// terminalOpenAction: POST /painel/maquinas/{id}/terminal/abrir. A sessão, com o motivo.
func (a *api) terminalOpenAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	id := r.PathValue("id")
	if !a.terminalOn() {
		http.Redirect(w, r, terminalBack(id, url.Values{"terminal-erro": {"desligado"}}), http.StatusSeeOther)
		return
	}
	if _, ok, err := a.store.HostState(r.Context(), id); err != nil {
		a.internalError(w, r, "ler a máquina", err)
		return
	} else if !ok {
		http.NotFound(w, r)
		return
	}
	reason := strings.Join(strings.Fields(r.PostFormValue("motivo")), " ")
	if n := utf8.RuneCountInString(reason); n < minTerminalReason || n > maxTerminalReason {
		http.Redirect(w, r, terminalBack(id, url.Values{"terminal-erro": {"motivo"}}), http.StatusSeeOther)
		return
	}
	now := a.now()
	// A auditoria primeiro: sem ela gravada, a sessão não abre.
	ev := store.AuditEvent{At: now, Username: s.Username, Source: s.Source, Action: "terminal.abrir", Target: id,
		Detail: "motivo: " + reason, Remote: remoteIP(r), RequestID: RequestID(r.Context())}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), auditTimeout)
	defer cancel()
	if err := a.store.RecordAudit(ctx, ev); err != nil {
		a.logger.Error("terminal: sessão não aberta, a auditoria não gravou", "machine_id", id, "error", err, "request_id", ev.RequestID)
		http.Redirect(w, r, terminalBack(id, url.Values{"terminal-erro": {"auditoria"}}), http.StatusSeeOther)
		return
	}
	if _, err := a.store.OpenTerminalSession(ctx, store.TerminalSession{MachineID: id, Username: s.Username, Reason: reason, Remote: ev.Remote,
		OpenedAt: now, ExpiresAt: now.Add(terminalSessionFor)}); err != nil {
		a.internalError(w, r, "abrir a sessão do terminal", err)
		return
	}
	a.logger.Warn("terminal: sessão aberta", "machine_id", id, "username", s.Username, "remote", ev.Remote, "motivo", reason, "request_id", ev.RequestID)
	a.mailTerminal(r.Context(), s, id, reason, now)
	http.Redirect(w, r, terminalBack(id, url.Values{}), http.StatusSeeOther)
}

// terminalCloseAction: POST /painel/maquinas/{id}/terminal/fechar. Encerra a sessão do admin.
func (a *api) terminalCloseAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	id := r.PathValue("id")
	sess, ok, err := a.store.ActiveTerminalSession(r.Context(), id, s.Username, a.now())
	if err != nil {
		a.internalError(w, r, "ler a sessão do terminal", err)
		return
	}
	if ok {
		if err := a.store.SetTerminalSessionExpiry(r.Context(), sess.ID, a.now()); err != nil {
			a.internalError(w, r, "fechar a sessão do terminal", err)
			return
		}
		a.audit(r, s, "terminal.fechar", id, "sessão #"+strconv.FormatInt(sess.ID, 10))
	}
	http.Redirect(w, r, terminalBack(id, url.Values{}), http.StatusSeeOther)
}

// terminalCommandAction: POST /painel/maquinas/{id}/terminal/comando. Registra, audita e só
// então cria o job assinado.
func (a *api) terminalCommandAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	id := r.PathValue("id")
	back := func(k, val string, keep url.Values) {
		if keep == nil {
			keep = url.Values{}
		}
		if k != "" {
			keep.Set(k, val)
		}
		http.Redirect(w, r, terminalBack(id, keep), http.StatusSeeOther)
	}
	if !a.terminalOn() {
		back("terminal-erro", "desligado", nil)
		return
	}
	if a.jobKey == nil {
		back("terminal-erro", "sem-chave", nil)
		return
	}
	timeout, _ := strconv.Atoi(r.PostFormValue("tempo"))
	p := jobs.CommandParams{Shell: r.PostFormValue("shell"), Command: strings.TrimSpace(r.PostFormValue("comando")), TimeoutSec: timeout}
	keep := url.Values{"shell": {p.Shell}, "tempo": {strconv.Itoa(timeout)}}
	if !slices.Contains(terminalTimeouts, timeout) || p.Validate() != nil || !slices.Contains(a.terminalShells(r.Context(), id), p.Shell) {
		back("terminal-erro", "comando", keep)
		return
	}
	caps, err := a.store.MachineCapabilities(r.Context(), id)
	if err != nil {
		a.internalError(w, r, "ler o agente da máquina", err)
		return
	}
	if !slices.Contains(caps, protocol.CapabilityTerminal) {
		back("terminal-erro", "agente", nil)
		return
	}
	now := a.now()
	sess, ok, err := a.store.ActiveTerminalSession(r.Context(), id, s.Username, now)
	if err != nil {
		a.internalError(w, r, "ler a sessão do terminal", err)
		return
	}
	if !ok {
		back("terminal-erro", "sessao", keep)
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), auditTimeout)
	defer cancel()
	remote := remoteIP(r)
	// 1. A auditoria, com o texto do comando: sem ela, nada sai.
	ev := store.AuditEvent{At: now, Username: s.Username, Source: s.Source, Action: "terminal.comando", Target: id,
		Detail: texto.Clip(fmt.Sprintf("sessão #%d · %s · %d s · %s", sess.ID, shellLabel(p.Shell), p.TimeoutSec, p.Command), 2000),
		Remote: remote, RequestID: RequestID(r.Context())}
	if err := a.store.RecordAudit(ctx, ev); err != nil {
		a.logger.Error("terminal: comando não enviado, a auditoria não gravou", "machine_id", id, "error", err, "request_id", ev.RequestID)
		back("terminal-erro", "auditoria", keep)
		return
	}
	// 2. O job assinado (o registro do comando aponta para ele).
	raw, err := json.Marshal(p)
	if err != nil {
		a.internalError(w, r, "montar o comando", err)
		return
	}
	rec, err := jobs.Issue(ctx, a.store, a.jobKey, jobs.Request{MachineID: id, Type: jobs.TypeCommand, TTL: jobs.CommandTTL, By: s.Username, Params: raw}, now)
	switch {
	case errors.Is(err, jobs.ErrMachineUnavailable):
		back("terminal-erro", "maquina", nil)
		return
	case err != nil:
		a.internalError(w, r, "criar o job do comando", err)
		return
	}
	// 3. O registro do terminal.
	if _, err := a.store.CreateTerminalCommand(ctx, store.TerminalCommand{SessionID: sess.ID, MachineID: id, Username: s.Username, Remote: remote,
		Shell: p.Shell, Command: p.Command, TimeoutSec: p.TimeoutSec, JobID: rec.ID, CreatedAt: now}); err != nil {
		// O job já existe: cancela, para não rodar um comando sem registro.
		_, _ = a.store.CancelJob(ctx, rec.ID, "terminal: o registro do comando não gravou")
		a.internalError(w, r, "registrar o comando do terminal", err)
		return
	}
	if err := a.store.SetTerminalSessionExpiry(ctx, sess.ID, now.Add(terminalSessionFor)); err != nil {
		a.logger.Error("terminal: sessão não renovada", "sessao", sess.ID, "error", err)
	}
	a.logger.Warn("terminal: comando enviado", "machine_id", id, "username", s.Username, "remote", remote, "shell", p.Shell,
		"job_id", rec.ID, "comando", p.Command, "request_id", ev.RequestID)
	back("", "", keep)
}

// terminalJobFinished grava, no registro do terminal, o resultado que o agente mandou.
func (a *api) terminalJobFinished(ctx context.Context, job store.JobRow, res protocol.JobResult) {
	out := res.Output
	truncated := strings.Contains(res.Detail, "saída cortada")
	if len(out) > jobs.MaxCommandOutput { // um agente que mandou mais que o combinado
		out, truncated = texto.Clip(out, jobs.MaxCommandOutput), true
	}
	note := res.Detail
	if res.Status == protocol.JobResultRejected {
		note = "recusado pelo agente: " + res.Detail
	}
	if _, err := a.store.FinishTerminalCommand(ctx, job.ID, res.ExitCode, texto.Clean(out), truncated, texto.Clip(note, 500), a.now()); err != nil {
		a.logger.Error("terminal: o resultado do comando não foi gravado", "job_id", job.ID, "error", err)
	}
}

// mailTerminal avisa a lista de notificações que uma sessão foi aberta (sem comandos).
func (a *api) mailTerminal(ctx context.Context, s panel.Session, id, reason string, at time.Time) {
	if !a.notifyOn() {
		return
	}
	name := short(id)
	if inv, ok, err := a.store.LatestInventory(ctx, id); err == nil && ok && inv.OS.Hostname != "" {
		name = inv.OS.Hostname
	}
	n := a.notifier.n
	subject := "patchd: terminal aberto (" + name + ")"
	body := fmt.Sprintf("%s abriu o terminal do administrador na máquina %s (%s), em %s UTC.\n\nMotivo: %s\n\n"+
		"Os comandos e as saídas ficam em Administração › Auditoria › Terminal. Se não foi combinado, confira a trilha (terminal.abrir, terminal.comando).\n",
		s.Username, name, id, at.Format("02/01/2006 15:04"), reason)
	go func() {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := n.Send(c, subject, body); err != nil {
			a.logger.Error("aviso de terminal aberto não saiu", "machine_id", id, "error", err)
		}
	}()
}

// terminalFlash: o aviso da volta de um pedido do terminal.
func terminalFlash(q url.Values, b *pageBase) {
	msg, ok := map[string]string{
		"desligado": "O terminal está desligado em Configuração › Parâmetros.",
		"sem-chave": "O servidor está sem a chave de jobs; nada foi enviado.",
		"motivo":    fmt.Sprintf("Diga o motivo (de %d a %d caracteres).", minTerminalReason, maxTerminalReason),
		"auditoria": "A auditoria não gravou: nada foi feito. Tente de novo; se repetir, veja o log do servidor.",
		"comando":   fmt.Sprintf("Comando inválido: uma linha, até %d caracteres, com o interpretador e o tempo da lista.", jobs.MaxCommandLen),
		"sessao":    "A sessão venceu (15 minutos sem comando) ou não foi aberta: diga o motivo de novo.",
		"agente":    "O agente desta máquina não aceita o terminal (anterior a esta versão, ou instalado com -no-terminal).",
		"maquina":   "A máquina foi aposentada; nada foi enviado.",
	}[q.Get("terminal-erro")]
	if ok {
		b.Error = msg
	}
}

type terminalHistoryView struct {
	pageBase
	Rows              []terminalRow
	Machine, User     string
	Prev, Next, Shown string
}

// terminalHistoryPage: GET /painel/terminal. O histórico de todos os comandos (só admin), com
// o filtro por máquina (ID) e por administrador.
func (a *api) terminalHistoryPage(w http.ResponseWriter, r *http.Request, s panel.Session) {
	q := r.URL.Query()
	v := terminalHistoryView{pageBase: a.baseFor(r.Context(), s, "Terminal", "configuracao"), Machine: texto.Clip(strings.TrimSpace(q.Get("maquina")), 64),
		User: texto.Clip(strings.TrimSpace(q.Get("usuario")), 100)}
	v.Sub = "auditoria"
	page, _ := strconv.Atoi(q.Get("pagina"))
	page = max(page, 1)
	list, err := a.store.TerminalCommands(r.Context(), store.TerminalFilter{MachineID: v.Machine, Username: v.User, Limit: terminalPage + 1, Offset: (page - 1) * terminalPage})
	if err != nil {
		a.internalError(w, r, "listar os comandos do terminal", err)
		return
	}
	href := func(p int) string {
		u := url.Values{}
		if v.Machine != "" {
			u.Set("maquina", v.Machine)
		}
		if v.User != "" {
			u.Set("usuario", v.User)
		}
		if p > 1 {
			u.Set("pagina", strconv.Itoa(p))
		}
		return "/painel/terminal?" + u.Encode()
	}
	if len(list) > terminalPage {
		list, v.Next = list[:terminalPage], href(page+1)
	}
	if page > 1 {
		v.Prev = href(page - 1)
	}
	names, err := a.machineNames(r.Context())
	if err != nil {
		a.internalError(w, r, "listar máquinas", err)
		return
	}
	now := a.now()
	for _, c := range list {
		row := terminalRowOf(c, a.stamp, now)
		row.Machine, row.ShowExtra = dash(names[c.MachineID]), true
		v.Rows = append(v.Rows, row)
	}
	a.render(w, r, http.StatusOK, "terminal-historico", v)
}
