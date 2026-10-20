package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/tasantiago/patchd/internal/protocol"
)

// O nome da Central de software (Aula 20.10) vem do painel (parâmetro PATCHD_APP_NAME), na loja
// que a Central pede ao agente. O agente guarda o nome na pasta de dados e, no Windows, grava
// na descrição da cópia da Central (patchd-app.exe), que é de onde o Windows tira o título dos
// avisos. A Central, ao ver a cópia trocada, reabre sozinha (Aula 18.8) já com o nome novo.

const appNameFile = "central-nome.txt"

// loadAppName: o nome guardado; sem arquivo (ou com um nome inválido), o padrão.
func loadAppName(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, appNameFile))
	n := strings.TrimSpace(string(b))
	if err != nil || protocol.CheckAppName(n) != nil {
		return protocol.DefaultAppName
	}
	return n
}

// appNamer recebe o nome a cada consulta da Central e só age quando ele muda.
type appNamer struct {
	dir    string
	logger *slog.Logger
	// apply refaz a cópia da Central com o nome (Windows, serviço instalado); nil: só guarda.
	apply func(name string) (bool, error)

	mu      sync.Mutex
	current string
}

// Set guarda o nome novo e refaz a cópia. Nome inválido ou igual: nada.
func (n *appNamer) Set(name string) {
	if protocol.CheckAppName(name) != nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.current == "" {
		n.current = loadAppName(n.dir)
	}
	if name == n.current {
		return
	}
	// #nosec G306 -- texto curto, sem segredo, na pasta de dados (só SYSTEM e administradores)
	if err := os.WriteFile(filepath.Join(n.dir, appNameFile), []byte(name+"\n"), 0o644); err != nil {
		n.logger.Warn("nome da Central não guardado (vale até o serviço reiniciar)", "error", err)
	}
	n.logger.Info("nome da Central trocado pelo painel", "antes", n.current, "nome", name)
	n.current = name
	if n.apply == nil {
		return
	}
	changed, err := n.apply(name)
	switch {
	case err != nil:
		n.logger.Error("cópia da Central não refeita com o nome novo", "error", err)
	case changed:
		n.logger.Info("cópia da Central refeita com o nome novo: ela reabre sozinha em até 1 minuto")
	}
}
