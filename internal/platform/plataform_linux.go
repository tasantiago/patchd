package platform

// Name é o rótulo legível da plataforma, usado em logs e no inventário.
const Name = "Linux"

// DataDir devolve a pasta de dados do agente. /var/lib é o local de estado
// persistente de serviços segundo o FHS; a permissão 0700 root entra na Aula 5.1.
func DataDir() string {
	return "/var/lib/patchd"
}
