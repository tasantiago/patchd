package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/machinebox"
)

const biosUsage = `uso: patchd-server bios <comando> [opções]

comandos:
  keygen  cria a chave da guarda da senha da BIOS: -out ARQUIVO grava a privada (0600).
          Ela vai para o servidor como PATCHD_BIOS_ESCROW_KEY_FILE (segredo do compose)
  key     mostra a impressão da chave de guarda configurada (a mesma do painel e dos logs)
  export  -out ARQUIVO: grava as senhas que podem estar valendo, ainda cifradas para a chave de
          guarda (a cópia de emergência; pode ir para qualquer backup, agendada)
  reveal  -key CHAVE (arquivo, ou - para colar) com -copia TEXTO (lida no Registro da máquina,
          HKLM\SOFTWARE\patchd\BIOS) ou com -file EXPORT -machine ID_OU_NOME: mostra a senha
          sem servidor e sem banco (fora da trilha: registre no chamado e troque depois)

A guarda (Aula 21.4, docs/bios-senha.md): o servidor sorteia a senha de administrador da
BIOS de cada máquina e guarda só o envelope cifrado para esta chave. Sem a privada, nenhuma
senha guardada pode ser revelada: guarde duas cópias fora do servidor, longe da cópia do banco.
A cópia de emergência: docs/bios-senha.md, seção 9.
`

func runBios(args []string, look config.Lookup, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = io.WriteString(stderr, biosUsage)
		return exitConfig
	}
	switch args[0] {
	case "keygen":
		return biosKeygen(args[1:], stdout, stderr)
	case "export":
		return runBiosExport(args[1:], look, stdout, stderr)
	case "reveal":
		return runBiosReveal(args[1:], stdout, stderr)
	case "key":
		priv, err := biosEscrowKey(look)
		if err == nil && priv == nil {
			err = fmt.Errorf("sem PATCHD_BIOS_ESCROW_KEY_FILE (crie com patchd-server bios keygen)")
		}
		if err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitConfig
		}
		pub, _ := machinebox.Public(priv)
		fmt.Fprintln(stdout, machinebox.KeyID(pub))
		return exitOK
	}
	_, _ = io.WriteString(stderr, biosUsage)
	return exitConfig
}

// biosEscrowKey lê a chave privada da guarda. Sem a variável: nil, sem erro (a guarda fica
// desligada). Variável definida e chave ilegível: erro (o servidor não sobe).
func biosEscrowKey(look config.Lookup) ([]byte, error) {
	s, err := config.Secret(look, "PATCHD_BIOS_ESCROW_KEY")
	if err != nil || s == "" {
		return nil, err
	}
	priv, err := machinebox.ParsePrivate(s)
	if err != nil {
		return nil, fmt.Errorf("PATCHD_BIOS_ESCROW_KEY: %w", err)
	}
	return priv, nil
}

func biosKeygen(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("bios keygen", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", "", "arquivo da chave privada (não pode existir)")
	if err := fs.Parse(args); err != nil || *out == "" || fs.NArg() > 0 {
		fmt.Fprintln(stderr, "patchd-server: informe -out ARQUIVO para a chave privada da guarda")
		return exitConfig
	}
	priv, err := machinebox.Generate()
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	pub, _ := machinebox.Public(priv)
	// O_EXCL: nunca sobrescreve uma chave existente (as senhas guardadas com ela se perderiam).
	// A exceção é o arquivo vazio que o deploy/gerar-segredos.sh deixa para o compose montar.
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if fi, err := os.Lstat(*out); err == nil && fi.Mode().IsRegular() && fi.Size() == 0 {
		flags = os.O_WRONLY | os.O_TRUNC
	}
	f, err := os.OpenFile(*out, flags, 0o600)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	if _, err := io.WriteString(f, machinebox.EncodePrivate(priv)+"\n"); err != nil {
		_ = f.Close()
		os.Remove(*out)
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	if err := f.Close(); err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	fmt.Fprintf(stderr, "chave da guarda da BIOS gravada em %s (0600). Ela vai para o servidor (PATCHD_BIOS_ESCROW_KEY_FILE);\n"+
		"guarde duas cópias fora do servidor: sem ela, nenhuma senha guardada pode ser revelada.\n", *out)
	fmt.Fprintln(stdout, machinebox.KeyID(pub))
	return exitOK
}
