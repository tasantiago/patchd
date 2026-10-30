package bioswrite

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/tasantiago/patchd/internal/wincom"
)

// O BitLocker da unidade do sistema, pela Win32_EncryptableVolume (a mesma classe que a coleta
// de Wake on LAN usa para o PIN). Sem o recurso (Windows Home, namespace ausente) ou sem a
// unidade: desprotegido, sem erro.
const bitlockerNS = `root\cimv2\Security\MicrosoftVolumeEncryption`

var systemDrive = regexp.MustCompile(`^[A-Za-z]:$`)

// WindowsBitLocker é o BitLocker do Windows.
type WindowsBitLocker struct{}

func (WindowsBitLocker) volume() (string, error) {
	d := os.Getenv("SystemDrive")
	if !systemDrive.MatchString(d) {
		return "", fmt.Errorf("unidade do sistema desconhecida %q", d)
	}
	return "SELECT * FROM Win32_EncryptableVolume WHERE DriveLetter = '" + d + "'", nil
}

func (b WindowsBitLocker) call(ctx context.Context, method string, in []wincom.Param, out []string) (wincom.Row, error) {
	wql, err := b.volume()
	if err != nil {
		return nil, err
	}
	var row wincom.Row
	err = wincom.Do(ctx, func() error {
		r, _, err := wincom.ExecMethod(bitlockerNS, wql, method, in, out)
		row = r
		return err
	})
	return row, err
}

// Protected: a propriedade ProtectionStatus, 1 = proteção ligada.
func (b WindowsBitLocker) Protected(ctx context.Context) (bool, error) {
	wql, err := b.volume()
	if err != nil {
		return false, err
	}
	var rows []wincom.Row
	err = wincom.Do(ctx, func() error {
		rows, err = wincom.Query(bitlockerNS, strings.Replace(wql, "SELECT *", "SELECT ProtectionStatus", 1), []string{"ProtectionStatus"})
		return err
	})
	switch {
	case wincom.Missing(err):
		return false, nil
	case err != nil:
		return false, err
	case len(rows) == 0:
		return false, nil
	}
	st, err := asCode(rows[0]["ProtectionStatus"])
	return st == 1, err
}

// SuspendOneReboot: DisableKeyProtectors(DisableCount = 1), a mesma coisa que
// Suspend-BitLocker -RebootCount 1. A proteção volta sozinha depois do reinício.
func (b WindowsBitLocker) SuspendOneReboot(ctx context.Context) error {
	row, err := b.call(ctx, "DisableKeyProtectors", []wincom.Param{{Name: "DisableCount", Value: uint32(1)}}, []string{"ReturnValue"})
	if err != nil {
		return err
	}
	if rv, _ := asCode(row["ReturnValue"]); rv != 0 {
		return fmt.Errorf("DisableKeyProtectors devolveu 0x%08X", rv)
	}
	return nil
}

// Resume: EnableKeyProtectors.
func (b WindowsBitLocker) Resume(ctx context.Context) error {
	row, err := b.call(ctx, "EnableKeyProtectors", nil, []string{"ReturnValue"})
	if err != nil {
		return err
	}
	if rv, _ := asCode(row["ReturnValue"]); rv != 0 {
		return fmt.Errorf("EnableKeyProtectors devolveu 0x%08X", rv)
	}
	return nil
}
