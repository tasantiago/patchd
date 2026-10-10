package jobs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"

	"github.com/tasantiago/patchd/internal/protocol"
)

// TypeUpdate (Aula 8.4) atualiza pacotes escolhidos no painel, no Linux. Os parâmetros são
// uma lista fechada: nome do pacote binário e a versão mínima que resolve a pendência. O
// agente só instala o que a própria busca dele aponta como pendente, com candidata pelo
// menos na versão pedida, e pelo gerenciador da distribuição (apt ou dnf, que conferem a
// assinatura dos repositórios). Nenhum comando, opção ou repositório vem do servidor.
const TypeUpdate = "update"

// MaxPackages limita o tamanho de um job de atualização.
const MaxPackages = 100

// Package é um pacote binário a atualizar: ele tem de ficar pelo menos em MinVersion.
type Package struct {
	Name       string `json:"name"`
	MinVersion string `json:"min_version"`
}

// UpdateParams são os parâmetros do job update.
type UpdateParams struct {
	Packages []Package `json:"packages"`
}

// Nomes e versões aceitos. Nada que comece com "-" (viraria opção do apt ou do dnf) e
// nada de espaço, barra, curinga ou "=" (o agente monta "nome" sozinho).
var (
	packageName    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9+._:-]{0,127}$`) // ":" para a arquitetura estrangeira do apt (libc6:i386)
	packageVersion = regexp.MustCompile(`^[0-9][0-9A-Za-z.+~:_-]{0,127}$`)
)

// ErrNotPending: o agente recusou porque o pacote pedido não está pendente na busca dele
// (ou a candidata é mais velha que a versão pedida). Vira "rejeitado", não "falhou".
var ErrNotPending = errors.New("o pedido não confere com a busca desta máquina")

// ParseUpdateParams lê e confere os parâmetros. Campo desconhecido, pacote repetido, nome
// ou versão fora do formato: erro.
func ParseUpdateParams(raw json.RawMessage) (UpdateParams, error) {
	var p UpdateParams
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return p, fmt.Errorf("parâmetros do update ilegíveis: %w", err)
	}
	return p, p.Validate()
}

// Validate confere a lista.
func (p UpdateParams) Validate() error {
	if len(p.Packages) == 0 || len(p.Packages) > MaxPackages {
		return fmt.Errorf("o update leva de 1 a %d pacotes (veio %d)", MaxPackages, len(p.Packages))
	}
	seen := map[string]bool{}
	for _, pk := range p.Packages {
		switch {
		case !packageName.MatchString(pk.Name):
			return fmt.Errorf("nome de pacote inválido %q", pk.Name)
		case !packageVersion.MatchString(pk.MinVersion):
			return fmt.Errorf("versão inválida %q para %s", pk.MinVersion, pk.Name)
		case seen[pk.Name]:
			return fmt.Errorf("pacote repetido %q", pk.Name)
		}
		seen[pk.Name] = true
	}
	return nil
}

// Names devolve os nomes, na ordem.
func (p UpdateParams) Names() []string {
	out := make([]string, len(p.Packages))
	for i, pk := range p.Packages {
		out[i] = pk.Name
	}
	return out
}

// ParamsFromPending monta o update a partir das pendências avaliadas pelo servidor: para
// cada fonte escolhida que ainda está pendente, os binários instalados dela, com a versão
// que corrige a fonte como mínima. Fontes que não estão pendentes não entram (lista vazia
// = nada a fazer nesta máquina). Usado pelo painel (Aula 8.4) e pelas campanhas (8.5).
func ParamsFromPending(items []protocol.PendingItem, sources []string) UpdateParams {
	var p UpdateParams
	seen := map[string]bool{}
	for _, it := range items {
		if !slices.Contains(sources, it.ID) || it.FixedIn == "" {
			continue
		}
		for _, bin := range it.Packages {
			if !seen[bin] {
				seen[bin] = true
				p.Packages = append(p.Packages, Package{Name: bin, MinVersion: it.FixedIn})
			}
		}
	}
	return p
}
