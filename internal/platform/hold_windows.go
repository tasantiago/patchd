package platform

import "golang.org/x/sys/windows"

// HoldFile abre o arquivo deixando os outros só lerem (Aula 20.6): enquanto a função
// devolvida não for chamada, ninguém escreve, renomeia nem apaga o arquivo, nem o SYSTEM.
// Ler continua permitido: a conferência da assinatura, o msiexec e o próprio Windows, ao
// executar, só leem. É o que fecha a troca do arquivo entre a conferência e a execução.
func HoldFile(path string) (func(), error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	return func() { _ = windows.CloseHandle(h) }, nil
}
