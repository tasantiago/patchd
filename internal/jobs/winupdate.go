package jobs

import (
	"bytes"
	"encoding/json"
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
	ID string `json:"id"`           // UpdateID do WUA (GUID)
	KB string `json:"kb,omitempty"` // só dígitos; o agente confere que a atualização encontrada é deste KB
}

// Label é o que aparece no log e no resultado: o KB, ou o começo do UpdateID.
func (u WinUpdate) Label() string {
	if u.KB != "" {
		return "KB" + u.KB
	}
	return u.ID[:8]
}

// WinUpdateParams são os parâmetros do job windows_update.
type WinUpdateParams struct {
	Updates []WinUpdate `json:"updates"`
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
	seen := map[string]bool{}
	for _, u := range p.Updates {
		id := strings.ToLower(u.ID)
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
	sourceWUADefault = "wua-default"
	sourceWUAOffline = "wua-offline"
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
// da política ainda mostra pendentes. failed: a busca dessa fonte falhou e não prova nada.
func StillMissing(p WinUpdateParams, rep protocol.PatchScanReport) (still []string, failed string) {
	for _, s := range rep.Scans {
		if s.Source != sourceWUADefault {
			continue
		}
		if s.Missing == nil {
			return nil, "a busca do Windows Update falhou (" + s.Error + ")"
		}
		for _, w := range p.Updates {
			if slices.ContainsFunc(s.Missing, func(u protocol.MissingUpdate) bool { return strings.EqualFold(u.ID, w.ID) }) {
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
