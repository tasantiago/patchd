package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/tasantiago/patchd/internal/biosconfig"
	"github.com/tasantiago/patchd/internal/biospw"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/machinebox"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

// Configurações da BIOS (docs/bios-config.md): a aba BIOS do equipamento mostra todas as
// configurações que o agente informou e deixa o admin alterar as da lista fechada
// (internal/biosconfig). A alteração vira o job bios_settings, assinado, com a senha em uso
// tirada da guarda e fechada para a caixa da máquina.

// biosConfigRecent: quantos jobs de configuração a aba mostra.
const biosConfigRecent = 5

type biosConfigCard struct {
	Has                  bool   // o inventário trouxe as configurações
	Reason               string // por que não há configurações
	Total, EditableCount int
	Groups               []biosConfigGroup // as editáveis, por categoria
	Others               []biosConfigRow   // o resto, só leitura no patchd
	Admin, CanEdit       bool
	Blocker              string // por que não dá para alterar agora ("" = dá)
	HasRisk              bool
	Recent               []biosConfigJob
	MinReason, MaxReason int
}

type biosConfigGroup struct {
	Title string
	Rows  []biosConfigRow
}

type biosConfigRow struct {
	Index                int
	Name, Path, Value    string
	Kind, Possible, Note string
	ReadOnly, Risk       bool
	Choices              []string
}

type biosConfigJob struct {
	ID                             int64
	State, StateLabel, Created, By string
	Detail                         string
	Changes                        []string
}

var biosKindLabel = map[string]string{
	protocol.BIOSKindEnum: "escolha", protocol.BIOSKindString: "texto", protocol.BIOSKindInteger: "número", protocol.BIOSKindList: "lista",
}

// biosConfigBack: a aba BIOS, com o aviso.
func biosConfigBack(id, query string) string { return machinePath(id) + "?aba=bios&" + query }

// biosConfigView monta a aba BIOS.
func (a *api) biosConfigView(r *http.Request, id string, admin bool) (*biosConfigCard, error) {
	ctx := r.Context()
	c := &biosConfigCard{Admin: admin, MinReason: minReasonLength, MaxReason: maxReasonLength}
	facts, err := a.biosFacts(ctx, id)
	if err != nil {
		return nil, err
	}
	fw := facts.Firmware
	switch {
	case fw == nil || fw.Interface == "":
		c.Reason = "O inventário desta máquina não mostra a interface do fabricante na BIOS."
	case fw.Interface != protocol.FirmwareHP:
		c.Reason = "Por enquanto o patchd lê as configurações só da BIOS da HP; " + firmwareInterfaceLabel[fw.Interface] + " entra quando for conferida no laboratório."
	case fw.Settings == nil:
		c.Reason = "O inventário não trouxe as configurações da BIOS: o agente é anterior a esta versão (atualize) ou a BIOS não respondeu à leitura (veja os avisos da coleta)."
	}
	if c.Reason == "" {
		c.Has, c.Total = true, len(fw.Settings)
		groups := map[string][]biosConfigRow{}
		for _, s := range fw.Settings {
			row := biosConfigRow{Name: s.Name, Path: s.Path, Value: s.Value, Kind: biosKindLabel[s.Kind],
				Possible: strings.Join(s.Possible, " · "), ReadOnly: s.ReadOnly}
			if rule, ok := biosconfig.Editable(s); ok {
				if choices := rule.Choices(s); len(choices) > 1 || (len(choices) == 1 && choices[0] != s.Value) {
					row.Index, row.Choices, row.Risk, row.Note = c.EditableCount, choices, rule.Risk, rule.Note
					if !slices.Contains(row.Choices, s.Value) {
						// O valor atual fora das opções (ex.: suporte legado ligado): o seletor
						// começa nele, para "nada mudou" não virar uma alteração sem querer.
						row.Choices = append([]string{s.Value}, row.Choices...)
					}
					c.EditableCount++
					c.HasRisk = c.HasRisk || rule.Risk
					groups[rule.Category] = append(groups[rule.Category], row)
					continue
				}
			}
			c.Others = append(c.Others, row)
		}
		for _, cat := range biosconfig.Categories {
			if rows := groups[cat]; len(rows) > 0 {
				c.Groups = append(c.Groups, biosConfigGroup{Title: cat, Rows: rows})
			}
		}
	}
	if c.Recent, err = a.biosConfigJobs(ctx, id); err != nil {
		return nil, err
	}
	if admin && c.Has && c.EditableCount > 0 {
		blocker, _, err := a.biosConfigBlocker(ctx, id, facts)
		if err != nil {
			return nil, err
		}
		c.Blocker, c.CanEdit = blocker, blocker == ""
	}
	return c, nil
}

// biosConfigJobs: os jobs de configuração mais recentes da máquina, com o que pediam.
func (a *api) biosConfigJobs(ctx context.Context, id string) ([]biosConfigJob, error) {
	list, err := a.store.Jobs(ctx, id, 200)
	if err != nil {
		return nil, err
	}
	var out []biosConfigJob
	for _, j := range list {
		if j.Type != jobs.TypeBIOSSettings {
			continue
		}
		v := biosConfigJob{ID: j.ID, State: j.State, StateLabel: stateLabel(j.State), Created: a.stamp(j.CreatedAt),
			By: j.CreatedBy, Detail: j.Detail}
		if full, found, err := a.store.JobForMachine(ctx, id, j.ID); err == nil && found {
			if p, err := jobPayloadBIOSSettings(full.Payload); err == nil {
				for _, ch := range p.Changes {
					v.Changes = append(v.Changes, ch.Name+" → "+ch.Value)
				}
			}
		}
		out = append(out, v)
		if len(out) == biosConfigRecent {
			break
		}
	}
	return out, nil
}

// jobPayloadBIOSSettings lê os parâmetros do job guardado (só nomes e valores interessam; a
// senha fechada nunca é mostrada).
func jobPayloadBIOSSettings(payload string) (jobs.BIOSSettingsParams, error) {
	var j jobs.Job
	if err := json.Unmarshal([]byte(payload), &j); err != nil {
		return jobs.BIOSSettingsParams{}, err
	}
	return jobs.ParseBIOSSettingsParams(j.Params)
}

// biosConfigBlocker: por que não dá para alterar agora ("" = dá), e a senha ativa da guarda
// quando a BIOS tem senha.
func (a *api) biosConfigBlocker(ctx context.Context, id string, facts biosFacts) (string, *store.BIOSPassword, error) {
	if a.jobKey == nil {
		return "o servidor está sem a chave de jobs (PATCHD_JOB_SIGNING_KEY_FILE)", nil, nil
	}
	if b := facts.blocker(); b != "" {
		return b, nil, nil
	}
	list, err := a.store.Jobs(ctx, id, 200)
	if err != nil {
		return "", nil, err
	}
	for _, j := range list {
		if (j.Type == jobs.TypeBIOSSettings || j.Type == jobs.TypeBIOSPassword) && !jobs.Final(j.State) {
			return fmt.Sprintf("há uma alteração da BIOS em andamento (job %d): espere a resposta da máquina", j.ID), nil, nil
		}
	}
	if !*facts.Firmware.AdminPassword {
		return "", nil, nil // BIOS sem senha: nada a fechar
	}
	if a.escrow == nil {
		return "a BIOS tem senha e a guarda está desligada (PATCHD_BIOS_ESCROW_KEY_FILE)", nil, nil
	}
	if err := a.settleBIOS(ctx, id); err != nil {
		return "", nil, err
	}
	guards, err := a.store.BIOSPasswords(ctx, id)
	if err != nil {
		return "", nil, err
	}
	var active *store.BIOSPassword
	for i := range guards {
		switch g := &guards[i]; g.State {
		case store.BIOSUncertain:
			return "há uma senha incerta na guarda: confira na BIOS e resolva no cartão Senha da BIOS (aba Hardware)", nil, nil
		case store.BIOSActive:
			active = g
		}
	}
	switch {
	case active == nil:
		return "a BIOS tem senha, mas a guarda não tem a senha em uso: grave uma senha pela guarda (aba Hardware, cartão Senha da BIOS) antes", nil, nil
	case active.EscrowKeyID != a.escrow.id:
		return fmt.Sprintf("a senha em uso foi guardada com outra chave de guarda (%s)", active.EscrowKeyID), nil, nil
	}
	return "", active, nil
}

// configureBIOSAction cria o job bios_settings com o que mudou no formulário da aba BIOS.
func (a *api) configureBIOSAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	id := r.PathValue("id")
	if !machineIDPattern.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	back := func(q string) { http.Redirect(w, r, biosConfigBack(id, q), http.StatusSeeOther) }
	ctx := r.Context()
	reason := strings.TrimSpace(r.PostFormValue("motivo"))
	if n := utf8.RuneCountInString(reason); n < minReasonLength || n > maxReasonLength {
		back("bios-config-erro=motivo")
		return
	}
	facts, err := a.biosFacts(ctx, id)
	if err != nil {
		a.internalError(w, r, "ler o firmware e a caixa", err)
		return
	}
	blocker, active, err := a.biosConfigBlocker(ctx, id, facts)
	if err != nil {
		a.internalError(w, r, "conferir a BIOS", err)
		return
	}
	if blocker != "" {
		back("bios-config-erro=bloqueada")
		return
	}
	current := make(map[string]protocol.BIOSSetting, len(facts.Firmware.Settings))
	for _, st := range facts.Firmware.Settings {
		current[st.Name] = st
	}
	// O formulário manda n<i> (o nome) e v<i> (o valor escolhido) para cada configuração
	// editável; só o que difere do inventário vira alteração, e cada uma é conferida de novo.
	var changes []biosconfig.Change
	var audit []string
	risk, seen := false, map[string]bool{}
	for i := 0; i < protocol.MaxBIOSSettings; i++ {
		name, ok := r.PostForm["n"+strconv.Itoa(i)]
		if !ok {
			break
		}
		value := r.PostFormValue("v" + strconv.Itoa(i))
		st, found := current[firstOf(name)]
		if found && seen[st.Name] {
			back("bios-config-erro=valor")
			return
		}
		seen[st.Name] = true
		if !found {
			back("bios-config-erro=mudou")
			return
		}
		if value == st.Value {
			continue
		}
		ch := biosconfig.Change{Name: st.Name, Value: value}
		if biosconfig.CheckChange(st, ch) != nil {
			back("bios-config-erro=valor")
			return
		}
		rule, _ := biosconfig.Lookup(st.Name)
		risk = risk || rule.Risk
		changes = append(changes, ch)
		audit = append(audit, fmt.Sprintf("%s: %s → %s", st.Name, st.Value, value))
	}
	switch {
	case len(changes) == 0:
		back("bios-config-erro=nada")
		return
	case len(changes) > biosconfig.MaxChanges:
		back("bios-config-erro=muitas")
		return
	case risk && r.PostFormValue("confirmo") != "1":
		back("bios-config-erro=confirmar")
		return
	}
	p := jobs.BIOSSettingsParams{Vendor: facts.Firmware.Interface, BoxKey: facts.BoxKeyID, Changes: changes}
	if active != nil {
		pw, err := biospw.Open(a.escrow.priv, id, active.Escrow)
		if err != nil {
			back("bios-config-erro=envelope")
			return
		}
		sealed, err := machinebox.Seal(facts.BoxKey, machinebox.PurposeBIOSConfig, []byte(pw))
		if err != nil {
			a.internalError(w, r, "fechar a senha para a caixa", err)
			return
		}
		p.Password = base64.StdEncoding.EncodeToString(sealed)
	}
	raw, err := jobs.BIOSSettingsParamsJSON(p)
	if err != nil {
		a.internalError(w, r, "montar o job de configuração da BIOS", err)
		return
	}
	rec, err := jobs.Issue(ctx, a.store, a.jobKey, jobs.Request{MachineID: id, Type: jobs.TypeBIOSSettings, TTL: panelBIOSTTL,
		By: s.Username, Params: raw}, a.now())
	if errors.Is(err, jobs.ErrMachineUnavailable) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		a.internalError(w, r, "criar o job de configuração da BIOS", err)
		return
	}
	source := "BIOS sem senha"
	if active != nil {
		source = fmt.Sprintf("senha da guarda #%d", active.ID)
	}
	a.logger.Warn("alteração de configurações da BIOS pedida", "machine_id", id, "job_id", rec.ID, "alteracoes", len(changes),
		"username", s.Username, "request_id", RequestID(ctx))
	a.audit(r, s, "bios.config.job", id, fmt.Sprintf("job %d, %s; motivo: %s; %s", rec.ID, source, reason, strings.Join(audit, "; ")))
	back(fmt.Sprintf("bios-config=pedida&job=%d", rec.ID))
}

func firstOf(l []string) string {
	if len(l) == 0 {
		return ""
	}
	return l[0]
}

// judgeBIOSSettings confere o "ok" do agente pelo inventário de depois. A BIOS respondeu 0 a
// todas; o que o inventário já mostra com o valor novo sai conferido, o resto fica anotado
// (em vários modelos o valor só aparece depois do reinício).
func (a *api) judgeBIOSSettings(r *http.Request, job store.JobRow, res protocol.JobResult) (string, string) {
	agent := strings.TrimSpace(res.Detail)
	p, err := jobPayloadBIOSSettings(job.Payload)
	if err != nil {
		return jobs.StateDone, "agente: " + agent
	}
	inv, found, err := a.store.LatestInventory(r.Context(), job.MachineID)
	if err != nil || !found || inv.Hardware == nil || inv.Hardware.Firmware == nil {
		return jobs.StateDone, "o servidor não conseguiu conferir no inventário; agente: " + agent
	}
	now := map[string]string{}
	for _, s := range inv.Hardware.Firmware.Settings {
		now[s.Name] = s.Value
	}
	var ok, later []string
	for _, c := range p.Changes {
		if now[c.Name] == c.Value {
			ok = append(ok, c.Name)
		} else {
			later = append(later, c.Name)
		}
	}
	detail := ""
	if len(ok) > 0 {
		detail = "conferido no inventário: " + strings.Join(ok, ", ") + "; "
	}
	if len(later) > 0 {
		detail += "o inventário ainda mostra o valor anterior (vale depois do reinício): " + strings.Join(later, ", ") + "; "
	}
	return jobs.StateDone, detail + "agente: " + agent
}

// biosConfigFlash: os avisos que voltam da aba BIOS.
func biosConfigFlash(q map[string][]string, v *machineView) bool {
	get := func(k string) string {
		if l := q[k]; len(l) > 0 {
			return l[0]
		}
		return ""
	}
	if get("bios-config") == "pedida" {
		v.Flash = fmt.Sprintf("Alteração pedida: job %s. A máquina grava no próximo check-in (até 1 h); as mudanças valem no reinício seguinte.", get("job"))
		return true
	}
	msg, ok := map[string]string{
		"motivo":    fmt.Sprintf("Informe o motivo da alteração (de %d a %d caracteres): chamado, ordem de serviço, o que for.", minReasonLength, maxReasonLength),
		"bloqueada": "A alteração não pode seguir nesta máquina: o motivo está na aba BIOS.",
		"mudou":     "As configurações da máquina mudaram desde que a página abriu: confira os valores e peça de novo.",
		"valor":     "Um dos valores escolhidos não é aceito por esta BIOS ou pela lista fechada do patchd.",
		"nada":      "Nenhum valor mudou: escolha ao menos uma configuração diferente da atual.",
		"muitas":    fmt.Sprintf("Altere no máximo %d configurações por vez.", biosconfig.MaxChanges),
		"confirmar": "Há alteração de risco alto (segurança, Wi-Fi, Bluetooth ou USB): marque a confirmação para seguir.",
		"envelope":  "O envelope da senha em uso não abre com a chave de guarda: nada foi pedido; veja o log do servidor.",
	}[get("bios-config-erro")]
	if ok {
		v.Error = msg
	}
	return ok
}
