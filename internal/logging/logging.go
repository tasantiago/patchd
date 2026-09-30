// Package logging cria o logger estruturado (log/slog) usado pelo agente e pelo servidor.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// parse interpreta o nível ("debug", "info", "warn", "error", sem diferenciar maiúsculas)
// e o formato ("json" ou "text").
func parse(level, format string) (slog.Level, string, error) {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		return 0, "", fmt.Errorf("nível de log inválido %q (use debug, info, warn ou error)", level)
	}
	f := strings.ToLower(strings.TrimSpace(format))
	if f != "json" && f != "text" {
		return 0, "", fmt.Errorf("formato de log inválido %q (use json ou text)", format)
	}
	return lvl, f, nil
}

// Check valida nível e formato sem criar o logger. Usado na validação da configuração.
func Check(level, format string) error {
	_, _, err := parse(level, format)
	return err
}

// New cria um logger que escreve em w no formato e a partir do nível indicados.
func New(w io.Writer, level, format string) (*slog.Logger, error) {
	lvl, f, err := parse(level, format)
	if err != nil {
		return nil, err
	}
	opts := &slog.HandlerOptions{Level: lvl}
	if f == "json" {
		return slog.New(slog.NewJSONHandler(w, opts)), nil
	}
	return slog.New(slog.NewTextHandler(w, opts)), nil
}
