package platform

import (
	"os"
	"path/filepath"
)

// Name é o rótulo legível da plataforma, usado em logs e no inventário.
const Name = "Windows"

// IsUnix indica se a plataforma segue as convenções Unix (caminhos, permissões, sinais).
const IsUnix = false

// DataDir devolve a pasta de dados do agente: %ProgramData%\patchd.
// A ACL restrita a SYSTEM e Administradores é aplicada na Aula 5.1.
func DataDir() string {
	base := os.Getenv("ProgramData")
	if base == "" {
		// Valor padrão do Windows, caso a variável não exista no ambiente do serviço.
		base = `C:\ProgramData`
	}
	return filepath.Join(base, "patchd")
}
