package main

import (
	"context"
	"fmt"
	"io"
	"os/user"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/store"
)

// Auditoria da linha de comando (Aula 18.1). Até aqui, a trilha via o painel e o agente; o
// que o operador fazia no terminal (criar job, mexer em anel, aprovar programa, publicar
// release) só aparecia no histórico do shell dele. Agora todo subcomando que muda alguma
// coisa vira um evento "cli.COMANDO.SUBCOMANDO", com quem rodou, as opções (sem segredos) e
// o resultado. Consultas (list, show, status) não entram.

// cliAudited: os subcomandos que mudam alguma coisa, por comando.
var cliAudited = map[string][]string{
	"job":      {"keygen", "create", "cancel"},
	"campaign": {"create", "pause", "resume", "cancel"},
	"ring":     {"set", "window"},
	"machine":  {"retire", "restore"},
	"token":    {"create", "revoke"},
	"user":     {"create", "password", "delete"},
	"package":  {"fetch", "install", "approve", "revoke"},
	"release":  {"publish", "withdraw"},
}

// cliSource é a origem dos eventos da linha de comando.
const cliSource = "linha de comando"

// cliAction diz qual evento o comando gera ("" para consultas).
func cliAction(args []string) string {
	if len(args) == 0 {
		return ""
	}
	rest := args[1:]
	switch args[0] {
	case "retention": // só o -apply apaga; sem ele, é simulação
		if hasFlag(rest, "apply") {
			return "cli.retention.apply"
		}
		return ""
	case "repo-cache":
		if len(rest) > 0 && rest[0] == "prune" && !hasFlag(rest, "dry-run") {
			return "cli.repo-cache.prune"
		}
		return ""
	}
	if len(rest) > 0 && slices.Contains(cliAudited[args[0]], rest[0]) {
		return "cli." + args[0] + "." + rest[0]
	}
	return ""
}

// hasFlag: -nome, --nome ou -nome=true.
func hasFlag(args []string, name string) bool {
	for _, a := range args {
		a = strings.TrimPrefix(strings.TrimPrefix(a, "-"), "-")
		if a == name || a == name+"=true" {
			return true
		}
	}
	return false
}

// cliSecret: opções cujo valor nunca vai para a trilha (hoje nenhum comando recebe segredo por
// opção — senhas e tokens vêm da entrada ou de arquivo —, mas um descuido não pode virar
// registro permanente).
var cliSecret = []string{"password", "senha", "secret", "segredo", "token", "key", "chave"}

// cliDetail monta o detalhe: as opções depois do subcomando, com os valores de opções
// sensíveis trocados por ***, e o resultado.
func cliDetail(args []string, code int) string {
	var out []string
	if len(args) > 1 {
		start := 2
		if args[0] == "retention" {
			start = 1
		}
		redactNext := false
		for _, a := range args[min(start, len(args)):] {
			if redactNext {
				out, redactNext = append(out, "***"), false
				continue
			}
			name := strings.TrimLeft(a, "-")
			if strings.HasPrefix(a, "-") && slices.ContainsFunc(cliSecret, func(s string) bool { return strings.Contains(strings.ToLower(name), s) }) {
				if k, _, ok := strings.Cut(a, "="); ok {
					out = append(out, k+"=***")
				} else {
					out, redactNext = append(out, a), true
				}
				continue
			}
			out = append(out, a)
		}
	}
	result := "ok"
	if code != exitOK {
		result = fmt.Sprintf("falhou (código %d)", code)
	}
	if len(out) == 0 {
		return result
	}
	return strings.Join(out, " ") + " → " + result
}

// operator é quem rodou o comando: PATCHD_OPERATOR (o nome que a equipe usa no painel, se o
// usuário do sistema for compartilhado, como no container) ou o usuário do sistema.
func operator(look config.Lookup) string {
	clean := func(s string) string {
		s = strings.TrimSpace(s)
		if s == "" || len(s) > 64 || strings.IndexFunc(s, unicode.IsControl) >= 0 {
			return ""
		}
		return s
	}
	if v, ok := look("PATCHD_OPERATOR"); ok {
		if s := clean(v); s != "" {
			return s
		}
	}
	if u, err := user.Current(); err == nil {
		if s := clean(u.Username); s != "" {
			return s
		}
	}
	return "desconhecido"
}

type auditRecorder interface {
	RecordAudit(ctx context.Context, e store.AuditEvent) error
}

// cliAuditOpen abre a trilha (o PostgreSQL da configuração); os testes a substituem. nil, sem
// erro: sem banco (modo memória), não há trilha.
var cliAuditOpen = func(look config.Lookup) (auditRecorder, func(), error) {
	cfg, err := loadServerConfig(nil, look, io.Discard)
	if err != nil || cfg.DatabaseURL == "" {
		return nil, func() {}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := store.Connect(ctx, cfg.DatabaseURL, 5*time.Second)
	if err != nil {
		return nil, func() {}, err
	}
	return store.NewPostgres(pool), pool.Close, nil
}

// auditCLI registra o comando na trilha. Erro de uso (código 2) não entra: nada foi tentado.
// Falha ao registrar não muda o resultado do comando, mas aparece no terminal.
func auditCLI(action string, args []string, look config.Lookup, code int, stderr io.Writer) {
	if code == exitConfig {
		return
	}
	rec, done, err := cliAuditOpen(look)
	defer done()
	if err == nil && rec != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err = rec.RecordAudit(ctx, store.AuditEvent{At: time.Now().UTC(), Username: operator(look), Source: cliSource,
			Action: action, Detail: cliDetail(args, code)})
	}
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: aviso: o comando não foi registrado na auditoria: %v\n", err)
	}
}
