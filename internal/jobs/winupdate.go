package jobs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/tasantiago/patchd/internal/protocol"
)

// TypeWinUpdate (Aula 16.1) instala atualizações do Windows pelo Windows Update Agent da
// própria máquina, da fonte que a política dela define (WSUS ou Windows Update). O job leva
// só identificadores: o UpdateID (GUID) que a busca da máquina mostrou e, para conferência,
// o número do KB. O agente procura de novo esse UpdateID, baixa e instala; nada de arquivo,
// comando ou endereço vem do servidor. O job nunca reinicia: o reinício é o job reboot.
const TypeWinUpdate = "windows_update"

// MaxWinUpdates limita um job: uma janela de manutenção não comporta muito mais.
const MaxWinUpdates = 20

// WinUpdate é uma atualização do Windows a instalar.
type WinUpdate struct {
	// ID: o UpdateID do WUA (GUID), o mesmo no WSUS, na Microsoft e no catálogo offline. Só
	// com a fonte Microsoft pode vir vazio: aí o agente procura pelo KB.
	ID string `json:"id"`
	KB string `json:"kb,omitempty"` // só dígitos; o agente confere que a atualização encontrada é deste KB
}

// Label é o que aparece no log e no resultado: o KB, ou o começo do UpdateID.
func (u WinUpdate) Label() string {
	if u.KB != "" || len(u.ID) < 8 {
		return "KB" + u.KB
	}
	return u.ID[:8]
}

// Fontes do job windows_update: a da política da máquina (WSUS quando configurado; o
// padrão, como sempre foi) ou direto da Microsoft (a fonte reserva). Agentes anteriores à
// reserva recusam o campo: o servidor só manda a Microsoft para quem a anuncia na busca.
const (
	WinSourcePolicy    = ""
	WinSourceMicrosoft = "microsoft"
)

// WinUpdateParams são os parâmetros do job windows_update.
type WinUpdateParams struct {
	Updates []WinUpdate `json:"updates"`
	Source  string      `json:"source,omitempty"`
	// Fallback (só com a fonte da política): se a política não entregar (a busca falha, o KB
	// não aparece, o download falha), o agente refaz o mesmo job direto pela Microsoft. Vai só
	// para agentes que anunciam (MicrosoftUpdate.InJob): os anteriores recusam o campo.
	Fallback bool `json:"fallback,omitempty"`
}

// SourceLabel: a fonte em palavras.
func (p WinUpdateParams) SourceLabel() string {
	if p.Source == WinSourceMicrosoft {
		return "Microsoft Update (fonte reserva)"
	}
	if p.Fallback {
		return "fonte da política, com a Microsoft de reserva"
	}
	return "fonte da política"
}

// O UpdateID entra no critério de busca do WUA (UpdateID='...'): só o formato de GUID passa,
// e nada de aspa chega lá.
var (
	updateGUID = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)
	kbNumber   = regexp.MustCompile(`^[0-9]{4,8}$`)
)

// ParseWinUpdateParams lê e confere os parâmetros.
func ParseWinUpdateParams(raw json.RawMessage) (WinUpdateParams, error) {
	var p WinUpdateParams
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return p, fmt.Errorf("parâmetros do windows_update ilegíveis: %w", err)
	}
	return p, p.Validate()
}

// Validate confere a lista.
func (p WinUpdateParams) Validate() error {
	if len(p.Updates) == 0 || len(p.Updates) > MaxWinUpdates {
		return fmt.Errorf("o windows_update leva de 1 a %d atualizações (veio %d)", MaxWinUpdates, len(p.Updates))
	}
	if p.Source != WinSourcePolicy && p.Source != WinSourceMicrosoft {
		return fmt.Errorf("fonte do windows_update desconhecida %q", p.Source)
	}
	if p.Fallback && p.Source != WinSourcePolicy {
		return errors.New("a reserva no job (fallback) só vai com a fonte da política")
	}
	seen := map[string]bool{}
	for _, u := range p.Updates {
		id := strings.ToLower(u.ID)
		if u.ID == "" && p.Source == WinSourceMicrosoft {
			// Na Microsoft, pelo KB: o UpdateID não estava em nenhuma busca da máquina.
			if !kbNumber.MatchString(u.KB) {
				return fmt.Errorf("sem UpdateID, o KB é obrigatório (veio %q)", u.KB)
			}
			id = "kb" + u.KB
			if seen[id] {
				return fmt.Errorf("KB repetido %s", u.KB)
			}
			seen[id] = true
			continue
		}
		switch {
		case !updateGUID.MatchString(u.ID):
			return fmt.Errorf("UpdateID inválido %q (esperado um GUID)", u.ID)
		case u.KB != "" && !kbNumber.MatchString(u.KB):
			return fmt.Errorf("KB inválido %q (só os dígitos: 5127648)", u.KB)
		case seen[id]:
			return fmt.Errorf("UpdateID repetido %s", u.ID)
		}
		seen[id] = true
	}
	return nil
}

// Labels devolve os rótulos, na ordem.
func (p WinUpdateParams) Labels() []string {
	out := make([]string, len(p.Updates))
	for i, u := range p.Updates {
		out[i] = u.Label()
	}
	return out
}

// Fontes do WUA na busca (as mesmas constantes do pacote patch, que importa este).
const (
	sourceWUADefault   = "wua-default"
	sourceWUAOffline   = "wua-offline"
	sourceWUAMicrosoft = "wua-microsoft" // a busca direta na Microsoft, quando a da política falhou
)

// WinUpdatesForKBs monta o job a partir da busca atual da máquina: para cada KB pedido, as
// atualizações pendentes com esse KB. Vale a fonte da política (wua-default); um KB que só
// o catálogo offline mostra entra também, e volta em offlineOnly: a fonte da máquina ainda
// não o oferece (WSUS sem aprovação), e o agente vai recusar. notFound: KBs que a busca não
// mostra pendentes (já instalados, ou de outra versão do Windows).
func WinUpdatesForKBs(rep protocol.PatchScanReport, kbs []string) (p WinUpdateParams, notFound, offlineOnly []string) {
	for _, kb := range kbs {
		kb = strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(kb)), "KB")
		var def, off []protocol.MissingUpdate
		for _, s := range rep.Scans {
			for _, u := range s.Missing {
				if !slices.Contains(u.KBs, kb) {
					continue
				}
				switch s.Source {
				case sourceWUADefault:
					def = append(def, u)
				case sourceWUAOffline:
					off = append(off, u)
				}
			}
		}
		use := def
		if len(use) == 0 {
			use = off
			if len(off) > 0 {
				offlineOnly = append(offlineOnly, kb)
			}
		}
		if len(use) == 0 {
			notFound = append(notFound, kb)
		}
		for _, u := range use {
			if !slices.ContainsFunc(p.Updates, func(w WinUpdate) bool { return strings.EqualFold(w.ID, u.ID) }) {
				p.Updates = append(p.Updates, WinUpdate{ID: u.ID, KB: kb})
			}
		}
	}
	return p, notFound, offlineOnly
}

// StillMissing (conferência no servidor) lista as atualizações do job que a busca da fonte
// do job ainda mostra pendentes. failed: a busca dessa fonte falhou e não prova nada. Job da
// Microsoft: vale a busca direta na Microsoft, se veio; senão, a da política.
func StillMissing(p WinUpdateParams, rep protocol.PatchScanReport) (still []string, failed string) {
	want := sourceWUADefault
	hasMS := slices.ContainsFunc(rep.Scans, func(s protocol.ScanResult) bool { return s.Source == sourceWUAMicrosoft && s.Missing != nil })
	if p.Source == WinSourceMicrosoft && slices.ContainsFunc(rep.Scans, func(s protocol.ScanResult) bool { return s.Source == sourceWUAMicrosoft }) {
		want = sourceWUAMicrosoft
	}
	// Com a reserva no job, a política falhando (o motivo de ir à Microsoft) não prova nada: vale
	// a busca direta na Microsoft, se veio.
	if p.Fallback && hasMS && PolicySourceProblem(rep) != "" {
		want = sourceWUAMicrosoft
	}
	for _, s := range rep.Scans {
		if s.Source != want {
			continue
		}
		if s.Missing == nil {
			return nil, "a busca do Windows Update falhou (" + s.Error + ")"
		}
		for _, w := range p.Updates {
			if slices.ContainsFunc(s.Missing, func(u protocol.MissingUpdate) bool {
				return w.ID != "" && strings.EqualFold(u.ID, w.ID) || w.ID == "" && slices.Contains(u.KBs, w.KB)
			}) {
				still = append(still, w.Label())
			}
		}
		return still, ""
	}
	return nil, "a busca não traz a fonte do Windows Update (wua-default)"
}

// PolicySourceProblem (Aula 16.2): por que a busca atual não serve para montar o job. A
// fonte da política (wua-default) falhou, ou não veio: o agente procura nela, e falharia na
// mesma busca. "" quando ela respondeu.
func PolicySourceProblem(rep protocol.PatchScanReport) string {
	for _, s := range rep.Scans {
		if s.Source == sourceWUADefault {
			if s.Missing == nil {
				return "a busca na fonte da política (WSUS ou Microsoft) falhou: " + s.Error
			}
			return ""
		}
	}
	return "a busca não traz a fonte da política (wua-default)"
}

// Campanha "atualizar o Windows" (Aula 16.4): a campanha leva KBs; cada máquina do grupo de implantação
// recebe os que corrigem alguma CVE aprovada dela e que a fonte da política mostra pendentes.

var kbTarget = regexp.MustCompile(`^(?:KB)?([0-9]{4,8})$`)

// NormalizeKB aceita "KB5127648", "kb5127648" ou "5127648" e devolve "KB5127648".
func NormalizeKB(s string) (string, bool) {
	m := kbTarget.FindStringSubmatch(strings.ToUpper(strings.TrimSpace(s)))
	if m == nil {
		return "", false
	}
	return "KB" + m[1], true
}

var kbInFixedIn = regexp.MustCompile(`KB([0-9]{4,8})`)

// KBsInPending devolve, dos KBs pedidos ("KB5127648"), os que aparecem no FixedIn de alguma
// pendência (já filtradas pela aprovação), na ordem pedida, só os dígitos.
func KBsInPending(items []protocol.PendingItem, want []string) []string {
	in := map[string]bool{}
	for _, it := range items {
		for _, m := range kbInFixedIn.FindAllStringSubmatch(it.FixedIn, -1) {
			in[m[1]] = true
		}
	}
	var out []string
	for _, w := range want {
		if kb, ok := NormalizeKB(w); ok && in[kb[2:]] && !slices.Contains(out, kb[2:]) {
			out = append(out, kb[2:])
		}
	}
	return out
}

// PolicyWinUpdates monta o job com os KBs que a fonte da política mostra pendentes na busca
// atual. skip (sem job): a fonte falhou, nenhum KB está nela, ou só no catálogo offline.
func PolicyWinUpdates(rep protocol.PatchScanReport, kbs []string) (WinUpdateParams, string) {
	if PolicySourceProblem(rep) != "" {
		return WinUpdateParams{}, SkipPolicyFailing
	}
	var use []string
	offline := false
	for _, kb := range kbs {
		_, notFound, off := WinUpdatesForKBs(rep, []string{kb})
		switch {
		case len(off) > 0:
			offline = true
		case len(notFound) == 0:
			use = append(use, kb)
		}
	}
	if len(use) == 0 {
		if offline {
			return WinUpdateParams{}, SkipOfflineOnly
		}
		return WinUpdateParams{}, SkipNotInScan
	}
	p, _, _ := WinUpdatesForKBs(rep, use)
	return p, ""
}

// Motivos do PolicyWinUpdates que a fonte reserva (a Microsoft) resolve.
const (
	SkipPolicyFailing = "fonte da política falhando"
	SkipOfflineOnly   = "só no catálogo offline"
	SkipNotInScan     = "KB fora da busca"
)

// FallbackReason: o motivo de ficar de fora é um dos que a Microsoft resolve.
func FallbackReason(skip string) bool {
	return skip == SkipPolicyFailing || skip == SkipOfflineOnly || skip == SkipNotInScan
}

// MicrosoftWinUpdates monta o job com a fonte Microsoft (a reserva): os mesmos KBs, cada um
// com o UpdateID que alguma busca da máquina mostrou (a direta na Microsoft, a do catálogo
// offline ou a da política: o UpdateID é o mesmo nas três) ou, sem nenhum, só o KB, que o
// agente procura na Microsoft. skip: o agente não sabe usar a Microsoft, ou a máquina não deixa.
func MicrosoftWinUpdates(rep protocol.PatchScanReport, kbs []string) (WinUpdateParams, string) {
	switch {
	case rep.MicrosoftUpdate == nil:
		return WinUpdateParams{}, "agente sem a fonte Microsoft (atualize o agente)"
	case !rep.MicrosoftUpdate.Available:
		reason := rep.MicrosoftUpdate.Reason
		if reason == "" {
			reason = "a máquina não permite"
		}
		return WinUpdateParams{}, "Microsoft bloqueada: " + reason
	}
	p := WinUpdateParams{Source: WinSourceMicrosoft}
	for _, kb := range kbs {
		kb = strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(kb)), "KB")
		if !kbNumber.MatchString(kb) {
			continue
		}
		var ids []string
		for _, src := range []string{sourceWUAMicrosoft, sourceWUAOffline, sourceWUADefault} {
			for _, s := range rep.Scans {
				if s.Source != src {
					continue
				}
				for _, u := range s.Missing {
					if slices.Contains(u.KBs, kb) && updateGUID.MatchString(u.ID) && !slices.Contains(ids, strings.ToLower(u.ID)) {
						ids = append(ids, strings.ToLower(u.ID))
					}
				}
			}
			if len(ids) > 0 {
				break
			}
		}
		if len(ids) == 0 {
			ids = []string{""} // só o KB
		}
		for _, id := range ids {
			if len(p.Updates) >= MaxWinUpdates {
				return p, ""
			}
			if id != "" && slices.ContainsFunc(p.Updates, func(w WinUpdate) bool { return strings.EqualFold(w.ID, id) }) {
				continue
			}
			p.Updates = append(p.Updates, WinUpdate{ID: id, KB: kb})
		}
	}
	if len(p.Updates) == 0 {
		return p, SkipNotInScan
	}
	return p, ""
}

// KBsInPendingAll: os KBs de todas as pendências (a campanha de todas as atualizações do
// sistema), sem repetir, na ordem das pendências (as exploradas primeiro).
func KBsInPendingAll(items []protocol.PendingItem) []string {
	var out []string
	for _, it := range items {
		for _, m := range kbInFixedIn.FindAllStringSubmatch(it.FixedIn, -1) {
			if !slices.Contains(out, m[1]) {
				out = append(out, m[1])
			}
		}
	}
	return out
}
