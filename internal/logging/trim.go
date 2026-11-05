package logging

import (
	"errors"
	"io"
	"io/fs"
	"os"
)

// Trim limita um log que outro processo escreve com o arquivo já aberto (o stderr do
// agente aberto pelo launchd, a saída do instalador da versão nova): passando de maxBytes,
// o conteúdo vira "nome.1" (o anterior sai) e o arquivo é zerado. Renomear não serviria:
// quem tem o arquivo aberto continuaria escrevendo no renomeado. Com O_APPEND, a próxima
// escrita vai para o começo do arquivo zerado. Devolve se limitou.
func Trim(path string, maxBytes int64) (bool, error) {
	st, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil || !st.Mode().IsRegular() || st.Size() <= maxBytes {
		return false, err
	}
	src, err := os.Open(path) // #nosec G304 -- caminho do próprio agente, na pasta de dados
	if err != nil {
		return false, err
	}
	defer src.Close()
	dst, err := os.OpenFile(path+".1", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) // #nosec G304 -- idem
	if err != nil {
		return false, err
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return false, err
	}
	if err := dst.Close(); err != nil {
		return false, err
	}
	return true, os.Truncate(path, 0)
}
