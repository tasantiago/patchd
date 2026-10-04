package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// RotatingFile é um arquivo de log com limite de tamanho. Quando o próximo registro
// passaria do limite, o arquivo vira "nome.1" (o "nome.1" anterior vira "nome.2", e assim
// por diante, até keep) e um arquivo novo começa. Serve para o agente rodando como
// serviço, que não tem terminal; no Linux, o journald já faz esse papel.
type RotatingFile struct {
	mu   sync.Mutex
	path string
	max  int64
	keep int
	f    *os.File
	size int64
}

// OpenRotating abre (ou cria) o arquivo, criando a pasta se preciso.
func OpenRotating(path string, maxBytes int64, keep int) (*RotatingFile, error) {
	if maxBytes <= 0 || keep < 1 {
		return nil, fmt.Errorf("log rotativo: tamanho e quantidade precisam ser positivos")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	r := &RotatingFile{path: path, max: maxBytes, keep: keep}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *RotatingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	r.f, r.size = f, st.Size()
	return nil
}

// Write grava um registro inteiro; um registro nunca é dividido entre dois arquivos.
func (r *RotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.size > 0 && r.size+int64(len(p)) > r.max {
		if err := r.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

// rotate desloca os arquivos antigos e abre um novo. Chamar com o lock.
func (r *RotatingFile) rotate() error {
	if err := r.f.Close(); err != nil {
		return err
	}
	_ = os.Remove(fmt.Sprintf("%s.%d", r.path, r.keep))
	for i := r.keep - 1; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", r.path, i), fmt.Sprintf("%s.%d", r.path, i+1))
	}
	if err := os.Rename(r.path, r.path+".1"); err != nil {
		return err
	}
	return r.open()
}

// Close fecha o arquivo atual.
func (r *RotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.f.Close()
}
