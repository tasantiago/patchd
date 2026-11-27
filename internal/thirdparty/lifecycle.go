package thirdparty

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Ciclos de suporte do endoflife.date (https://endoflife.date, licença MIT): para cada
// produto, as linhas de versão (3.12, 22, 8.0) com a data do fim do suporte e a versão mais
// nova de cada uma. O servidor baixa um produto por vez (/api/v1/products/<produto>) e cruza
// com o inventário localmente: nada do parque sai daqui.

// Cycle é um ciclo de suporte de um produto.
type Cycle struct {
	Product string
	Name    string     // 3.12, 22, 8.0
	EOLFrom *time.Time // fim do suporte (dia, UTC); nil: sem data conhecida
	IsEOL   bool       // o endoflife.date já marcava como sem suporte quando foi buscado
	Latest  string     // a versão mais nova do ciclo (vazia se não houver)
	Fetched time.Time
}

// EOLAt diz se o ciclo está sem suporte no instante.
func (c Cycle) EOLAt(now time.Time) bool {
	return c.IsEOL || c.EOLFrom != nil && !now.Before(*c.EOLFrom)
}

// EOLSoon: termina o suporte dentro do prazo (e ainda não terminou).
func (c Cycle) EOLSoon(now time.Time, within time.Duration) bool {
	return !c.EOLAt(now) && c.EOLFrom != nil && c.EOLFrom.Sub(now) <= within
}

// LifecycleWarning: o prazo do aviso de fim de suporte próximo.
const LifecycleWarning = 90 * 24 * time.Hour

// CycleFor devolve o ciclo da versão: o de nome mais longo que é começo dela, em pedaços
// inteiros (3.12 serve a 3.12.7, não a 3.1.2; 22 serve a 22.11.0). nil: nenhum.
func CycleFor(version string, cycles []Cycle) *Cycle {
	v, ok := Normalize(version)
	if !ok {
		return nil
	}
	var best *Cycle
	for i := range cycles {
		c := &cycles[i]
		name, ok := Normalize(c.Name)
		if !ok || name != c.Name {
			continue
		}
		if v != name && !strings.HasPrefix(v, name+".") {
			continue
		}
		if best == nil || len(name) > len(best.Name) {
			best = c
		}
	}
	return best
}

// lifecycleResponse: o pedaço da resposta de /api/v1/products/<produto> que interessa.
type lifecycleResponse struct {
	Result struct {
		Name     string `json:"name"`
		Releases []struct {
			Name    string  `json:"name"`
			IsEOL   bool    `json:"isEol"`
			EOLFrom *string `json:"eolFrom"`
			Latest  *struct {
				Name string `json:"name"`
			} `json:"latest"`
		} `json:"releases"`
	} `json:"result"`
}

// maxCycles: um produto com mais ciclos que isso é resposta errada (o Firefox tem ~160).
const maxCycles = 1000

// Lifecycle busca os ciclos de um produto no endoflife.date.
func (c *Client) Lifecycle(ctx context.Context, product string) ([]Cycle, error) {
	if !lifecycleSlug.MatchString(product) {
		return nil, fmt.Errorf("endoflife.date: produto %q inválido", product)
	}
	var r lifecycleResponse
	if err := c.getJSON(ctx, fmt.Sprintf("%s/api/v1/products/%s", c.EndoflifeURL, url.PathEscape(product)), &r); err != nil {
		return nil, err
	}
	if len(r.Result.Releases) == 0 || len(r.Result.Releases) > maxCycles {
		return nil, fmt.Errorf("endoflife.date: %s veio com %d ciclos", product, len(r.Result.Releases))
	}
	now := time.Now().UTC()
	out := make([]Cycle, 0, len(r.Result.Releases))
	for _, x := range r.Result.Releases {
		if x.Name == "" || len(x.Name) > 40 {
			continue
		}
		cy := Cycle{Product: product, Name: x.Name, IsEOL: x.IsEOL, Fetched: now}
		if x.EOLFrom != nil {
			d, err := time.Parse("2006-01-02", *x.EOLFrom)
			if err != nil {
				return nil, fmt.Errorf("endoflife.date: %s %s: data de fim %q ilegível", product, x.Name, *x.EOLFrom)
			}
			cy.EOLFrom = &d
		}
		if x.Latest != nil && len(x.Latest.Name) <= 64 {
			cy.Latest = x.Latest.Name
		}
		out = append(out, cy)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("endoflife.date: %s sem ciclos legíveis", product)
	}
	return out, nil
}

// evergreenRows lê a resposta do /app/<nome> do Evergreen: uma lista de linhas (uma por
// arquitetura, canal, idioma...) ou, em versões antigas da API, uma linha só.
func evergreenRows(raw json.RawMessage) ([]map[string]any, error) {
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err == nil {
		return rows, nil
	}
	var one map[string]any
	if err := json.Unmarshal(raw, &one); err != nil {
		return nil, fmt.Errorf("resposta do Evergreen ilegível: %w", err)
	}
	return []map[string]any{one}, nil
}

// evergreenVersions: as versões das linhas que passam no filtro.
func evergreenVersions(rows []map[string]any, filter map[string]string) []string {
	var out []string
	for _, row := range rows {
		if v, isString := row["Version"].(string); isString && rowMatches(row, filter) {
			out = append(out, v)
		}
	}
	return out
}

// rowMatches: a linha tem cada campo do filtro com o valor pedido (sem diferença de
// maiúsculas, nem nos nomes dos campos).
func rowMatches(row map[string]any, filter map[string]string) bool {
	for k, want := range filter {
		if got, found := field(row, k); !found || !strings.EqualFold(got, want) {
			return false
		}
	}
	return true
}

// field: o valor de um campo da linha, como texto (o nome sem diferença de maiúsculas).
func field(row map[string]any, name string) (string, bool) {
	for k, v := range row {
		if strings.EqualFold(k, name) && v != nil {
			return fmt.Sprint(v), true
		}
	}
	return "", false
}
