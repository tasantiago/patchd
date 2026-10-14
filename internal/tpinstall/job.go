package tpinstall

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/tasantiago/patchd/internal/jobs"
)

// Labels: o nome de cada estado na tela (linha de comando e painel).
var Labels = map[string]string{
	StateUpdate:     "ATUALIZAR",
	StateNoPackage:  "SEM PACOTE",
	StateDuplicate:  "DUPLICADO",
	StateBundle:     "BUNDLE",
	StateUserOnly:   "USUÁRIO",
	StateBadVersion: "VERSÃO ILEGÍVEL",
	StateCurrent:    "EM DIA",
}

// JobParams monta os parâmetros do job thirdparty a partir de uma decisão ATUALIZAR (Aula
// 14.6; era da linha de comando na 14.4): o pacote guardado e a regra. Os argumentos do
// manifesto viram tokens conferidos; o que não passa não vira job. A linha de comando e o
// painel usam esta mesma função, para o mesmo pedido dar o mesmo job.
func JobParams(d Decision) (jobs.ThirdPartyParams, error) {
	pk := d.Package
	if d.State != StateUpdate || pk == nil {
		return jobs.ThirdPartyParams{}, errors.New("a decisão não é ATUALIZAR")
	}
	args, err := jobs.SplitArgs(pk.Type, pk.Arguments)
	if err != nil {
		return jobs.ThirdPartyParams{}, fmt.Errorf("os argumentos do instalador guardado não cabem no job: %w", err)
	}
	p := jobs.ThirdPartyParams{App: d.App.ID, Version: pk.Version, SHA256: pk.SHA256, Size: pk.Size, Installer: pk.Type, Args: args}
	if len(pk.ReturnCodes) > 0 {
		p.ReturnCodes = map[string]string{}
		for c, r := range pk.ReturnCodes {
			p.ReturnCodes[strconv.Itoa(c)] = r
		}
	}
	if d.App.Install != nil {
		p.Signer = d.App.Install.Signer
	}
	return p, p.Validate()
}
