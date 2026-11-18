package jobs

import (
	"bytes"
	"encoding/json"
	"fmt"
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
