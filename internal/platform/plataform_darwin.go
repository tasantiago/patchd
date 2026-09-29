package platform

// Name é o rótulo legível da plataforma, usado em logs e no inventário.
const Name = "macOS"

// DataDir devolve a pasta de dados do agente no local usado por daemons do sistema.
func DataDir() string {
	return "/Library/Application Support/patchd"
}
