package inventory

import (
	"context"
	"errors"

	"github.com/tasantiago/patchd/internal/protocol"
)

// ErrNotImplemented indica uma seção do inventário ainda não implementada neste SO.
var ErrNotImplemented = errors.New("ainda não implementado neste SO")

// Os tipos de dados do inventário moram em protocol, porque o servidor também os usa
// e não deve importar os coletores. Os aliases são o mesmo tipo com outro nome: o código
// deste pacote continua usando Software, OSInfo e Hardware sem mudança.
type (
	OSInfo   = protocol.OSInfo
	Software = protocol.Software
	Hardware = protocol.Hardware
)

// Collector coleta o inventário da máquina local. Cada SO tem sua implementação,
// escolhida na compilação; New devolve a do SO atual.
type Collector interface {
	// OS identifica o sistema operacional.
	OS(ctx context.Context) (OSInfo, error)
	// Software lista o software instalado. Em falha parcial, devolve o que
	// conseguiu coletar junto com o erro. Seção não implementada: ErrNotImplemented.
	Software(ctx context.Context) ([]Software, error)
	// Hardware descreve a máquina. Em falha parcial (ex.: campo que exige root),
	// devolve os campos lidos junto com o erro. Não implementada: ErrNotImplemented.
	Hardware(ctx context.Context) (Hardware, error)
}
