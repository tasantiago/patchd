#!/usr/bin/env bash
# Aula 6.1: parser, cliente e sincronização do catálogo do MSRC. Rode na raiz do repositório.
# Pode rodar de novo: os arquivos são regravados, e a alteração no main.go só é feita uma vez.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
for m in internal/store/migrations/0004_*.sql; do
  if [ -e "$m" ] && [ "$m" != internal/store/migrations/0004_catalogo_msrc.sql ]; then echo "ERRO: já existe outra migration 0004 ($m)" >&2; exit 1; fi
done

mkdir -p internal/catalog/msrc
cat > internal/catalog/msrc/msrc.go <<'PATCHD_EOF'
// Package msrc lê os documentos CVRF da API de atualizações de segurança da Microsoft
// (MSRC, https://api.msrc.microsoft.com/cvrf/v3.0) e os reduz ao que o patchd usa: os
// produtos, as CVEs (com severidade, impacto, exploração e CVSS) e as correções do
// fabricante (KB e build corrigido) por produto.
//
// Os números de tipo foram conferidos no documento real de 2026-Sep (Aula 6.1):
//
//	Threats:      0 = impacto, 1 = situação de exploração (uma por CVE), 3 = severidade
//	Remediations: 2 = correção do fabricante (KB, FixedBuild, Supercedence); 3 e 6 são
//	              links para notas da versão e artigos de suporte, sem dado novo
package msrc

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

// Tipos do CVRF que o patchd usa.
const (
	threatImpact        = 0
	threatExploitStatus = 1
	threatSeverity      = 3
	remediationVendor   = 2
)

// kbPattern: a descrição de uma correção do fabricante é o número do KB. Há entradas
// do tipo 2 com "Release Notes" no lugar do número: não são KBs e ficam de fora.
var kbPattern = regexp.MustCompile(`^[0-9]{6,8}$`)

// Update é um item da lista /updates: um documento, com a data da última revisão.
// Os documentos são revisados depois de publicados; a API não aceita requisição
// condicional (no-store, sem Last-Modified), então é por CurrentReleaseDate que a
// sincronização sabe o que baixar de novo.
type Update struct {
	ID                 string    `json:"ID"` // ex.: 2026-Sep
	DocumentTitle      string    `json:"DocumentTitle"`
	InitialReleaseDate time.Time `json:"InitialReleaseDate"`
	CurrentReleaseDate time.Time `json:"CurrentReleaseDate"`
	CvrfURL            string    `json:"CvrfUrl"`
}

// ParseUpdates lê a resposta de /updates.
func ParseUpdates(r io.Reader) ([]Update, error) {
	var resp struct {
		Value []Update `json:"value"`
	}
	if err := json.NewDecoder(r).Decode(&resp); err != nil {
		return nil, fmt.Errorf("lista de documentos do MSRC ilegível: %w", err)
	}
	return resp.Value, nil
}

// Document é um documento mensal já reduzido.
type Document struct {
	ID         string
	Title      string
	Initial    time.Time
	Current    time.Time
	Products   []Product
	Vulns      []Vuln
	Affected   []Affected
	Fixes      []Fix
	SkippedFix int // correções do tipo 2 sem número de KB (ex.: "Release Notes")
}

// Product é um produto do documento: o ID só vale dentro do catálogo do MSRC.
type Product struct {
	ID   string
	Name string
}

// Vuln é uma CVE.
type Vuln struct {
	CVE       string
	Title     string
	Exploited bool   // "Exploited:Yes"
	Disclosed bool   // "Publicly Disclosed:Yes"
	Exploit   string // o texto inteiro da situação de exploração, como veio
}

// Affected liga uma CVE a um produto, com a severidade, o impacto e o CVSS daquele produto.
type Affected struct {
	CVE       string
	ProductID string
	Severity  string  // Critical, Important, Moderate, Low
	Impact    string  // ex.: Elevation of Privilege
	BaseScore float64 // 0 quando não há CVSS para o produto
	Vector    string
}

// Fix é uma correção do fabricante para uma CVE num produto.
type Fix struct {
	CVE             string
	ProductID       string
	KB              string // só os dígitos
	SubType         string // Security Update, Monthly Rollup, Servicing Stack Update...
	FixedBuild      string // ex.: 10.0.26200.9445; vazio quando o produto não tem build
	Supersedes      string // KB substituído, como veio (pode ser vazio)
	RestartRequired string // Yes, No, Maybe, ou vazio
}

// Estrutura bruta do CVRF em JSON, só com os campos usados.
type rawDoc struct {
	DocumentTitle    value `json:"DocumentTitle"`
	DocumentTracking struct {
		Identification struct {
			ID value `json:"ID"`
		} `json:"Identification"`
		InitialReleaseDate cvrfTime `json:"InitialReleaseDate"`
		CurrentReleaseDate cvrfTime `json:"CurrentReleaseDate"`
	} `json:"DocumentTracking"`
	ProductTree struct {
		FullProductName []struct {
			ProductID string `json:"ProductID"`
			Value     string `json:"Value"`
		} `json:"FullProductName"`
	} `json:"ProductTree"`
	Vulnerability []rawVuln `json:"Vulnerability"`
}

type rawVuln struct {
	CVE             string `json:"CVE"`
	Title           value  `json:"Title"`
	ProductStatuses []struct {
		ProductID []string `json:"ProductID"`
	} `json:"ProductStatuses"`
	Threats []struct {
		Type        int      `json:"Type"`
		Description value    `json:"Description"`
		ProductID   []string `json:"ProductID"`
	} `json:"Threats"`
	CVSSScoreSets []struct {
		BaseScore float64  `json:"BaseScore"`
		Vector    string   `json:"Vector"`
		ProductID []string `json:"ProductID"`
	} `json:"CVSSScoreSets"`
	Remediations []struct {
		Type            int      `json:"Type"`
		SubType         string   `json:"SubType"`
		Description     value    `json:"Description"`
		FixedBuild      string   `json:"FixedBuild"`
		Supercedence    string   `json:"Supercedence"`
		RestartRequired value    `json:"RestartRequired"`
		ProductID       []string `json:"ProductID"`
	} `json:"Remediations"`
}

type value struct {
	Value string `json:"Value"`
}

// cvrfTime: as datas do documento vêm sem fuso ("2026-10-02T07:00:00"); as da lista,
// com "Z". As duas são UTC.
type cvrfTime time.Time

func (t *cvrfTime) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05"} {
		if v, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			*t = cvrfTime(v.UTC())
			return nil
		}
	}
	return fmt.Errorf("data fora do formato esperado: %q", s)
}

// Parse lê um documento CVRF e o reduz.
func Parse(r io.Reader) (Document, error) {
	var raw rawDoc
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return Document{}, fmt.Errorf("documento do MSRC ilegível: %w", err)
	}
	d := Document{
		ID:      raw.DocumentTracking.Identification.ID.Value,
		Title:   raw.DocumentTitle.Value,
		Initial: time.Time(raw.DocumentTracking.InitialReleaseDate),
		Current: time.Time(raw.DocumentTracking.CurrentReleaseDate),
	}
	if d.ID == "" {
		return d, fmt.Errorf("documento do MSRC sem ID")
	}
	for _, p := range raw.ProductTree.FullProductName {
		d.Products = append(d.Products, Product{ID: p.ProductID, Name: p.Value})
	}

	for _, v := range raw.Vulnerability {
		vu := Vuln{CVE: v.CVE, Title: v.Title.Value}
		severity, impact := map[string]string{}, map[string]string{}
		for _, t := range v.Threats {
			switch t.Type {
			case threatExploitStatus:
				vu.Exploit = t.Description.Value
				st := parseExploitStatus(t.Description.Value)
				vu.Exploited = st["Exploited"] == "Yes"
				vu.Disclosed = st["Publicly Disclosed"] == "Yes"
			case threatSeverity:
				for _, id := range t.ProductID {
					severity[id] = t.Description.Value
				}
			case threatImpact:
				for _, id := range t.ProductID {
					impact[id] = t.Description.Value
				}
			}
		}
		d.Vulns = append(d.Vulns, vu)

		score, vector := map[string]float64{}, map[string]string{}
		for _, c := range v.CVSSScoreSets {
			for _, id := range c.ProductID {
				score[id], vector[id] = c.BaseScore, c.Vector
			}
		}
		seen := map[string]bool{}
		for _, s := range v.ProductStatuses {
			for _, id := range s.ProductID {
				if seen[id] {
					continue
				}
				seen[id] = true
				d.Affected = append(d.Affected, Affected{CVE: v.CVE, ProductID: id,
					Severity: severity[id], Impact: impact[id], BaseScore: score[id], Vector: vector[id]})
			}
		}

		for _, rm := range v.Remediations {
			if rm.Type != remediationVendor {
				continue
			}
			kb := strings.TrimSpace(rm.Description.Value)
			if !kbPattern.MatchString(kb) {
				d.SkippedFix++
				continue
			}
			for _, id := range rm.ProductID {
				d.Fixes = append(d.Fixes, Fix{CVE: v.CVE, ProductID: id, KB: kb, SubType: rm.SubType,
					FixedBuild: strings.TrimSpace(rm.FixedBuild), Supersedes: strings.TrimSpace(rm.Supercedence),
					RestartRequired: rm.RestartRequired.Value})
			}
		}
	}
	return d, nil
}

// parseExploitStatus lê "Publicly Disclosed:No;Exploited:No;Latest Software Release:...".
func parseExploitStatus(s string) map[string]string {
	m := map[string]string{}
	for _, part := range strings.Split(s, ";") {
		if k, v, ok := strings.Cut(part, ":"); ok {
			m[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return m
}
PATCHD_EOF
echo "gravado: internal/catalog/msrc/msrc.go"

mkdir -p internal/catalog/msrc
cat > internal/catalog/msrc/msrc_test.go <<'PATCHD_EOF'
package msrc

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestParseUpdates(t *testing.T) {
	const lista = `{"@odata.context":"x","value":[
	 {"ID":"2026-Sep","Alias":"2026-Sep","DocumentTitle":"September 2026 Security Updates","Severity":null,
	  "InitialReleaseDate":"2026-09-08T07:00:00Z","CurrentReleaseDate":"2026-10-04T01:53:47Z",
	  "CvrfUrl":"https://api.msrc.microsoft.com/cvrf/v3.0/cvrf/2026-Sep"}]}`
	us, err := ParseUpdates(strings.NewReader(lista))
	if err != nil || len(us) != 1 {
		t.Fatalf("%+v %v", us, err)
	}
	u := us[0]
	if u.ID != "2026-Sep" || !u.CurrentReleaseDate.Equal(time.Date(2026, 10, 4, 1, 53, 47, 0, time.UTC)) || !strings.HasSuffix(u.CvrfURL, "/2026-Sep") {
		t.Errorf("item: %+v", u)
	}
}

// Regras do parser num documento mínimo, no mesmo formato do real.
func TestParseRegras(t *testing.T) {
	const doc = `{"DocumentTitle":{"Value":"T"},
	 "DocumentTracking":{"Identification":{"ID":{"Value":"2026-Sep"}},"InitialReleaseDate":"2026-09-08T07:00:00","CurrentReleaseDate":"2026-10-04T01:53:47"},
	 "ProductTree":{"FullProductName":[{"ProductID":"1","Value":"Produto A"},{"ProductID":"2","Value":"Produto B"}]},
	 "Vulnerability":[{"CVE":"CVE-1","Title":{"Value":"Falha"},
	  "ProductStatuses":[{"ProductID":["1","2"]},{"ProductID":["1"]}],
	  "Threats":[{"Type":0,"Description":{"Value":"Remote Code Execution"},"ProductID":["1"]},
	             {"Type":3,"Description":{"Value":"Critical"},"ProductID":["1"]},
	             {"Type":3,"Description":{"Value":"Important"},"ProductID":["2"]},
	             {"Type":1,"Description":{"Value":"Publicly Disclosed:Yes;Exploited:Yes;Latest Software Release:Exploitation Detected"}}],
	  "CVSSScoreSets":[{"BaseScore":9.8,"Vector":"CVSS:3.1/AV:N","ProductID":["1"]}],
	  "Remediations":[{"Type":2,"SubType":"Security Update","Description":{"Value":"5124012"},"FixedBuild":"10.0.26200.9445","Supercedence":"5121000","RestartRequired":{"Value":"Yes"},"ProductID":["1","2"]},
	                  {"Type":2,"SubType":"Security Update","Description":{"Value":"Release Notes"},"FixedBuild":"","RestartRequired":{},"ProductID":["2"]},
	                  {"Type":3,"SubType":"5124012","ProductID":["1"]},
	                  {"Type":6,"Description":{"Value":"5124012"},"ProductID":["1"]}]}]}`
	d, err := Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	if d.ID != "2026-Sep" || !d.Current.Equal(time.Date(2026, 10, 4, 1, 53, 47, 0, time.UTC)) || len(d.Products) != 2 {
		t.Errorf("documento: %+v", d)
	}
	if len(d.Vulns) != 1 || !d.Vulns[0].Exploited || !d.Vulns[0].Disclosed {
		t.Errorf("exploração: %+v", d.Vulns)
	}
	// Um produto repetido nos ProductStatuses vira uma linha só; o CVSS é o do produto.
	if len(d.Affected) != 2 {
		t.Fatalf("afetados: %+v", d.Affected)
	}
	a1, a2 := d.Affected[0], d.Affected[1]
	if a1.ProductID != "1" || a1.Severity != "Critical" || a1.Impact != "Remote Code Execution" || a1.BaseScore != 9.8 {
		t.Errorf("produto 1: %+v", a1)
	}
	if a2.ProductID != "2" || a2.Severity != "Important" || a2.Impact != "" || a2.BaseScore != 0 {
		t.Errorf("produto 2 (sem impacto nem CVSS próprios): %+v", a2)
	}
	// Só o tipo 2 com número de KB vira correção: "Release Notes", tipo 3 e tipo 6 ficam de fora.
	if len(d.Fixes) != 2 || d.SkippedFix != 1 {
		t.Fatalf("correções: %+v (ignoradas %d)", d.Fixes, d.SkippedFix)
	}
	f := d.Fixes[0]
	if f.KB != "5124012" || f.FixedBuild != "10.0.26200.9445" || f.Supersedes != "5121000" || f.RestartRequired != "Yes" || f.SubType != "Security Update" {
		t.Errorf("correção: %+v", f)
	}
	if _, err := Parse(strings.NewReader(`{"DocumentTracking":{}}`)); err == nil {
		t.Error("documento sem ID deveria ser recusado")
	}
}

// O recorte do documento real de setembro de 2026 (scripts da Aula 6.1): os valores
// conferidos na saída da exploração precisam sair iguais do parser.
func TestRecorteReal(t *testing.T) {
	f, err := os.Open("testdata/2026-Sep-recorte.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	d, err := Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	if d.ID != "2026-Sep" || !d.Current.Equal(time.Date(2026, 10, 4, 1, 53, 47, 0, time.UTC)) {
		t.Errorf("documento: %s %v", d.ID, d.Current)
	}

	nomes := map[string]string{}
	for _, p := range d.Products {
		nomes[p.ID] = p.Name
	}
	if nomes["20853"] != "Windows 11 version 26H1 for x64-based Systems" {
		t.Errorf("produto 20853: %q", nomes["20853"])
	}

	var achou bool
	for _, x := range d.Fixes {
		if x.CVE == "CVE-2026-50349" && x.ProductID == "20853" {
			achou = true
			if x.KB != "5124012" || x.FixedBuild != "10.0.28000.2954" || x.Supersedes != "5121000" ||
				x.RestartRequired != "Yes" || x.SubType != "Security Update" {
				t.Errorf("correção da CVE-2026-50349 no 26H1 x64: %+v", x)
			}
		}
	}
	if !achou {
		t.Error("falta a correção da CVE-2026-50349 no produto 20853")
	}
	for _, a := range d.Affected {
		if a.CVE == "CVE-2026-50349" && a.ProductID == "20853" &&
			(a.Severity != "Important" || a.Impact != "Elevation of Privilege" || !strings.HasPrefix(a.Vector, "CVSS:3.1/")) {
			t.Errorf("CVE-2026-50349 no 26H1 x64: %+v", a)
		}
	}

	var explorada, build25H2 bool
	for _, v := range d.Vulns {
		explorada = explorada || v.Exploited
	}
	for _, x := range d.Fixes {
		build25H2 = build25H2 || (x.ProductID == "20438" && strings.HasPrefix(x.FixedBuild, "10.0.26200."))
		if x.KB == "" {
			t.Errorf("correção sem KB: %+v", x)
		}
	}
	if !explorada || !build25H2 || d.SkippedFix == 0 {
		t.Errorf("o recorte traz uma CVE explorada (%t), um build do 25H2 (%t) e uma correção sem KB ignorada (%d)",
			explorada, build25H2, d.SkippedFix)
	}
}

// O documento inteiro, baixado à parte: PATCHD_MSRC_DOC=/tmp/msrc-sep.json go test -v -run Completo
func TestDocumentoCompleto(t *testing.T) {
	path := os.Getenv("PATCHD_MSRC_DOC")
	if path == "" {
		t.Skip("defina PATCHD_MSRC_DOC com o caminho de um documento baixado")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	inicio := time.Now()
	d, err := Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	explorada := 0
	for _, v := range d.Vulns {
		if v.Exploited {
			explorada++
		}
	}
	t.Logf("%s (%s): %d produtos, %d CVEs (%d exploradas), %d ligações CVE×produto, %d correções, %d sem KB ignoradas, em %s",
		d.ID, d.Current.Format(time.RFC3339), len(d.Products), len(d.Vulns), explorada, len(d.Affected), len(d.Fixes), d.SkippedFix,
		time.Since(inicio).Round(time.Millisecond))
	if len(d.Vulns) == 0 || len(d.Fixes) == 0 {
		t.Error("documento sem CVEs ou sem correções")
	}
}
PATCHD_EOF
echo "gravado: internal/catalog/msrc/msrc_test.go"

mkdir -p internal/catalog/msrc
cat > internal/catalog/msrc/client.go <<'PATCHD_EOF'
package msrc

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// DefaultBaseURL é a API CVRF do MSRC. A versão 3.0 não exige chave.
const DefaultBaseURL = "https://api.msrc.microsoft.com/cvrf/v3.0"

// idPattern: os documentos são identificados por ano e mês em inglês (2026-Sep).
var idPattern = regexp.MustCompile(`^[0-9]{4}-[A-Z][a-z]{2}$`)

// Client baixa a lista e os documentos. Cada resposta tem um teto de tamanho: o documento
// de setembro de 2026 tem 20 MB, e um servidor que mandasse gigabytes não derruba o patchd.
type Client struct {
	BaseURL     string
	HTTP        *http.Client
	UserAgent   string
	MaxListSize int64
	MaxDocSize  int64
}

// NewClient monta o cliente com os padrões.
func NewClient(userAgent string) *Client {
	return &Client{
		BaseURL:     DefaultBaseURL,
		HTTP:        &http.Client{Timeout: 5 * time.Minute},
		UserAgent:   userAgent,
		MaxListSize: 16 << 20,
		MaxDocSize:  256 << 20,
	}
}

// Updates baixa a lista de documentos.
func (c *Client) Updates(ctx context.Context) ([]Update, error) {
	data, err := c.get(ctx, "/updates", c.MaxListSize)
	if err != nil {
		return nil, err
	}
	return ParseUpdates(bytes.NewReader(data))
}

// Document baixa e lê um documento.
func (c *Client) Document(ctx context.Context, id string) (Document, error) {
	if !idPattern.MatchString(id) {
		return Document{}, fmt.Errorf("ID de documento do MSRC inválido: %q", id)
	}
	data, err := c.get(ctx, "/cvrf/"+id, c.MaxDocSize)
	if err != nil {
		return Document{}, err
	}
	d, err := Parse(bytes.NewReader(data))
	if err != nil {
		return d, err
	}
	if d.ID != id {
		return d, fmt.Errorf("pedido o documento %s, recebido o %s", id, d.ID)
	}
	return d, nil
}

func (c *Client) get(ctx context.Context, path string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.BaseURL, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.UserAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		trecho, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return nil, fmt.Errorf("MSRC %s: %s: %s", path, resp.Status, strings.TrimSpace(string(trecho)))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("MSRC %s: %w", path, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("MSRC %s: resposta maior que o limite de %d bytes", path, limit)
	}
	return data, nil
}
PATCHD_EOF
echo "gravado: internal/catalog/msrc/client.go"

mkdir -p internal/catalog/msrc
cat > internal/catalog/msrc/client_test.go <<'PATCHD_EOF'
package msrc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestClient(t *testing.T) {
	recorte, err := os.ReadFile("testdata/2026-Sep-recorte.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/json" {
			http.Error(w, "sem Accept", http.StatusNotAcceptable)
			return
		}
		switch r.URL.Path {
		case "/updates":
			_, _ = w.Write([]byte(`{"value":[{"ID":"2026-Sep","InitialReleaseDate":"2026-09-08T07:00:00Z","CurrentReleaseDate":"2026-10-04T01:53:47Z"}]}`))
		case "/cvrf/2026-Sep":
			_, _ = w.Write(recorte)
		case "/cvrf/2026-Aug":
			_, _ = w.Write(recorte) // o servidor mandou o documento errado
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := NewClient("patchd-teste")
	c.BaseURL = srv.URL
	ctx := context.Background()

	us, err := c.Updates(ctx)
	if err != nil || len(us) != 1 || us[0].ID != "2026-Sep" {
		t.Fatalf("lista: %+v %v", us, err)
	}
	d, err := c.Document(ctx, "2026-Sep")
	if err != nil || d.ID != "2026-Sep" || len(d.Fixes) == 0 {
		t.Fatalf("documento: %v", err)
	}
	if _, err := c.Document(ctx, "2026-Aug"); err == nil || !strings.Contains(err.Error(), "recebido o 2026-Sep") {
		t.Errorf("documento trocado: %v", err)
	}
	if _, err := c.Document(ctx, "2026-Jan"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("inexistente: %v", err)
	}
	if _, err := c.Document(ctx, "../updates"); err == nil || !strings.Contains(err.Error(), "inválido") {
		t.Errorf("ID fora do padrão: %v", err)
	}
	c.MaxDocSize = 1 << 10
	if _, err := c.Document(ctx, "2026-Sep"); err == nil || !strings.Contains(err.Error(), "limite") {
		t.Errorf("limite de tamanho: %v", err)
	}
}
PATCHD_EOF
echo "gravado: internal/catalog/msrc/client_test.go"

mkdir -p internal/catalog/msrc/testdata
cat > internal/catalog/msrc/testdata/recorta.py <<'PATCHD_EOF'
# Recorta o documento do MSRC num exemplo pequeno para os testes: quatro CVEs
# escolhidas por propriedade e só os produtos que elas citam.
import json, sys, re
src, dst = sys.argv[1], sys.argv[2]
d = json.load(open(src))
vulns = d["Vulnerability"]

def produtos(v):
    ids = set()
    for s in v.get("ProductStatuses", []):
        ids.update(s.get("ProductID", []))
    for k in ("Threats", "Remediations", "CVSSScoreSets"):
        for x in v.get(k, []):
            ids.update(x.get("ProductID", []))
    return ids

def primeira(cond):
    return next(v for v in vulns if cond(v))

escolhidas = [
    primeira(lambda v: v["CVE"] == "CVE-2026-50349"),
    primeira(lambda v: any("Exploited:Yes" in t["Description"]["Value"] for t in v.get("Threats", []) if t.get("Type") == 1)),
    primeira(lambda v: any(r.get("Type") == 2 and "20438" in r.get("ProductID", []) and r.get("FixedBuild", "").startswith("10.0.26200.")
                           for r in v.get("Remediations", []))),
    primeira(lambda v: any(r.get("Type") == 2 and not re.fullmatch(r"[0-9]{6,8}", r.get("Description", {}).get("Value", ""))
                           for r in v.get("Remediations", []))),
]
unicas = list({v["CVE"]: v for v in escolhidas}.values())
ids = set().union(*(produtos(v) for v in unicas))
saida = {
    "DocumentTitle": d["DocumentTitle"],
    "DocumentTracking": d["DocumentTracking"],
    "ProductTree": {"FullProductName": [p for p in d["ProductTree"]["FullProductName"] if p["ProductID"] in ids]},
    "Vulnerability": unicas,
}
with open(dst, "w") as f:
    json.dump(saida, f, ensure_ascii=False, indent=1)
print("CVEs:", [v["CVE"] for v in unicas], "| produtos:", len(saida["ProductTree"]["FullProductName"]))
PATCHD_EOF
echo "gravado: internal/catalog/msrc/testdata/recorta.py"

mkdir -p internal/store/migrations
cat > internal/store/migrations/0004_catalogo_msrc.sql <<'PATCHD_EOF'
-- 0004: catálogo de segurança da Microsoft (Aula 6.1).
--
-- Um documento do MSRC é a unidade de sincronização: quando a lista do MSRC traz uma data
-- de revisão diferente, o documento inteiro é apagado e gravado de novo, numa transação.
-- Por isso as CVEs, as ligações e as correções pertencem ao documento (a mesma CVE pode
-- aparecer em mais de um documento, quando é revisada num mês seguinte).

CREATE TABLE msrc_documents (
    id               text        PRIMARY KEY COLLATE "C", -- ex.: 2026-Sep
    title            text        NOT NULL,
    initial_release  timestamptz NOT NULL,
    current_release  timestamptz NOT NULL,                -- a data da lista; é o que a sincronização compara
    fetched_at       timestamptz NOT NULL DEFAULT now(),
    skipped_fixes    integer     NOT NULL                 -- correções do tipo 2 sem número de KB
);

-- O ProductID do MSRC é estável entre os documentos; o nome mais recente vale.
CREATE TABLE msrc_products (
    id   text PRIMARY KEY COLLATE "C",
    name text NOT NULL
);

CREATE TABLE msrc_vulns (
    document_id text    NOT NULL REFERENCES msrc_documents (id) ON DELETE CASCADE,
    cve         text    NOT NULL COLLATE "C",
    title       text    NOT NULL,
    exploited   boolean NOT NULL,
    disclosed   boolean NOT NULL,
    exploit     text    NOT NULL, -- o texto original; vazio quando o MSRC não informa
    PRIMARY KEY (document_id, cve)
);
CREATE INDEX msrc_vulns_cve_idx ON msrc_vulns (cve);

CREATE TABLE msrc_affected (
    document_id text         NOT NULL,
    cve         text         NOT NULL COLLATE "C",
    product_id  text         NOT NULL COLLATE "C",
    severity    text         NOT NULL,
    impact      text         NOT NULL,
    base_score  numeric(3,1) NOT NULL,
    vector      text         NOT NULL,
    PRIMARY KEY (document_id, cve, product_id),
    FOREIGN KEY (document_id, cve) REFERENCES msrc_vulns ON DELETE CASCADE
);
CREATE INDEX msrc_affected_product_idx ON msrc_affected (product_id);

CREATE TABLE msrc_fixes (
    document_id      text NOT NULL,
    cve              text NOT NULL COLLATE "C",
    product_id       text NOT NULL COLLATE "C",
    kb               text NOT NULL,
    subtype          text NOT NULL,
    fixed_build      text NOT NULL, -- vazio quando o produto não tem build (Office, por exemplo)
    supersedes       text NOT NULL,
    restart_required text NOT NULL,
    PRIMARY KEY (document_id, cve, product_id, kb, fixed_build),
    FOREIGN KEY (document_id, cve) REFERENCES msrc_vulns ON DELETE CASCADE
);
CREATE INDEX msrc_fixes_product_idx ON msrc_fixes (product_id);
CREATE INDEX msrc_fixes_kb_idx      ON msrc_fixes (kb);
PATCHD_EOF
echo "gravado: internal/store/migrations/0004_catalogo_msrc.sql"

mkdir -p internal/store
cat > internal/store/catalog_msrc.go <<'PATCHD_EOF'
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tasantiago/patchd/internal/catalog/msrc"
)

// MSRCVersions devolve, para cada documento do MSRC guardado, a data de revisão da lista
// no momento em que foi baixado.
func (p *Postgres) MSRCVersions(ctx context.Context) (map[string]time.Time, error) {
	rows, err := p.pool.Query(ctx, `SELECT id, current_release FROM msrc_documents`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var id string
		var t time.Time
		if err := rows.Scan(&id, &t); err != nil {
			return nil, err
		}
		out[id] = t.UTC()
	}
	return out, rows.Err()
}

// SaveMSRCDocument grava um documento inteiro numa transação, substituindo a versão
// anterior dele. current é a data de revisão da lista (/updates), a que a próxima
// sincronização compara. As linhas vão por COPY: um mês tem dezenas de milhares.
func (p *Postgres) SaveMSRCDocument(ctx context.Context, d msrc.Document, current time.Time) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) // sem efeito depois do Commit

	if _, err := tx.Exec(ctx, `DELETE FROM msrc_documents WHERE id = $1`, d.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO msrc_documents (id, title, initial_release, current_release, skipped_fixes)
		VALUES ($1, $2, $3, $4, $5)`, d.ID, d.Title, d.Initial, current, d.SkippedFix); err != nil {
		return err
	}

	for _, pr := range d.Products {
		if _, err := tx.Exec(ctx, `
			INSERT INTO msrc_products (id, name) VALUES ($1, $2)
			ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name`, pr.ID, pr.Name); err != nil {
			return err
		}
	}

	// Repetições dentro do documento (a mesma CVE, ou a mesma correção listada duas vezes)
	// ficam com a primeira ocorrência: a chave primária não aceita duplicatas.
	vulns := [][]any{}
	seenV := map[string]bool{}
	for _, v := range d.Vulns {
		if seenV[v.CVE] {
			continue
		}
		seenV[v.CVE] = true
		vulns = append(vulns, []any{d.ID, v.CVE, v.Title, v.Exploited, v.Disclosed, v.Exploit})
	}
	affected := [][]any{}
	seenA := map[[2]string]bool{}
	for _, a := range d.Affected {
		k := [2]string{a.CVE, a.ProductID}
		if seenA[k] || !seenV[a.CVE] {
			continue
		}
		seenA[k] = true
		affected = append(affected, []any{d.ID, a.CVE, a.ProductID, a.Severity, a.Impact, a.BaseScore, a.Vector})
	}
	fixes := [][]any{}
	seenF := map[[4]string]bool{}
	for _, f := range d.Fixes {
		k := [4]string{f.CVE, f.ProductID, f.KB, f.FixedBuild}
		if seenF[k] || !seenV[f.CVE] {
			continue
		}
		seenF[k] = true
		fixes = append(fixes, []any{d.ID, f.CVE, f.ProductID, f.KB, f.SubType, f.FixedBuild, f.Supersedes, f.RestartRequired})
	}

	copies := []struct {
		table string
		cols  []string
		rows  [][]any
	}{
		{"msrc_vulns", []string{"document_id", "cve", "title", "exploited", "disclosed", "exploit"}, vulns},
		{"msrc_affected", []string{"document_id", "cve", "product_id", "severity", "impact", "base_score", "vector"}, affected},
		{"msrc_fixes", []string{"document_id", "cve", "product_id", "kb", "subtype", "fixed_build", "supersedes", "restart_required"}, fixes},
	}
	for _, c := range copies {
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{c.table}, c.cols, pgx.CopyFromRows(c.rows)); err != nil {
			return fmt.Errorf("%s: %w", c.table, err)
		}
	}
	return tx.Commit(ctx)
}

// MSRCDocumentSummary é uma linha do "catalog list".
type MSRCDocumentSummary struct {
	ID        string
	Title     string
	Current   time.Time
	FetchedAt time.Time
	Vulns     int
	Exploited int
	Fixes     int
}

// MSRCDocuments resume os documentos guardados, do mais novo para o mais velho.
func (p *Postgres) MSRCDocuments(ctx context.Context) ([]MSRCDocumentSummary, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT d.id, d.title, d.current_release, d.fetched_at,
		       (SELECT count(*) FROM msrc_vulns v WHERE v.document_id = d.id),
		       (SELECT count(*) FROM msrc_vulns v WHERE v.document_id = d.id AND v.exploited),
		       (SELECT count(*) FROM msrc_fixes f WHERE f.document_id = d.id)
		FROM msrc_documents d
		ORDER BY d.initial_release DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MSRCDocumentSummary
	for rows.Next() {
		var s MSRCDocumentSummary
		if err := rows.Scan(&s.ID, &s.Title, &s.Current, &s.FetchedAt, &s.Vulns, &s.Exploited, &s.Fixes); err != nil {
			return nil, err
		}
		s.Current, s.FetchedAt = s.Current.UTC(), s.FetchedAt.UTC()
		out = append(out, s)
	}
	return out, rows.Err()
}

// MSRCBuild é o maior build corrigido de um produto num documento.
type MSRCBuild struct {
	Product  string
	Document string
	Build    string
	KBs      string // os KBs que levam a esse build, separados por vírgula
}

// MSRCBuilds lista, para os produtos cujo nome começa com prefix (sem diferenciar maiúsculas:
// o próprio MSRC escreve "Version" e "version"), o maior build corrigido em cada documento. A comparação é numérica, parte a parte (10.0.26200.9445 como
// {10,0,26200,9445}), nunca como texto.
func (p *Postgres) MSRCBuilds(ctx context.Context, prefix string) ([]MSRCBuild, error) {
	rows, err := p.pool.Query(ctx, `
		WITH b AS (
			SELECT pr.name, f.document_id, f.kb, string_to_array(f.fixed_build, '.')::int[] AS v
			FROM msrc_fixes f JOIN msrc_products pr ON pr.id = f.product_id
			WHERE f.fixed_build ~ '^[0-9]+(\.[0-9]+)+$' AND pr.name ILIKE $1 || '%'
		), m AS (
			SELECT name, document_id, max(v) AS v FROM b GROUP BY name, document_id
		)
		SELECT m.name, m.document_id, array_to_string(m.v, '.'),
		       (SELECT string_agg(DISTINCT b.kb, ',' ORDER BY b.kb) FROM b
		         WHERE b.name = m.name AND b.document_id = m.document_id AND b.v = m.v)
		FROM m JOIN msrc_documents d ON d.id = m.document_id
		ORDER BY m.name, d.initial_release DESC`, prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MSRCBuild
	for rows.Next() {
		var b MSRCBuild
		if err := rows.Scan(&b.Product, &b.Document, &b.Build, &b.KBs); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
PATCHD_EOF
echo "gravado: internal/store/catalog_msrc.go"

mkdir -p internal/store
cat > internal/store/catalog_msrc_test.go <<'PATCHD_EOF'
package store

import (
	"context"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/catalog/msrc"
)

func documentoDeTeste(id string, inicial time.Time) msrc.Document {
	return msrc.Document{
		ID: id, Title: "Security Updates " + id, Initial: inicial, Current: inicial,
		Products: []msrc.Product{
			{ID: "20438", Name: "Windows 11 Version 25H2 for x64-based Systems"},
			{ID: "11", Name: "Microsoft Office LTSC 2024"},
		},
		Vulns: []msrc.Vuln{
			{CVE: "CVE-1", Title: "Win32k", Exploited: true, Exploit: "Publicly Disclosed:No;Exploited:Yes"},
			{CVE: "CVE-2", Title: "Kernel"},
			{CVE: "CVE-2", Title: "Kernel (repetida)"},
			{CVE: "CVE-3", Title: "Office"},
		},
		Affected: []msrc.Affected{
			{CVE: "CVE-1", ProductID: "20438", Severity: "Important", Impact: "Elevation of Privilege", BaseScore: 7.8, Vector: "CVSS:3.1/AV:L"},
			{CVE: "CVE-2", ProductID: "20438", Severity: "Critical", Impact: "Remote Code Execution", BaseScore: 9.8},
			{CVE: "CVE-3", ProductID: "11", Severity: "Important"},
		},
		Fixes: []msrc.Fix{
			// 10001 > 9445 como número; como texto, "9445" ganharia.
			{CVE: "CVE-1", ProductID: "20438", KB: "5124008", SubType: "Security Update", FixedBuild: "10.0.26200.9445"},
			{CVE: "CVE-2", ProductID: "20438", KB: "5125000", SubType: "Security Update", FixedBuild: "10.0.26200.10001"},
			{CVE: "CVE-2", ProductID: "20438", KB: "5125000", SubType: "Security Update", FixedBuild: "10.0.26200.10001"},
			{CVE: "CVE-3", ProductID: "11", KB: "5002900", SubType: "Security Update"},
		},
	}
}

func TestCatalogoMSRC(t *testing.T) {
	pool := bancoDeTeste(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	st := NewPostgres(pool)

	ago := time.Date(2026, 8, 11, 7, 0, 0, 0, time.UTC)
	set := time.Date(2026, 9, 8, 7, 0, 0, 0, time.UTC)
	rev1 := time.Date(2026, 10, 2, 14, 29, 13, 0, time.UTC)
	if err := st.SaveMSRCDocument(ctx, documentoDeTeste("2026-Aug", ago), rev1); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveMSRCDocument(ctx, documentoDeTeste("2026-Sep", set), set); err != nil {
		t.Fatal(err)
	}
	v, err := st.MSRCVersions(ctx)
	if err != nil || !v["2026-Aug"].Equal(rev1) || !v["2026-Sep"].Equal(set) {
		t.Fatalf("versões: %v %v", v, err)
	}

	docs, err := st.MSRCDocuments(ctx)
	if err != nil || len(docs) != 2 || docs[0].ID != "2026-Sep" {
		t.Fatalf("documentos (mais novo primeiro): %+v %v", docs, err)
	}
	if d := docs[0]; d.Vulns != 3 || d.Exploited != 1 || d.Fixes != 3 {
		t.Errorf("contagens (repetições descartadas): %+v", d)
	}

	// A revisão substitui o documento inteiro: a CVE-1 sai, e a data da lista muda.
	rev2 := time.Date(2026, 10, 4, 1, 53, 47, 0, time.UTC)
	novo := documentoDeTeste("2026-Sep", set)
	novo.Vulns = novo.Vulns[1:]
	novo.Affected = novo.Affected[1:]
	novo.Fixes = novo.Fixes[1:]
	if err := st.SaveMSRCDocument(ctx, novo, rev2); err != nil {
		t.Fatal(err)
	}
	docs, _ = st.MSRCDocuments(ctx)
	if d := docs[0]; d.Vulns != 2 || d.Exploited != 0 || d.Fixes != 2 || !d.Current.Equal(rev2) {
		t.Errorf("depois da revisão: %+v", d)
	}

	// Prefixo sem diferenciar maiúsculas: o MSRC escreve "Version" e "version".
	builds, err := st.MSRCBuilds(ctx, "windows 11")
	if err != nil || len(builds) != 2 {
		t.Fatalf("builds: %+v %v", builds, err)
	}
	for _, b := range builds {
		if b.Product != "Windows 11 Version 25H2 for x64-based Systems" || b.Build != "10.0.26200.10001" || b.KBs != "5125000" {
			t.Errorf("maior build por comparação numérica: %+v", b)
		}
	}
	if builds[0].Document != "2026-Sep" || builds[1].Document != "2026-Aug" {
		t.Errorf("ordem (mais novo primeiro): %+v", builds)
	}
}
PATCHD_EOF
echo "gravado: internal/store/catalog_msrc_test.go"

mkdir -p cmd/server
cat > cmd/server/catalog.go <<'PATCHD_EOF'
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"text/tabwriter"
	"time"

	"github.com/tasantiago/patchd/internal/buildinfo"
	"github.com/tasantiago/patchd/internal/catalog/msrc"
	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/store"
)

const catalogUsage = `uso: patchd-server catalog <comando> [opções]

comandos:
  sync [-months N]          baixa do MSRC os documentos novos ou revisados dos últimos N meses (padrão 12)
  list                      lista os documentos guardados
  builds [-product PREFIXO] maior build corrigido por produto e documento (padrão "Windows")

A URL do banco vem da mesma configuração do servidor (PATCHD_DATABASE_URL ou _FILE).
PATCHD_MSRC_URL troca a API do MSRC por outro endereço (padrão: https://api.msrc.microsoft.com/cvrf/v3.0).
`

// runCatalog administra o catálogo de segurança direto no banco.
func runCatalog(args []string, look config.Lookup, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, catalogUsage)
		return exitConfig
	}
	cfg, err := loadServerConfig(nil, look, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: configuração inválida:\n%v\n", err)
		return exitConfig
	}
	if cfg.DatabaseURL == "" {
		fmt.Fprintln(stderr, "patchd-server: o catálogo exige o PostgreSQL (PATCHD_DATABASE_URL ou PATCHD_DATABASE_URL_FILE)")
		return exitConfig
	}

	fs := flag.NewFlagSet("catalog "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	months := fs.Int("months", 12, "janela de meses do sync")
	product := fs.String("product", "Windows", "prefixo do nome do produto no builds")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 {
		fmt.Fprint(stderr, catalogUsage)
		return exitConfig
	}
	if *months < 1 || *months > 120 {
		fmt.Fprintln(stderr, "patchd-server: -months deve ficar entre 1 e 120")
		return exitConfig
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	pool, err := store.Connect(ctx, cfg.DatabaseURL, 10*time.Second)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	defer pool.Close()
	if err := store.Migrate(ctx, pool, logger); err != nil {
		fmt.Fprintf(stderr, "patchd-server: %v\n", err)
		return exitRuntime
	}
	st := store.NewPostgres(pool)

	switch args[0] {
	case "sync":
		src := msrc.NewClient("patchd-server/" + buildinfo.Get().Version)
		// Outro endereço (um espelho interno, por exemplo); o padrão é a API pública do MSRC.
		if u, ok := look("PATCHD_MSRC_URL"); ok && u != "" {
			src.BaseURL = u
		}
		since := time.Now().UTC().AddDate(0, -*months, 0)
		synced, failed, err := syncMSRC(ctx, src, st, since, stdout)
		if err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitRuntime
		}
		fmt.Fprintf(stdout, "%d documento(s) gravado(s), %d com falha\n", synced, failed)
		if failed > 0 {
			return exitRuntime
		}
		return exitOK
	case "list":
		docs, err := st.MSRCDocuments(ctx)
		if err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitRuntime
		}
		tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "DOCUMENTO\tREVISÃO (UTC)\tCVEs\tEXPLORADAS\tCORREÇÕES\tBAIXADO (UTC)\tTÍTULO")
		for _, d := range docs {
			fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%s\t%s\n", d.ID, d.Current.Format("2006-01-02 15:04"), d.Vulns, d.Exploited,
				d.Fixes, d.FetchedAt.Format("2006-01-02 15:04"), d.Title)
		}
		tw.Flush()
		return exitOK
	case "builds":
		bs, err := st.MSRCBuilds(ctx, *product)
		if err != nil {
			fmt.Fprintf(stderr, "patchd-server: %v\n", err)
			return exitRuntime
		}
		tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "PRODUTO\tDOCUMENTO\tBUILD CORRIGIDO\tKB")
		for _, b := range bs {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", b.Product, b.Document, b.Build, b.KBs)
		}
		tw.Flush()
		return exitOK
	default:
		fmt.Fprintf(stderr, "patchd-server: comando desconhecido %q\n\n%s", args[0], catalogUsage)
		return exitConfig
	}
}

// msrcSource e msrcStore são o que a sincronização usa; os testes passam versões falsas.
type msrcSource interface {
	Updates(ctx context.Context) ([]msrc.Update, error)
	Document(ctx context.Context, id string) (msrc.Document, error)
}

type msrcStore interface {
	MSRCVersions(ctx context.Context) (map[string]time.Time, error)
	SaveMSRCDocument(ctx context.Context, d msrc.Document, current time.Time) error
}

// syncMSRC baixa a lista e, da janela pedida, só os documentos novos ou com data de
// revisão diferente da guardada. A API do MSRC não aceita requisição condicional, então
// a lista (pequena) é o que evita baixar dezenas de MB à toa. Um documento que falha não
// impede os outros.
func syncMSRC(ctx context.Context, src msrcSource, st msrcStore, since time.Time, out io.Writer) (synced, failed int, err error) {
	updates, err := src.Updates(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("lista do MSRC: %w", err)
	}
	known, err := st.MSRCVersions(ctx)
	if err != nil {
		return 0, 0, err
	}
	// Do mais velho para o mais novo, numa cópia: a lista recebida não é alterada.
	updates = slices.Clone(updates)
	slices.SortFunc(updates, func(a, b msrc.Update) int { return a.InitialReleaseDate.Compare(b.InitialReleaseDate) })

	for _, u := range updates {
		if u.InitialReleaseDate.Before(since) {
			continue
		}
		prev, seen := known[u.ID]
		if seen && prev.Equal(u.CurrentReleaseDate) {
			fmt.Fprintf(out, "  %-9s em dia\n", u.ID)
			continue
		}
		start := time.Now()
		d, err := src.Document(ctx, u.ID)
		if err == nil {
			err = st.SaveMSRCDocument(ctx, d, u.CurrentReleaseDate)
		}
		if err != nil {
			failed++
			fmt.Fprintf(out, "  %-9s FALHOU: %v\n", u.ID, err)
			continue
		}
		synced++
		state := "novo"
		if seen {
			state = "revisado"
		}
		exploited := 0
		for _, v := range d.Vulns {
			if v.Exploited {
				exploited++
			}
		}
		fmt.Fprintf(out, "  %-9s %-8s %d CVEs (%d exploradas), %d correções (%d sem KB ignoradas), %s\n",
			u.ID, state, len(d.Vulns), exploited, len(d.Fixes), d.SkippedFix, time.Since(start).Round(100*time.Millisecond))
	}
	return synced, failed, nil
}
PATCHD_EOF
echo "gravado: cmd/server/catalog.go"

mkdir -p cmd/server
cat > cmd/server/catalog_test.go <<'PATCHD_EOF'
package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/catalog/msrc"
)

type fonteFalsa struct {
	lista    []msrc.Update
	baixados []string
	falha    string // ID que falha ao baixar
}

func (f *fonteFalsa) Updates(context.Context) ([]msrc.Update, error) { return f.lista, nil }

func (f *fonteFalsa) Document(_ context.Context, id string) (msrc.Document, error) {
	f.baixados = append(f.baixados, id)
	if id == f.falha {
		return msrc.Document{}, errors.New("rede caiu")
	}
	return msrc.Document{ID: id, Vulns: []msrc.Vuln{{CVE: "CVE-1", Exploited: true}}, Fixes: []msrc.Fix{{CVE: "CVE-1", KB: "5000000"}}}, nil
}

type bancoFalso struct{ versoes map[string]time.Time }

func (b *bancoFalso) MSRCVersions(context.Context) (map[string]time.Time, error) {
	out := map[string]time.Time{}
	for k, v := range b.versoes {
		out[k] = v
	}
	return out, nil
}

func (b *bancoFalso) SaveMSRCDocument(_ context.Context, d msrc.Document, current time.Time) error {
	b.versoes[d.ID] = current
	return nil
}

func dia(m time.Month, d int) time.Time { return time.Date(2026, m, d, 7, 0, 0, 0, time.UTC) }

func TestSyncMSRCBaixaSoONovoOuRevisado(t *testing.T) {
	fonte := &fonteFalsa{lista: []msrc.Update{
		{ID: "2026-Sep", InitialReleaseDate: dia(9, 8), CurrentReleaseDate: dia(10, 4)},
		{ID: "2025-Jan", InitialReleaseDate: time.Date(2025, 1, 14, 8, 0, 0, 0, time.UTC), CurrentReleaseDate: dia(1, 1)},
		{ID: "2026-Aug", InitialReleaseDate: dia(8, 11), CurrentReleaseDate: dia(10, 2)},
	}}
	banco := &bancoFalso{versoes: map[string]time.Time{}}
	desde := dia(1, 1) // fora da janela: 2025-Jan
	var out bytes.Buffer

	n, falhas, err := syncMSRC(context.Background(), fonte, banco, desde, &out)
	if err != nil || n != 2 || falhas != 0 || strings.Join(fonte.baixados, ",") != "2026-Aug,2026-Sep" || fonte.lista[0].ID != "2026-Sep" {
		t.Fatalf("primeira vez (em ordem, só a janela): %d %d %v %v\n%s", n, falhas, fonte.baixados, err, out.String())
	}
	if !strings.Contains(out.String(), "1 exploradas") {
		t.Errorf("resumo: %s", out.String())
	}

	// Nada mudou na lista: nada é baixado.
	fonte.baixados, out = nil, bytes.Buffer{}
	n, _, _ = syncMSRC(context.Background(), fonte, banco, desde, &out)
	if n != 0 || len(fonte.baixados) != 0 || strings.Count(out.String(), "em dia") != 2 {
		t.Errorf("sem mudanças: %d %v\n%s", n, fonte.baixados, out.String())
	}

	// Setembro revisado: só ele é baixado de novo. Agosto também mudou, mas falha; o
	// resto segue, e a falha é contada.
	for i := range fonte.lista {
		if fonte.lista[i].ID != "2025-Jan" {
			fonte.lista[i].CurrentReleaseDate = dia(10, 5)
		}
	}
	fonte.falha = "2026-Aug"
	fonte.baixados, out = nil, bytes.Buffer{}
	n, falhas, _ = syncMSRC(context.Background(), fonte, banco, desde, &out)
	if n != 1 || falhas != 1 || !strings.Contains(out.String(), "revisado") || !strings.Contains(out.String(), "FALHOU: rede caiu") {
		t.Errorf("revisão e falha: %d %d\n%s", n, falhas, out.String())
	}
	if !banco.versoes["2026-Sep"].Equal(dia(10, 5)) || !banco.versoes["2026-Aug"].Equal(dia(10, 2)) {
		t.Errorf("a data só avança no que foi gravado: %v", banco.versoes)
	}
}
PATCHD_EOF
echo "gravado: cmd/server/catalog_test.go"

python3 - <<'PATCHD_PY'
import pathlib
p = pathlib.Path('cmd/server/main.go')
s = p.read_text()
if 'runCatalog(' in s:
    print('main.go: já alterado')
else:
    a = """	if len(args) > 0 && args[0] == "release" {
		return runRelease(args[1:], look, stdout, stderr, info.Version)
	}"""
    assert s.count(a) == 1, 'main.go: âncora do release não encontrada'
    s = s.replace(a, a + """
	if len(args) > 0 && args[0] == "catalog" {
		return runCatalog(args[1:], look, stdout, stderr)
	}""")
    b = """	// Subcomandos de administração: "patchd-server token create|list|revoke" e
	// "patchd-server release current|publish|withdraw"."""
    assert s.count(b) == 1, 'main.go: âncora do comentário não encontrada'
    s = s.replace(b, """	// Subcomandos de administração: "patchd-server token create|list|revoke",
	// "patchd-server release current|publish|withdraw" e "patchd-server catalog sync|list|builds".""")
    p.write_text(s)
    print('main.go: alterado')
PATCHD_PY

F=internal/catalog/msrc/testdata/2026-Sep-recorte.json
if [ ! -f "$F" ]; then
  if [ -f /tmp/msrc-sep.json ]; then python3 internal/catalog/msrc/testdata/recorta.py /tmp/msrc-sep.json "$F"
  else echo "AVISO: falta $F; baixe /tmp/msrc-sep.json (Passo 2 da aula) e rode o recorta.py"; fi
fi
gofmt -l . | sed "s/^/gofmt pendente: /"
echo "pronto: rode os testes"
