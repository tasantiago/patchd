package inventory

// DetectPackageManager lê o os-release e devolve o banco de pacotes da distribuição
// ("dpkg" ou "rpm") e o ID da distribuição. Distribuição sem suporte: manager vazio.
// É a mesma decisão do inventário de software (Aula 2.3), reaproveitada pela busca.
func DetectPackageManager(readFile func(string) ([]byte, error)) (manager, id string, err error) {
	c := &linuxCollector{readFile: readFile}
	fields, _, err := c.readOSRelease()
	if err != nil {
		return "", "", err
	}
	return packageManagerFor(fields["ID"], fields["ID_LIKE"]), fields["ID"], nil
}
