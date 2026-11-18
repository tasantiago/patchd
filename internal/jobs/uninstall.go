package jobs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/tasantiago/patchd/internal/protocol"
)

// TypeUninstall (Inventário › Programas instalados): o agente do Windows remove em silêncio
// um programa instalado para a máquina toda. O job leva a entrada de Programas e Recursos
// (nome, versão, a subchave Uninstall do HKLM e o método: msi, quiet ou inno), a mesma da
// regularização (ReplaceEntry); o comando sai do registro da própria máquina, lido na hora, e o
// agente confere que a entrada ainda é do programa (o nome) antes de executar. Instalação por
// usuário não sai (o agente roda como SYSTEM). Só vai a agentes que anunciam
// protocol.CapabilityUninstall.
const TypeUninstall = "desinstalar"

// UninstallParams: a entrada a remover.
type UninstallParams struct {
	ReplaceEntry
}

// Validate confere a entrada.
func (p UninstallParams) Validate() error { return p.ReplaceEntry.Validate() }

// ParseUninstallParams lê e confere os parâmetros.
func ParseUninstallParams(raw json.RawMessage) (UninstallParams, error) {
	var p UninstallParams
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return p, fmt.Errorf("parâmetros da remoção ilegíveis: %w", err)
	}
	return p, p.Validate()
}

// UninstallParamsFor: os parâmetros do job para a entrada do inventário, ou por que ela não
// sai pelo patchd (instalação por usuário, componente do sistema, sem desinstalação silenciosa).
func UninstallParamsFor(s protocol.Software) (UninstallParams, error) {
	switch {
	case s.Scope != "machine":
		return UninstallParams{}, errors.New("instalação por usuário: o patchd remove só o que está instalado para a máquina toda")
	case s.SystemComponent || s.IsUpdate:
		return UninstallParams{}, errors.New("componente do sistema ou atualização: sai com o programa principal")
	case s.Uninstall == "":
		return UninstallParams{}, errors.New("o programa não registrou desinstalação silenciosa (ou o agente ainda não informou como ele sai)")
	}
	p := UninstallParams{ReplaceEntry: ReplaceEntry{Name: strings.TrimSpace(s.Name), Version: s.Version, Key: s.Source, Method: s.Uninstall}}
	if s.Uninstall == protocol.UninstallMSI {
		p.ProductCode = s.ProductCode
	}
	return p, p.Validate()
}
