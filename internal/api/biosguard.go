package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tasantiago/patchd/internal/biospw"
	"github.com/tasantiago/patchd/internal/machinebox"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/store"
)

// Guarda da senha da BIOS (Aula 21.4; desenho em docs/bios-senha.md). O servidor sorteia a
// senha de cada máquina e guarda só o envelope fechado para a chave pública da guarda. A
// privada vem de PATCHD_BIOS_ESCROW_KEY_FILE (decisão D1: no servidor); com ela, um admin
// revela a senha de uma máquina, com motivo, auditoria e aviso por e-mail. A gravação na BIOS
// pelo agente é a Aula 21.5.

// escrowKey: a chave da guarda carregada no servidor.
type escrowKey struct {
	priv, pub []byte
	id        string
}

// WithBIOSEscrow liga a guarda com a chave privada (machinebox.ParsePrivate).
func WithBIOSEscrow(priv []byte) Option {
	return func(a *api) {
		pub, err := machinebox.Public(priv)
		if err != nil {
			return
		}
		a.escrow = &escrowKey{priv: priv, pub: pub, id: machinebox.KeyID(pub)}
	}
}

// Limites do motivo da revelação: o suficiente para um número de chamado ou uma frase.
const (
	minRevealReason = 10
	maxRevealReason = 200
)

// biosCard é o cartão "Senha da BIOS" na aba Hardware.
type biosCard struct {
	On         bool   // o servidor tem a chave de guarda
	KeyID      string // a impressão dela
	Admin      bool
	CanPrepare bool // admin, guarda ligada e nenhuma pendente
	Pending    int64
	Rows       []biosRow
	MinReason  int
	MaxReason  int
}

type biosRow struct {
	ID                        int64
	State, Level, Created, By string
	Reveals                   string
	CanReveal                 bool
	OtherKey                  string // fechada com outra chave de guarda (não abre com a atual)
}

var biosStateLabel = map[string]struct{ label, level string }{
	store.BIOSPending:   {"pendente: guardada, ainda não gravada na BIOS", "atencao"},
	store.BIOSActive:    {"ativa", ""},
	store.BIOSUncertain: {"incerta: a gravação saiu e a resposta não voltou", "erro"},
	store.BIOSFailed:    {"falhou: a máquina recusou", "erro"},
	store.BIOSReplaced:  {"substituída", ""},
}

// biosView monta o cartão (só na aba Hardware).
func (a *api) biosView(r *http.Request, id string, admin bool) (*biosCard, error) {
	list, err := a.store.BIOSPasswords(r.Context(), id)
	if err != nil {
		return nil, err
	}
	c := &biosCard{On: a.escrow != nil, Admin: admin, MinReason: minRevealReason, MaxReason: maxRevealReason}
	if c.On {
		c.KeyID = a.escrow.id
	}
	for _, b := range list {
		st := biosStateLabel[b.State]
		row := biosRow{ID: b.ID, State: st.label, Level: st.level, Created: b.CreatedAt.Format("02/01/2006 15:04") + " UTC", By: b.CreatedBy,
			Reveals: "nenhuma"}
		if b.Reveals > 0 && b.LastRevealedAt != nil {
			row.Reveals = fmt.Sprintf("%d (a última por %s, %s UTC)", b.Reveals, b.LastRevealedBy, b.LastRevealedAt.Format("02/01/2006 15:04"))
		}
		switch {
		case c.On && b.EscrowKeyID != c.KeyID:
			row.OtherKey = b.EscrowKeyID
		case c.On && admin && b.State != store.BIOSFailed:
			row.CanReveal = true
		}
		if b.State == store.BIOSPending {
			c.Pending = b.ID
		}
		c.Rows = append(c.Rows, row)
	}
	c.CanPrepare = c.On && admin && c.Pending == 0
	return c, nil
}

func biosBack(id, query string) string { return machinePath(id) + "?aba=hardware&" + query }

// prepareBIOSAction sorteia e guarda uma senha nova (pendente). Nada muda na máquina.
func (a *api) prepareBIOSAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	id := r.PathValue("id")
	if !machineIDPattern.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	if a.escrow == nil {
		http.Redirect(w, r, biosBack(id, "bios-erro=sem-chave"), http.StatusSeeOther)
		return
	}
	pw, err := biospw.Generate()
	if err != nil {
		a.internalError(w, r, "sortear a senha da BIOS", err)
		return
	}
	env, err := biospw.Seal(a.escrow.pub, id, pw)
	if err != nil {
		a.internalError(w, r, "fechar a senha da BIOS", err)
		return
	}
	b, created, err := a.store.CreateBIOSPassword(r.Context(), store.BIOSPassword{MachineID: id, Escrow: env,
		EscrowKeyID: a.escrow.id, CreatedAt: a.now(), CreatedBy: s.Username})
	if errors.Is(err, store.ErrNoMachine) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		a.internalError(w, r, "guardar a senha da BIOS", err)
		return
	}
	if !created {
		http.Redirect(w, r, biosBack(id, "bios=existente"), http.StatusSeeOther)
		return
	}
	a.logger.Info("senha da BIOS preparada (guardada, ainda não gravada)", "machine_id", id, "guarda", b.ID,
		"chave_guarda", a.escrow.id, "username", s.Username, "request_id", RequestID(r.Context()))
	a.audit(r, s, "bios.senha.preparar", id, fmt.Sprintf("guarda #%d, chave %s", b.ID, a.escrow.id))
	http.Redirect(w, r, biosBack(id, "bios=preparada"), http.StatusSeeOther)
}

// revealView: a página que mostra a senha, uma vez, para quem pediu.
type revealView struct {
	pageBase
	MachineID, Name string
	Guard           int64
	State           string
	Grouped         string
	Mailed          bool
}

// revealBIOSAction mostra a senha guardada. A auditoria é obrigatória: sem o registro gravado,
// a senha não aparece.
func (a *api) revealBIOSAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	id := r.PathValue("id")
	gid, err := strconv.ParseInt(r.PathValue("guarda"), 10, 64)
	if !machineIDPattern.MatchString(id) || err != nil || gid < 1 {
		http.NotFound(w, r)
		return
	}
	if a.escrow == nil {
		http.Redirect(w, r, biosBack(id, "bios-erro=sem-chave"), http.StatusSeeOther)
		return
	}
	reason := strings.Join(strings.Fields(r.PostFormValue("motivo")), " ")
	if n := utf8.RuneCountInString(reason); n < minRevealReason || n > maxRevealReason {
		http.Redirect(w, r, biosBack(id, "bios-erro=motivo"), http.StatusSeeOther)
		return
	}
	b, found, err := a.store.BIOSPasswordByID(r.Context(), gid)
	if err != nil {
		a.internalError(w, r, "ler a guarda da BIOS", err)
		return
	}
	if !found || b.MachineID != id {
		http.NotFound(w, r)
		return
	}
	if b.EscrowKeyID != a.escrow.id {
		http.Redirect(w, r, biosBack(id, "bios-erro=outra-chave"), http.StatusSeeOther)
		return
	}
	pw, err := biospw.Open(a.escrow.priv, id, b.Escrow)
	if err != nil {
		// Envelope que não abre com a chave certa: adulterado ou trocado no banco.
		a.logger.Error("senha da BIOS: o envelope não abre", "machine_id", id, "guarda", gid, "error", err,
			"request_id", RequestID(r.Context()))
		http.Redirect(w, r, biosBack(id, "bios-erro=envelope"), http.StatusSeeOther)
		return
	}
	now := a.now()
	ev := store.AuditEvent{At: now, Username: s.Username, Source: s.Source, Action: "bios.senha.revelar", Target: id,
		Detail: fmt.Sprintf("guarda #%d (%s); motivo: %s", gid, b.State, reason), Remote: remoteIP(r), RequestID: RequestID(r.Context())}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), auditTimeout)
	defer cancel()
	if err := a.store.RecordAudit(ctx, ev); err != nil {
		a.logger.Error("senha da BIOS não revelada: a auditoria não gravou", "machine_id", id, "guarda", gid, "error", err,
			"request_id", ev.RequestID)
		http.Redirect(w, r, biosBack(id, "bios-erro=auditoria"), http.StatusSeeOther)
		return
	}
	if err := a.store.MarkBIOSRevealed(ctx, gid, s.Username, now); err != nil {
		a.logger.Error("senha da BIOS: contagem de revelações não gravada", "guarda", gid, "error", err)
	}
	a.logger.Warn("senha da BIOS revelada", "machine_id", id, "guarda", gid, "username", s.Username,
		"remote", ev.Remote, "request_id", ev.RequestID)
	v := revealView{pageBase: a.baseFor(r.Context(), s, "Senha da BIOS", "inventario"), MachineID: id, Name: short(id), Guard: gid,
		State: biosStateLabel[b.State].label, Grouped: biospw.Grouped(pw)}
	if inv, ok, err := a.store.LatestInventory(r.Context(), id); err == nil && ok && inv.OS.Hostname != "" {
		v.Name = inv.OS.Hostname
	}
	v.Mailed = a.mailReveal(s, id, v.Name, gid, reason, now)
	w.Header().Set("Cache-Control", "no-store")
	a.render(w, r, http.StatusOK, "bios-revelada", v)
}

// mailReveal avisa a lista de notificações, em segundo plano (o e-mail não segura a página).
// Sem a senha, claro: quem, qual máquina, quando e o motivo.
func (a *api) mailReveal(s panel.Session, id, name string, gid int64, reason string, at time.Time) bool {
	if !a.notifyOn() {
		return false
	}
	n := a.notifier.n
	subject := "patchd: senha da BIOS revelada (" + name + ")"
	body := fmt.Sprintf("%s revelou a senha de administrador da BIOS da máquina %s (%s), guarda #%d, em %s UTC.\n\nMotivo: %s\n\n"+
		"Se não foi combinado, troque a senha desta máquina e confira a trilha de auditoria (bios.senha.revelar).\n",
		s.Username, name, id, gid, at.Format("02/01/2006 15:04"), reason)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := n.Send(ctx, subject, body); err != nil {
			a.logger.Error("aviso de senha da BIOS revelada não saiu", "machine_id", id, "guarda", gid, "error", err)
		}
	}()
	return true
}

// biosFlash: as mensagens do cartão (lista fixa).
func biosFlash(q map[string][]string, v *machineView) bool {
	get := func(k string) string {
		if l := q[k]; len(l) > 0 {
			return l[0]
		}
		return ""
	}
	switch get("bios") {
	case "preparada":
		v.Flash = "Senha da BIOS sorteada e guardada (pendente). Nada mudou na máquina: a gravação vem na Aula 21.5."
		return true
	case "existente":
		v.Flash = "Esta máquina já tem uma senha pendente; ela é a que será gravada. Nenhuma senha nova foi sorteada."
		return true
	}
	msg, ok := map[string]string{
		"sem-chave":   "A guarda está desligada: o servidor está sem PATCHD_BIOS_ESCROW_KEY_FILE.",
		"motivo":      fmt.Sprintf("Informe o motivo da revelação (de %d a %d caracteres): chamado, ordem de serviço, o que for.", minRevealReason, maxRevealReason),
		"outra-chave": "Esta senha foi guardada com outra chave de guarda, que o servidor não tem mais.",
		"envelope":    "O envelope desta senha não abre com a chave de guarda: ele foi alterado ou trocado no banco. Nada foi revelado; veja o log do servidor.",
		"auditoria":   "A senha não foi revelada porque a auditoria não gravou o pedido. Tente de novo; se continuar, veja o log do servidor.",
	}[get("bios-erro")]
	if ok {
		v.Error = msg
	}
	return ok
}
