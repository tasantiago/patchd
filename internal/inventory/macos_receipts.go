package inventory

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/tasantiago/patchd/internal/platform"
)

// Recibos de instalação do macOS.
const (
	pkgutilPath    = "/usr/sbin/pkgutil"
	receiptsSource = "pkgutil (recibo de instalação)"
)

// receiptToSoftware converte um recibo (plist do pkgutil --pkg-info-plist) em Software.
// Recibos persistem depois de o conteúdo ser substituído ou apagado: são evidência de
// instalação, mais fraca que o caminho de um aplicativo.
func receiptToSoftware(plist []byte) (Software, error) {
	d, err := parsePlistDict(plist)
	if err != nil {
		return Software{}, err
	}
	id := strings.TrimSpace(d["pkgid"])
	if id == "" {
		return Software{}, errors.New("recibo sem pkgid")
	}
	return Software{
		Name:            id,
		Version:         strings.TrimSpace(d["pkg-version"]),
		InstallDate:     unixDate(d["install-time"]),
		Scope:           "machine",
		SystemComponent: strings.HasPrefix(id, "com.apple."),
		Source:          receiptsSource,
	}, nil
}

// collectDarwinReceipts lista os recibos do volume de inicialização e lê cada um.
// Recibo ilegível vira erro sem interromper os demais. Fica fora do arquivo _darwin.go
// para ser testável em qualquer SO.
func collectDarwinReceipts(ctx context.Context, run platform.Runner) ([]Software, error) {
	out, err := run.Run(ctx, pkgutilPath, "--pkgs")
	if err != nil {
		return nil, err
	}

	var list []Software
	var errs []error
	for _, id := range strings.Split(string(out), "\n") {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		// Respeita o prazo total da coleta.
		if err := ctx.Err(); err != nil {
			errs = append(errs, fmt.Errorf("recibos: coleta interrompida: %w", err))
			break
		}
		info, err := run.Run(ctx, pkgutilPath, "--pkg-info-plist", id)
		if err != nil {
			errs = append(errs, fmt.Errorf("recibo %s: %w", id, err))
			continue
		}
		s, err := receiptToSoftware(info)
		if err != nil {
			errs = append(errs, fmt.Errorf("recibo %s: %w", id, err))
			continue
		}
		list = append(list, s)
	}
	return list, errors.Join(errs...)
}
