package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/tasantiago/patchd/internal/ad"
	"github.com/tasantiago/patchd/internal/api"
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
	// Só se há nome, não o nome: a saída pode ir para um chamado ou para o chat.
	if rep.Name != "" {
		fmt.Fprintln(stdout, "nome de exibição (displayName): preenchido")
	} else {
		fmt.Fprintln(stdout, "nome de exibição (displayName): vazio (o painel mostra o usuário)")
	}
	admins, reads := cfg.GroupLists()
	fmt.Fprintf(stdout, "grupos admin (%d): %s\n", len(admins), grupo(len(admins) > 0, rep.Admin))
	fmt.Fprintf(stdout, "grupos leitura (%d): %s\n", len(reads), grupo(len(reads) > 0, rep.Read))
	// Aula 19.4: só a quantidade; os nomes ficam no painel (a saída pode ir para um chamado).
	if len(rep.Missing) > 0 {
		fmt.Fprintf(stdout, "grupos da tela que o AD não achou: %d (confira em Configuração › Parâmetros › Acesso)\n", len(rep.Missing))
	}
	if role := rep.Role(); role != "" {
		fmt.Fprintf(stdout, "perfil no painel: %s\n", role)
		return exitOK
	}
	fmt.Fprintln(stdout, "perfil no painel: nenhum (o login seria recusado)")
	return exitRuntime
}

// adInfo: o cartão do AD em Configuração › Acesso (Aula 15.3): quais variáveis estão
// definidas (nunca os valores) e o mesmo login de teste do ad check.
func adInfo(cfg ad.Config, dir *ad.Directory) api.ADInfo {
	baseNote := ""
	if cfg.BaseDN == "" {
		baseNote = "a base é derivada do domínio"
	}
	return api.ADInfo{
		Vars: []api.ADVar{
			{Name: "PATCHD_LDAP_HOST", Set: cfg.Host != ""},
			{Name: "PATCHD_LDAP_DOMAIN", Set: cfg.Domain != ""},
			{Name: "PATCHD_LDAP_BASE_DN", Set: cfg.BaseDN != "", Note: baseNote},
			{Name: "PATCHD_LDAP_ADMIN_GROUP", Set: cfg.AdminGroup != "", Note: "o grupo garantido; outros grupos admin em Parâmetros › Acesso"},
			{Name: "PATCHD_LDAP_READ_GROUP", Set: cfg.ReadGroup != "", Note: "o padrão; a tela vence (Parâmetros › Acesso)"},
		},
		Check: func(ctx context.Context, user, pw string) (api.ADCheckResult, error) {
			return adCheckResult(cfg, func() (ad.Report, error) { return dir.Check(ctx, user, pw) })
		},
	}
}

// adCheckResult traduz o relatório do ad.Check para a tela.
func adCheckResult(cfg ad.Config, check func() (ad.Report, error)) (api.ADCheckResult, error) {
	rep, err := check()
	admins, reads := cfg.GroupLists()
	res := api.ADCheckResult{TLSVersion: rep.TLSVersion, Name: rep.Name, AdminSet: len(admins) > 0, ReadSet: len(reads) > 0}
	var ue *ad.UnavailableError
	switch {
	case errors.As(err, &ue):
		res.Unavailable = ue.Reason
		return res, nil
	case errors.Is(err, panel.ErrBadCredentials):
		res.BadPassword = true
		return res, nil
	case err != nil:
		return res, err
	}
	res.Admin, res.Read, res.Role = rep.Admin, rep.Read, rep.Role()
	res.AdminVia, res.ReadVia, res.Missing = rep.AdminVia, rep.ReadVia, rep.Missing
	return res, nil
}
