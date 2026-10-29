package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/tasantiago/patchd/internal/machinebox"
)

func discoveryKeygen(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("discovery keygen", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", "", "arquivo novo para a chave de cifragem da credencial Windows")
	if err := fs.Parse(args); err != nil || *out == "" || fs.NArg() != 0 {
		return exitConfig
	}
	key, err := machinebox.Generate()
	if err != nil {
		return exitRuntime
	}
	f, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		fmt.Fprintln(stderr, "não foi possível criar o arquivo da chave; ele não pode existir")
		return exitRuntime
	}
	_, err = io.WriteString(f, machinebox.EncodePrivate(key)+"\n")
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		_ = os.Remove(*out)
		return exitRuntime
	}
	fmt.Fprintln(stdout, "Chave criada. Configure PATCHD_DISCOVERY_KEY_FILE e guarde uma cópia segura fora do banco.")
	return exitOK
}
