package platform

import "os"

// InContainer diz se o agente está dentro de um container, e por quê. Dentro de um
// container, os arquivos são os da imagem e o kernel é o do host (Aula 2.1): inventário,
// busca e reinício pendente descreveriam uma máquina que não existe.
func InContainer() (bool, string) {
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	return detectContainer(exists, os.ReadFile, os.Getenv)
}
