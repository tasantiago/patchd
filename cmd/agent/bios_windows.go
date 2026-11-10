package main

import (
	"context"
	"log/slog"

	"github.com/tasantiago/patchd/internal/bioslegacy"
	"github.com/tasantiago/patchd/internal/bioswrite"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/machinebox"
)

// biosRunner: o executor do job bios_password (Aula 21.5), com a caixa desta máquina. Sem a
// caixa, nil: o agente recusa esses jobs, e o servidor não os cria (o painel não vê a chave).
func biosRunner(logger *slog.Logger, boxPriv []byte, machineID string) func(context.Context, int64, jobs.BIOSPasswordParams) (string, error) {
	if boxPriv == nil {
		return nil
	}
	pub, err := machinebox.Public(boxPriv)
	if err != nil {
		return nil
	}
	r := bioswrite.Runner{
		BoxKeyID: machinebox.KeyID(pub),
		Open:     func(purpose string, sealed []byte) ([]byte, error) { return machinebox.Open(boxPriv, purpose, sealed) },
		Detect:   bioswrite.Detect,
		Logger:   logger,
		// A cópia de emergência no Registro (docs/bios-senha.md, seção 9).
		MachineID: machineID,
		SaveCopy:  bioswrite.SaveRegistryCopy,
		// O marcador que o .bat deixou no Registro (docs/bios-senha.md, seção 11).
		Legacy:      bioslegacy.Read,
		ClearLegacy: bioslegacy.Clear,
	}
	return r.Run
}

// biosLegacy: o Registro tem o marcador do .bat com uma senha que serve de senha atual.
func biosLegacy() bool { return bioslegacy.Present() }

// biosSettingsRunner: o executor do job bios_settings (docs/bios-config.md), com a caixa desta
// máquina e o BitLocker do Windows. Sem a caixa, nil: o agente recusa esses jobs.
func biosSettingsRunner(logger *slog.Logger, boxPriv []byte) func(context.Context, int64, jobs.BIOSSettingsParams) (string, error) {
	if boxPriv == nil {
		return nil
	}
	pub, err := machinebox.Public(boxPriv)
	if err != nil {
		return nil
	}
	r := bioswrite.SettingsRunner{
		BoxKeyID:  machinebox.KeyID(pub),
		Open:      func(purpose string, sealed []byte) ([]byte, error) { return machinebox.Open(boxPriv, purpose, sealed) },
		Detect:    bioswrite.DetectSettings,
		BitLocker: bioswrite.WindowsBitLocker{},
		Logger:    logger,
	}
	return r.Run
}
