package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/tasantiago/patchd/internal/ad"
	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/panel"
)

// adConfig lê o login pelo AD do ambiente (Aula 7.3). Sem PATCHD_LDAP_HOST, desligado.
// Os valores (servidor, domínio, grupos) ficam só no .env: as mensagens citam as variáveis.
func adConfig(look config.Lookup) (ad.Config, bool, error) {
	host := config.String(look, "PATCHD_LDAP_HOST", "")
	if host == "" {
		return ad.Config{}, false, nil
	}
	cfg := ad.Config{
		Host:       host,
		Domain:     config.String(look, "PATCHD_LDAP_DOMAIN", ""),
		BaseDN:     config.String(look, "PATCHD_LDAP_BASE_DN", ""),
		AdminGroup: config.String(look, "PATCHD_LDAP_ADMIN_GROUP", ""),
		ReadGroup:  config.String(look, "PATCHD_LDAP_READ_GROUP", ""),
	}
	if err := cfg.Validate(); err != nil {
		return cfg, true, fmt.Errorf("login pelo AD (PATCHD_LDAP_HOST definido):\n%w", err)
	}
	return cfg, true, nil
}

const adUsage = `uso: patchd-server ad check -user NOME

Faz um login de teste no AD com a configuração do servidor (PATCHD_LDAP_*) e mostra o
resultado: conexão LDAPS, senha, grupos e o perfil que o painel daria. Não abre sessão.
A senha vem da primeira linha da entrada padrão, como em "patchd-server user":

  read -rsp 'Senha: ' S; echo; printf '%s\n' "$S" | patchd-server ad check -user fulano; unset S
`

// runAD trata "patchd-server ad check".
func runAD(args []string, look config.Lookup, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "check" {
		_, _ = io.WriteString(stderr, adUsage)
		return exitConfig
	}
	fs := flag.NewFlagSet("ad check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	user := fs.String("user", "", "usuário da rede, sem o domínio")
	if err := fs.Parse(args[1:]); err != nil {
		return exitConfig
	}
	cfg, on, err := adConfig(look)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitConfig
	}
	if !on {
		fmt.Fprintln(stderr, "patchd-server: o login pelo AD está desligado (defina PATCHD_LDAP_HOST e as demais PATCHD_LDAP_*)")
		return exitConfig
	}
	dir, err := ad.New(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitConfig
	}
	name := panel.NormalizeUsername(*user)
	if !panel.ValidUsername(name) {
		fmt.Fprintln(stderr, "patchd-server: -user: informe o usuário da rede, sem o domínio")
		return exitConfig
	}
	pw, err := readSecret(stdin)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitConfig
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return adCheck(ctx, dir, cfg, name, pw, stdout)
}

// adCheck faz o login de teste e escreve o relatório. Nunca escreve o servidor, o
// domínio nem os DNs: a saída pode ir para um chamado ou para o chat.
func adCheck(ctx context.Context, dir *ad.Directory, cfg ad.Config, user, pw string, stdout io.Writer) int {
	rep, err := dir.Check(ctx, user, pw)
	var ue *ad.UnavailableError
	switch {
	case errors.As(err, &ue):
		fmt.Fprintf(stdout, "conexão LDAPS: falhou: %s\n", ue.Reason)
		if rep.TLSVersion != "" {
			fmt.Fprintf(stdout, "(a conexão abriu com %s; a falha foi depois dela)\n", rep.TLSVersion)
		}
		fmt.Fprintln(stdout, "resultado: o AD não respondeu; no painel, valeria a conta local de contingência")
		return exitRuntime
	case errors.Is(err, panel.ErrBadCredentials):
		fmt.Fprintf(stdout, "conexão LDAPS: ok (%s)\n", rep.TLSVersion)
		fmt.Fprintf(stdout, "senha: recusada pelo AD (%v)\n", err)
		return exitRuntime
	case err != nil:
		fmt.Fprintf(stdout, "erro: %v\n", err)
		return exitRuntime
	}
	grupo := func(configured, member bool) string {
		switch {
		case !configured:
			return "não configurado"
		case member:
			return "membro"
		}
		return "não membro"
	}
	fmt.Fprintf(stdout, "conexão LDAPS: ok (%s)\n", rep.TLSVersion)
	fmt.Fprintln(stdout, "senha: conferida pelo AD")
	fmt.Fprintf(stdout, "grupo admin (PATCHD_LDAP_ADMIN_GROUP): %s\n", grupo(cfg.AdminGroup != "", rep.Admin))
	fmt.Fprintf(stdout, "grupo leitura (PATCHD_LDAP_READ_GROUP): %s\n", grupo(cfg.ReadGroup != "", rep.Read))
	if role := rep.Role(); role != "" {
		fmt.Fprintf(stdout, "perfil no painel: %s\n", role)
		return exitOK
	}
	fmt.Fprintln(stdout, "perfil no painel: nenhum (o login seria recusado)")
	return exitRuntime
}
