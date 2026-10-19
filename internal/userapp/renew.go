package userapp

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"time"
)

// Renovação da Central (Aula 18.8). O app roda da cópia patchd-app.exe que o serviço do
// agente mantém. Quando o agente se atualiza, ele renomeia a cópia em uso (patchd-app.exe.old-…)
// e põe a nova no lugar; o processo aberto continuava sendo o antigo até o próximo logon
// (lab da 17.5: a Loja não aparecia). Agora o app confere, de tempos em tempos, se o arquivo
// do caminho dele ainda é o mesmo de quando abriu: trocado, ele se reabre com o novo; sumido
// por duas conferências seguidas (agente removido, -user-app=false), ele só fecha.

// Renewal é o resultado de uma conferência.
type Renewal int

const (
	RenewNone     Renewal = iota // o mesmo arquivo: nada a fazer
	RenewReplaced                // outro arquivo no caminho: reabrir com ele
	RenewGone                    // o caminho sumiu em duas conferências seguidas: fechar
)

// Self vigia o arquivo do próprio app.
type Self struct {
	Path    string
	start   os.FileInfo
	missing int
}

// WatchSelf guarda a identidade do arquivo em path. Erro: o caminho não existe (o app não
// está rodando da cópia vigiada; quem chama não vigia).
func WatchSelf(path string) (*Self, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	// No Windows, a identidade (volume + índice do arquivo) que o os.SameFile compara é lida
	// do disco na primeira comparação, pelo caminho: comparar já, enquanto o caminho ainda é
	// deste arquivo; depois da troca, a leitura tardia pegaria o arquivo novo.
	os.SameFile(fi, fi)
	return &Self{Path: path, start: fi}, nil
}

// Check confere uma vez. A troca é feita por rename (atômica), mas entre tirar a cópia velha
// e pôr a nova o caminho fica vazio por um instante: um sumiço só conta na segunda vez.
func (s *Self) Check() Renewal {
	fi, err := os.Stat(s.Path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		s.missing++
		if s.missing >= 2 {
			return RenewGone
		}
		return RenewNone
	case err != nil:
		return RenewNone // sem conclusão: confere de novo na próxima
	}
	s.missing = 0
	if os.SameFile(s.start, fi) {
		return RenewNone
	}
	return RenewReplaced
}

// Wait confere a cada every até haver o que fazer ou o contexto acabar (RenewNone).
func (s *Self) Wait(ctx context.Context, every time.Duration) Renewal {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return RenewNone
		case <-t.C:
			if r := s.Check(); r != RenewNone {
				return r
			}
		}
	}
}
