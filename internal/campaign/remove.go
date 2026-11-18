package campaign

import (
	"context"
	"errors"
	"slices"
	"strings"
	"unicode"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
)

// RemoveTarget é o programa que a campanha de remoção (jobs.TypeUninstall) tira do parque: os
// nomes exatos de Programas e Recursos (os de Inventário › Programas instalados), o fabricante
// e, opcionalmente, só algumas versões.
type RemoveTarget struct {
	Program   string   `json:"program"`            // o nome mostrado (LibreOffice)
	Publisher string   `json:"publisher"`          // o fabricante, como no inventário ("" vale)
	Names     []string `json:"names"`              // os nomes exatos da entrada
	Versions  []string `json:"versions,omitempty"` // vazio: todas as versões
}

// Limites do alvo.
const (
	MaxRemoveNames    = 50
	MaxRemoveVersions = 50
)

func printableText(s string, max int) bool {
	return len(s) <= max && strings.IndexFunc(s, func(r rune) bool { return !unicode.IsPrint(r) }) < 0
}

// Validate confere o alvo.
func (t RemoveTarget) Validate() error {
	switch {
	case strings.TrimSpace(t.Program) == "" || !printableText(t.Program, 200):
		return errors.New("o programa a remover não tem nome")
	case !printableText(t.Publisher, 200):
		return errors.New("fabricante inválido")
	case len(t.Names) == 0 || len(t.Names) > MaxRemoveNames:
		return errors.New("o programa a remover leva de 1 a 50 nomes do inventário")
	case len(t.Versions) > MaxRemoveVersions:
		return errors.New("versões demais")
	}
	for _, n := range t.Names {
		if strings.TrimSpace(n) == "" || !printableText(n, 260) {
			return errors.New("nome do inventário inválido")
		}
	}
	for _, v := range t.Versions {
		if !printableText(v, 64) {
			return errors.New("versão inválida")
		}
	}
	return nil
}

// Matches: a entrada do inventário é do programa (e de uma das versões, se a campanha escolheu).
func (t RemoveTarget) Matches(s protocol.Software) bool {
	return slices.Contains(t.Names, s.Name) && s.Publisher == t.Publisher && (len(t.Versions) == 0 || slices.Contains(t.Versions, s.Version))
}

// Label: "LibreOffice (The Document Foundation), versões 24.2.5.2, 25.2.1.1".
func (t RemoveTarget) Label() string {
	s := t.Program
	if t.Publisher != "" {
		s += " (" + t.Publisher + ")"
	}
	if len(t.Versions) == 0 {
		return s + ", todas as versões"
	}
	return s + ", versões " + strings.Join(t.Versions, ", ")
}

// uninstallJob: o job de remoção da máquina, pela primeira entrada do programa que sai pelo
// patchd. Sem job, skip diz por quê (sem o programa não é falha: a máquina só não tem o que
// remover).
func (e *Engine) uninstallJob(ctx context.Context, c *Campaign, id string) (*jobs.UninstallParams, string, error) {
	if e.Inventory == nil || c.Remove == nil {
		return nil, "sem inventário", nil
	}
	inv, ok, err := e.Inventory(ctx, id)
	switch {
	case err != nil:
		return nil, "", err
	case !ok:
		return nil, "sem inventário", nil
	case inv.OS.Family != "windows":
		return nil, "não é Windows", nil
	}
	skip := "sem o programa"
	for _, s := range inv.Software {
		if !c.Remove.Matches(s) {
			continue
		}
		p, err := jobs.UninstallParamsFor(s)
		if err != nil {
			if s.Scope != "machine" {
				skip = "instalação por usuário"
			} else {
				skip = "sem desinstalação silenciosa"
			}
			continue
		}
		if e.CanUninstall == nil {
			return nil, "agente sem a remoção", nil
		}
		can, err := e.CanUninstall(ctx, id)
		if err != nil {
			return nil, "", err
		}
		if !can {
			return nil, "agente sem a remoção (atualize o agente)", nil
		}
		return &p, "", nil
	}
	return nil, skip, nil
}
