package api

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/biospw"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/machinebox"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

// Gravação da senha da BIOS (Aula 21.5; desenho em docs/bios-senha.md). O admin pede a
// gravação de uma senha pendente; o servidor abre a guarda, fecha a senha nova (e a atual,
// quando a troca a exige) para a caixa da máquina e cria o job bios_password, assinado. A
// resposta da máquina decide a guarda: ativa, falhou, incerta, ou de volta a pendente quando
// a BIOS nem foi tocada.

// panelBIOSTTL: o prazo do job. A máquina faz check-in de hora em hora; um dia cobre uma
// máquina desligada à noite.
const panelBIOSTTL = 24 * time.Hour

// Limites da senha atual digitada (a da frota, na primeira troca): o que a BIOS aceita, sem
// controle; o formato dela não é o da guarda.
const maxCurrentPassword = 64

// biosOutcome: o que o job diz sobre a senha. ok = false: o job ainda não terminou.
func biosOutcome(j store.JobRow) (state, result string, ok bool) {
	switch j.State {
	case jobs.StatePending, jobs.StateDelivered:
		return "", "", false
	case jobs.StateDone:
		return store.BIOSActive, j.Detail, true
	case jobs.StateFailed:
		if strings.Contains(j.Detail, jobs.BIOSUncertain) {
			return store.BIOSUncertain, j.Detail, true
		}
		return store.BIOSFailed, j.Detail, true
	case jobs.StateCanceled, jobs.StateExpired:
		if j.DeliveredAt != nil {
			// Entregue e sem resposta: a máquina pode ter gravado antes de cair.
			return store.BIOSUncertain, fmt.Sprintf("job %d %s depois da entrega, sem resposta da máquina", j.ID, j.State), true
		}
	}
	// Recusado, adiado, dispensado, ou expirado sem entrega: a BIOS não foi tocada.
	return store.BIOSPending, fmt.Sprintf("job %d %s: %s", j.ID, j.State, j.Detail), true
}

// settleBIOS fecha as gravações terminadas da máquina. É chamada quando o resultado chega, ao
// abrir o cartão e antes de uma gravação nova: um job expirado também fecha.
func (a *api) settleBIOS(ctx context.Context, machineID string) error {
	list, err := a.store.BIOSPasswords(ctx, machineID)
	if err != nil {
		return err
	}
	expired := false
	for _, b := range list {
		if b.State != store.BIOSPending || b.JobID == 0 {
			continue
		}
		if !expired {
			if _, err := a.store.ExpireJobs(ctx, a.now()); err != nil {
				return err
			}
			expired = true
		}
		j, found, err := a.store.JobForMachine(ctx, machineID, b.JobID)
		if err != nil {
			return err
		}
		state, result, done := store.BIOSPending, fmt.Sprintf("o job %d sumiu (retenção?)", b.JobID), true
		if found {
			state, result, done = biosOutcome(j)
		}
		if !done {
			continue
		}
		changed, err := a.store.SettleBIOSPassword(ctx, b.ID, b.JobID, state, result, a.now())
		if err != nil {
			return err
		}
		if changed {
			level := a.logger.Info
			if state != store.BIOSActive {
				level = a.logger.Warn
			}
			level("senha da BIOS: gravação fechada", "machine_id", machineID, "guarda", b.ID, "job_id", b.JobID, "estado", state)
		}
	}
	return nil
}

// judgeBIOS confere o "ok" do agente pelo inventário de depois: a senha de administrador tem
// de aparecer definida. Sem isso, a gravação é incerta (o agente diz que gravou, o inventário
// não mostra).
func (a *api) judgeBIOS(r *http.Request, job store.JobRow, res protocol.JobResult) (string, string) {
	agent := strings.TrimSpace(res.Detail)
	inv, found, err := a.store.LatestInventory(r.Context(), job.MachineID)
	if err != nil {
		return jobs.StateFailed, jobs.BIOSUncertain + "o servidor não conseguiu ler o inventário; agente: " + agent
	}
	if found && inv.Hardware != nil && inv.Hardware.Firmware != nil && inv.Hardware.Firmware.AdminPassword != nil && *inv.Hardware.Firmware.AdminPassword {
		return jobs.StateDone, "o inventário mostra a senha de administrador definida; agente: " + agent
	}
	return jobs.StateFailed, jobs.BIOSUncertain + "o agente informou sucesso, mas o inventário não mostra a senha definida; agente: " + agent
}

// biosFacts: o que a gravação precisa saber da máquina: o firmware (do inventário) e a chave
// da caixa (do estado).
type biosFacts struct {
	Firmware *protocol.Firmware
	BoxKey   []byte
	BoxKeyID string
}

func (a *api) biosFacts(ctx context.Context, id string) (biosFacts, error) {
	var f biosFacts
	inv, found, err := a.store.LatestInventory(ctx, id)
	if err != nil {
		return f, err
	}
	if found && inv.Hardware != nil {
		f.Firmware = inv.Hardware.Firmware
	}
	h, found, err := a.store.HostState(ctx, id)
	if err != nil {
		return f, err
	}
	if found && h.State != nil {
		if pub, err := machinebox.ParsePublic(h.State.BoxKey); err == nil {
			f.BoxKey, f.BoxKeyID = pub, machinebox.KeyID(pub)
		}
	}
	return f, nil
}

// blocker: o motivo que impede gravar nesta máquina ("" = pode).
func (f biosFacts) blocker() string {
	fw := f.Firmware
	switch {
	case fw == nil || fw.Interface == "":
		return "o inventário não mostra a interface do fabricante na BIOS desta máquina"
	case fw.Interface != protocol.FirmwareHP:
		return "por enquanto o patchd grava só na BIOS da HP; " + firmwareInterfaceLabel[fw.Interface] + " entra quando for conferida no laboratório"
	case fw.SureAdmin != nil && *fw.SureAdmin:
		return "HP Sure Admin ligado: esta BIOS só aceita mudanças por pacote assinado"
	case fw.AdminPassword == nil:
		return "o inventário não diz se a BIOS tem senha de administrador"
	case f.BoxKey == nil:
		return "a máquina não informou a chave de cifragem (agente anterior à v0.18.2)"
	}
	return ""
}

// writeBIOSAction cria o job que grava a senha pendente.
func (a *api) writeBIOSAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	id := r.PathValue("id")
	gid, err := strconv.ParseInt(r.PathValue("guarda"), 10, 64)
	if !machineIDPattern.MatchString(id) || err != nil || gid < 1 {
		http.NotFound(w, r)
		return
	}
	back := func(q string) { http.Redirect(w, r, biosBack(id, q), http.StatusSeeOther) }
	switch {
	case a.escrow == nil:
		back("bios-erro=sem-chave")
		return
	case a.jobKey == nil:
		back("bios-erro=sem-jobs")
		return
	}
	ctx := r.Context()
	if err := a.settleBIOS(ctx, id); err != nil {
		a.internalError(w, r, "fechar gravações da BIOS", err)
		return
	}
	list, err := a.store.BIOSPasswords(ctx, id)
	if err != nil {
		a.internalError(w, r, "ler a guarda da BIOS", err)
		return
	}
	var target, active *store.BIOSPassword
	for i := range list {
		switch b := &list[i]; {
		case b.State == store.BIOSUncertain:
			back("bios-erro=incerta")
			return
		case b.ID == gid:
			target = b
		case b.State == store.BIOSActive:
			active = b
		}
	}
	if target == nil {
		http.NotFound(w, r)
		return
	}
	if target.State != store.BIOSPending || target.JobID != 0 {
		back("bios-erro=nao-pendente")
		return
	}
	facts, err := a.biosFacts(ctx, id)
	if err != nil {
		a.internalError(w, r, "ler o firmware e a caixa", err)
		return
	}
	if facts.blocker() != "" {
		back("bios-erro=bloqueada")
		return
	}
	// A senha atual: a ativa da guarda; sem ela, com a BIOS já com senha, a que o admin digitou
	// (a da frota, na primeira troca). BIOS sem senha: não há atual.
	var current []byte
	source := "definição (a BIOS estava sem senha)"
	if *facts.Firmware.AdminPassword {
		switch {
		case active != nil && active.EscrowKeyID == a.escrow.id:
			pw, err := biospw.Open(a.escrow.priv, id, active.Escrow)
			if err != nil {
				back("bios-erro=envelope")
				return
			}
			current, source = []byte(pw), fmt.Sprintf("troca (senha atual: a da guarda #%d)", active.ID)
		default:
			typed := r.PostFormValue("senha_atual")
			if typed == "" || len(typed) > maxCurrentPassword || strings.ContainsFunc(typed, func(c rune) bool { return c < 0x20 || c == 0x7f }) {
				back("bios-erro=senha-atual")
				return
			}
			current, source = []byte(typed), "troca (senha atual informada pelo admin)"
		}
	}
	newPW, err := biospw.Open(a.escrow.priv, id, target.Escrow)
	if err != nil {
		back("bios-erro=envelope")
		return
	}
	seal := func(purpose string, pw []byte) (string, error) {
		b, err := machinebox.Seal(facts.BoxKey, purpose, pw)
		return base64.StdEncoding.EncodeToString(b), err
	}
	p := jobs.BIOSPasswordParams{Guard: gid, Vendor: facts.Firmware.Interface, BoxKey: facts.BoxKeyID}
	if p.New, err = seal(machinebox.PurposeBIOS, []byte(newPW)); err == nil && current != nil {
		p.Current, err = seal(machinebox.PurposeBIOSCurrent, current)
	}
	clear(current)
	if err != nil {
		a.internalError(w, r, "fechar a senha para a caixa", err)
		return
	}
	raw, err := jobs.BIOSPasswordParamsJSON(p)
	if err != nil {
		a.internalError(w, r, "montar o job da BIOS", err)
		return
	}
	rec, err := jobs.Issue(ctx, a.store, a.jobKey, jobs.Request{MachineID: id, Type: jobs.TypeBIOSPassword, TTL: panelBIOSTTL,
		By: s.Username, Params: raw}, a.now())
	if errors.Is(err, jobs.ErrMachineUnavailable) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		a.internalError(w, r, "criar o job da BIOS", err)
		return
	}
	attached, err := a.store.AttachBIOSJob(ctx, gid, rec.ID, a.now())
	if err != nil || !attached {
		// Dois pedidos ao mesmo tempo: o segundo job não segue.
		_, _ = a.store.CancelJob(ctx, rec.ID, s.Username)
		if err != nil {
			a.internalError(w, r, "ligar o job à guarda", err)
			return
		}
		back("bios-erro=nao-pendente")
		return
	}
	a.logger.Warn("gravação da senha da BIOS pedida", "machine_id", id, "guarda", gid, "job_id", rec.ID, "username", s.Username,
		"request_id", RequestID(ctx))
	a.audit(r, s, "bios.senha.gravar", id, fmt.Sprintf("guarda #%d, job %d, %s", gid, rec.ID, source))
	back(fmt.Sprintf("bios=gravando&job=%d", rec.ID))
}

// resolveBIOSAction decide uma senha incerta, depois que alguém conferiu na BIOS.
func (a *api) resolveBIOSAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	id := r.PathValue("id")
	gid, err := strconv.ParseInt(r.PathValue("guarda"), 10, 64)
	if !machineIDPattern.MatchString(id) || err != nil || gid < 1 {
		http.NotFound(w, r)
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
	var valid bool
	switch r.PostFormValue("valor") {
	case "nova":
		valid = true
	case "anterior":
	default:
		http.Redirect(w, r, biosBack(id, "bios-erro=resolver"), http.StatusSeeOther)
		return
	}
	result := "conferida na BIOS por " + s.Username + ": a anterior continua valendo"
	if valid {
		result = "conferida na BIOS por " + s.Username + ": a nova vale"
	}
	changed, err := a.store.ResolveBIOSPassword(r.Context(), gid, valid, result, a.now())
	if err != nil {
		a.internalError(w, r, "resolver a guarda da BIOS", err)
		return
	}
	if !changed {
		http.Redirect(w, r, biosBack(id, "bios-erro=resolver"), http.StatusSeeOther)
		return
	}
	a.audit(r, s, "bios.senha.resolver", id, fmt.Sprintf("guarda #%d: %s", gid, result))
	http.Redirect(w, r, biosBack(id, "bios=resolvida"), http.StatusSeeOther)
}
