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
	"github.com/tasantiago/patchd/internal/biosplain"
	"github.com/tasantiago/patchd/internal/biospw"
	"github.com/tasantiago/patchd/internal/biosstd"
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
	Summary              []biosSummaryRow // o resumo: os mesmos itens para qualquer modelo
	Vendor               string
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
	Choices              []biosChoice
	// Em linguagem simples (internal/biosplain): o título, a explicação e o valor. Plain falso:
	// fora do dicionário, a tela mostra o nome do fabricante.
	Title, Help, ValueText string
	Plain                  bool
}

// biosChoice: uma opção do seletor. Value é o valor exato da BIOS (o que o job grava); Label,
// a tradução.
type biosChoice struct{ Value, Label string }

// biosSummaryRow: uma linha do resumo da aba BIOS.
type biosSummaryRow struct {
	Item, Help, State, Level string
	Standard                 string // "conforme o padrão", "fora do padrão" ou "" (fora do padrão da BIOS)
	StdLevel                 string
	Detail                   string // o que a BIOS escreve, para quem precisar
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
	case fw.Settings == nil:
		c.Reason = "O inventário não trouxe as configurações da BIOS: o agente é anterior a esta versão (atualize) ou a BIOS não respondeu à leitura (veja os avisos da coleta)."
	}
	if fw != nil {
		c.Vendor = vendorLabel(fw.Interface)
		summary, err := a.biosSummary(ctx, id, fw)
		if err != nil {
			return nil, err
		}
		c.Summary = summary
	}
	if c.Reason == "" {
		c.Has, c.Total = true, len(fw.Settings)
		groups := map[string][]biosConfigRow{}
		for _, s := range fw.Settings {
			row := plainRow(s)
			if rule, ok := biosconfig.Editable(s); ok {
				if choices := rule.Choices(s); len(choices) > 1 || (len(choices) == 1 && choices[0] != s.Value) {
					row.Index, row.Risk, row.Note = c.EditableCount, rule.Risk, rule.Note
					if !slices.Contains(choices, s.Value) {
						// O valor atual fora das opções (ex.: suporte legado ligado): o seletor
						// começa nele, para "nada mudou" não virar uma alteração sem querer.
						choices = append([]string{s.Value}, choices...)
					}
					row.Choices = plainChoices(s.Name, choices)
					c.EditableCount++
					c.HasRisk = c.HasRisk || rule.Risk
					topic := categoryTopic[rule.Category]
					if e, ok := biosplain.Describe(s.Name); ok {
						topic = e.Topic
					}
					groups[topic] = append(groups[topic], row)
					continue
				}
			}
			c.Others = append(c.Others, row)
		}
		for _, topic := range biosplain.Topics {
			if rows := groups[topic]; len(rows) > 0 {
				c.Groups = append(c.Groups, biosConfigGroup{Title: topic, Rows: rows})
			}
		}
		// As traduzidas primeiro (na ordem da BIOS), as outras depois.
		slices.SortStableFunc(c.Others, func(x, y biosConfigRow) int { return btoi(y.Plain) - btoi(x.Plain) })
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
					v.Changes = append(v.Changes, biosplain.Change(ch.Name, ch.Value))
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
	jobID, err := a.issueBIOSSettings(ctx, id, facts, active, changes, s.Username)
	switch {
	case errors.Is(err, errBIOSEnvelope):
		back("bios-config-erro=envelope")
		return
	case errors.Is(err, jobs.ErrMachineUnavailable):
		http.NotFound(w, r)
		return
	case err != nil:
		a.internalError(w, r, "criar o job de configuração da BIOS", err)
		return
	}
	source := "BIOS sem senha"
	if active != nil {
		source = fmt.Sprintf("senha da guarda #%d", active.ID)
	}
	a.logger.Warn("alteração de configurações da BIOS pedida", "machine_id", id, "job_id", jobID, "alteracoes", len(changes),
		"username", s.Username, "request_id", RequestID(ctx))
	a.audit(r, s, "bios.config.job", id, fmt.Sprintf("job %d, %s; motivo: %s; %s", jobID, source, reason, strings.Join(audit, "; ")))
	back(fmt.Sprintf("bios-config=pedida&job=%d", jobID))
}

// issueBIOSSettings cria o job bios_settings com as alterações já conferidas. active: a senha
// em uso da guarda (nil: a BIOS está sem senha); ela vai fechada para a caixa da máquina.
func (a *api) issueBIOSSettings(ctx context.Context, id string, facts biosFacts, active *store.BIOSPassword, changes []biosconfig.Change, by string) (int64, error) {
	p := jobs.BIOSSettingsParams{Vendor: facts.Firmware.Interface, BoxKey: facts.BoxKeyID, Changes: changes}
	if active != nil {
		pw, err := biospw.Open(a.escrow.priv, id, active.Escrow)
		if err != nil {
			return 0, errBIOSEnvelope
		}
		sealed, err := machinebox.Seal(facts.BoxKey, machinebox.PurposeBIOSConfig, []byte(pw))
		if err != nil {
			return 0, fmt.Errorf("fechar a senha para a caixa: %w", err)
		}
		p.Password = base64.StdEncoding.EncodeToString(sealed)
	}
	raw, err := jobs.BIOSSettingsParamsJSON(p)
	if err != nil {
		return 0, fmt.Errorf("montar o job de configuração da BIOS: %w", err)
	}
	rec, err := jobs.Issue(ctx, a.store, a.jobKey, jobs.Request{MachineID: id, Type: jobs.TypeBIOSSettings, TTL: panelBIOSTTL, By: by, Params: raw}, a.now())
	if err != nil {
		return 0, err
	}
	return rec.ID, nil
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
		v.Flash = fmt.Sprintf("Alteração pedida: job %s. A máquina grava no próximo check-in (até %s); as mudanças valem no reinício seguinte.", get("job"), v.CheckinWait)
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

// categoryTopic: o assunto simples de cada categoria da lista fechada (para o que não está no
// dicionário).
var categoryTopic = map[string]string{
	biosconfig.CatPower: biosplain.TopicPower, biosconfig.CatBoot: biosplain.TopicBoot,
	biosconfig.CatVirtSec: biosplain.TopicSecurity, biosconfig.CatDevices: biosplain.TopicDevices,
}

// plainRow: a configuração com o título, a explicação e o valor em linguagem simples.
func plainRow(s protocol.BIOSSetting) biosConfigRow {
	row := biosConfigRow{Name: s.Name, Path: s.Path, Value: s.Value, Kind: biosKindLabel[s.Kind], ReadOnly: s.ReadOnly,
		Title: s.Name, ValueText: biosplain.Value(s.Name, s.Value)}
	if e, ok := biosplain.Describe(s.Name); ok {
		row.Title, row.Help, row.Plain = e.Title, e.Help, true
	}
	var possible []string
	for _, v := range s.Possible {
		possible = append(possible, biosplain.Value(s.Name, v))
	}
	row.Possible = strings.Join(possible, " · ")
	return row
}

// plainChoices: as opções do seletor com a tradução. Duas opções com a mesma tradução levam o
// valor da BIOS junto, para não ficarem iguais na tela.
func plainChoices(name string, values []string) []biosChoice {
	count := map[string]int{}
	out := make([]biosChoice, len(values))
	for i, v := range values {
		out[i] = biosChoice{Value: v, Label: biosplain.Value(name, v)}
		count[out[i].Label]++
	}
	for i := range out {
		if count[out[i].Label] > 1 && out[i].Label != out[i].Value {
			out[i].Label += " (" + out[i].Value + ")"
		}
	}
	return out
}

// biosSummary: os mesmos itens para qualquer marca e modelo, em linguagem simples, com o que o
// padrão da BIOS pede (docs/bios-config.md, seção 9).
func (a *api) biosSummary(ctx context.Context, id string, fw *protocol.Firmware) ([]biosSummaryRow, error) {
	var out []biosSummaryRow
	std := func(ok bool) (string, string) {
		if ok {
			return "conforme o padrão", "ok"
		}
		return "fora do padrão", "atencao"
	}

	// A senha de administrador.
	pw := biosSummaryRow{Item: "Senha de administrador", Help: "Protege a BIOS: sem ela, qualquer pessoa muda as configurações ao ligar o computador."}
	inGuard := false
	if a.escrow != nil {
		list, err := a.store.BIOSPasswords(ctx, id)
		if err != nil {
			return nil, err
		}
		for _, b := range list {
			inGuard = inGuard || (b.State == store.BIOSActive && b.EscrowKeyID == a.escrow.id)
		}
	}
	switch {
	case fw.AdminPassword == nil:
		pw.State = "Não se sabe (o inventário não diz)"
	case *fw.AdminPassword && inGuard:
		pw.State, pw.Level = "Definida e guardada pelo patchd", "ok"
		pw.Standard, pw.StdLevel = std(true)
	case *fw.AdminPassword:
		pw.State = "Definida, mas o patchd não a conhece"
		pw.Standard, pw.StdLevel = std(false)
	default:
		pw.State, pw.Level = "Sem senha", "atencao"
		pw.Standard, pw.StdLevel = std(false)
	}
	out = append(out, pw)

	// Os objetivos do padrão, pelas configurações que a BIOS informou.
	goal := func(item, help, g, okText, fixText string) {
		r := biosSummaryRow{Item: item, Help: help}
		if fw.Settings == nil {
			r.State = "Não se sabe (a BIOS não foi lida)"
			out = append(out, r)
			return
		}
		f := biosstd.Evaluate(g, fw.Settings)
		var ev []string
		for _, e := range f.Evidence {
			name, value, _ := strings.Cut(e, ": ")
			ev = append(ev, name+" = "+value)
		}
		if len(ev) > 0 {
			r.Detail = vendorLabel(fw.Interface) + ": " + strings.Join(ev, " · ")
		}
		switch f.State {
		case biosstd.StateOK:
			r.State, r.Level = okText, "ok"
			r.Standard, r.StdLevel = std(true)
		case biosstd.StateFix:
			r.State, r.Level = fixText, "atencao"
			r.Standard, r.StdLevel = std(false)
		case biosstd.StateStuck:
			r.State = "O patchd não consegue mudar nesta BIOS"
			r.Standard, r.StdLevel = std(false)
		default:
			r.State = "Este modelo não tem esta configuração"
		}
		out = append(out, r)
	}
	goal("Ligar pela rede", "Permite que a TI ligue o computador à distância para atualizar fora do horário.",
		biosstd.GoalWake, "Ligado", "Desligado (ou bloqueado pela economia de energia)")
	goal("Iniciar por pendrive ou HD externo", "Desligado, ninguém inicia outro sistema por um pendrive para burlar o Windows.",
		biosstd.GoalUSBBoot, "Bloqueado", "Permitido")

	// O resto, informativo.
	sb := biosSummaryRow{Item: "Inicialização segura (Secure Boot)", Help: "Impede o computador de iniciar um sistema não reconhecido."}
	switch {
	case fw.SecureBoot == nil:
		sb.State = "Não se sabe"
	case *fw.SecureBoot:
		sb.State, sb.Level = "Ligada", "ok"
	default:
		sb.State, sb.Level = "Desligada", "atencao"
	}
	mode := biosSummaryRow{Item: "Modo de inicialização", Help: "O Windows 11 exige o moderno (UEFI)."}
	switch fw.Mode {
	case protocol.FirmwareUEFI:
		mode.State, mode.Level = "Moderno (UEFI)", "ok"
	case protocol.FirmwareLegacy:
		mode.State, mode.Level = "Antigo (legado)", "atencao"
	default:
		mode.State = "Não se sabe"
	}
	tpm := biosSummaryRow{Item: "Chip de segurança (TPM)", Help: "Guarda as chaves do BitLocker e do Windows Hello. O Windows 11 exige a versão 2.0."}
	switch fw.TPM {
	case "2.0":
		tpm.State, tpm.Level = "Versão 2.0", "ok"
	case "1.2":
		tpm.State, tpm.Level = "Versão 1.2 (antiga)", "atencao"
	case protocol.TPMNone:
		tpm.State, tpm.Level = "Não tem, ou está desligado", "atencao"
	default:
		tpm.State = "Não se sabe"
	}
	out = append(out, sb, mode, tpm)
	for _, k := range []struct{ key, item string }{{biosplain.KeyVirt, "Virtualização"}, {biosplain.KeyPowerLoss, "Quando a energia volta depois de uma queda"}} {
		r := biosSummaryRow{Item: k.item, State: "Este modelo não informa"}
		for _, st := range fw.Settings {
			if e, ok := biosplain.Describe(st.Name); ok && e.Key == k.key {
				r.Help, r.State = e.Help, biosplain.Value(st.Name, st.Value)
				r.Detail = vendorLabel(fw.Interface) + ": " + st.Name + " = " + st.Value
				break
			}
		}
		if fw.Settings == nil {
			r.State = "Não se sabe (a BIOS não foi lida)"
		}
		out = append(out, r)
	}
	return out, nil
}
