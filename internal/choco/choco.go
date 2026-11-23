// Package choco lê, de um pacote do Chocolatey (o repositório da comunidade), o link e o
// SHA-256 do instalador que o script do pacote baixaria, sem executar o script: o .nupkg é um
// zip com o .nuspec (a versão) e o tools/chocolateyInstall.ps1, de onde saem as atribuições
// de texto ($url64 = '...', checksum64 = '...').
//
// O que o pacote diz é de quem o mantém (como o winget-pkgs): o servidor confere o SHA-256 do
// que baixa e o agente confere a assinatura antes de instalar. Pacote que traz o instalador
// dentro do .nupkg (embutido) não serve: o patchd não guarda o que veio de dentro do pacote.
package choco

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
)

// BaseURL: o repositório da comunidade (NuGet v2).
const BaseURL = "https://community.chocolatey.org/api/v2"

// MaxPackage: o .nupkg (os que só baixam têm alguns KB; os embutidos, centenas de MB).
const MaxPackage = 32 << 20

// Package é o pacote lido.
type Package struct {
	ID, Version string
	Script      string   // tools/chocolateyInstall.ps1
	Embedded    []string // instaladores dentro do .nupkg (.exe, .msi): o pacote embutido
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// ValidID: o id do pacote (googlechrome, adobereader, firefox).
func ValidID(id string) bool { return idPattern.MatchString(id) }

// Fetch baixa o pacote: a versão pedida, ou a mais nova (versão vazia).
func Fetch(ctx context.Context, hc *http.Client, base, id, version, userAgent string) (Package, error) {
	if !ValidID(id) {
		return Package{}, fmt.Errorf("pacote do Chocolatey %q inválido", id)
	}
	u := strings.TrimRight(base, "/") + "/package/" + url.PathEscape(id)
	if version != "" {
		u += "/" + url.PathEscape(version)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Package{}, err
	}
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return Package{}, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return Package{}, fmt.Errorf("o pacote %s%s não existe no Chocolatey", id, map[bool]string{true: " " + version, false: ""}[version != ""])
	case resp.StatusCode != http.StatusOK:
		return Package{}, fmt.Errorf("GET %s: %s", u, resp.Status)
	case resp.ContentLength > MaxPackage:
		return Package{}, embeddedTooBig(resp.ContentLength)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxPackage+1))
	if err != nil {
		return Package{}, err
	}
	if len(data) > MaxPackage {
		return Package{}, embeddedTooBig(int64(len(data)))
	}
	return Read(data)
}

func embeddedTooBig(n int64) error {
	return fmt.Errorf("o pacote tem %d MiB: traz o instalador dentro (pacote embutido), e o patchd só usa o link do fabricante; "+
		"use a fonte do site do fabricante ou o instalador próprio", n>>20)
}

// Read lê o .nupkg.
func Read(nupkg []byte) (Package, error) {
	zr, err := zip.NewReader(bytes.NewReader(nupkg), int64(len(nupkg)))
	if err != nil {
		return Package{}, fmt.Errorf("o pacote não é um .nupkg (zip): %w", err)
	}
	var p Package
	for _, f := range zr.File {
		name := strings.ToLower(strings.ReplaceAll(f.Name, "\\", "/"))
		switch {
		case strings.HasSuffix(name, ".nuspec") && !strings.Contains(name, "/"):
			b, err := readFile(f, 1<<20)
			if err != nil {
				return p, err
			}
			var spec struct {
				Metadata struct {
					ID      string `xml:"id"`
					Version string `xml:"version"`
				} `xml:"metadata"`
			}
			if err := xml.Unmarshal(b, &spec); err != nil {
				return p, fmt.Errorf("o .nuspec do pacote está ilegível: %w", err)
			}
			p.ID, p.Version = strings.TrimSpace(spec.Metadata.ID), strings.TrimSpace(spec.Metadata.Version)
		case name == "tools/chocolateyinstall.ps1":
			b, err := readFile(f, 1<<20)
			if err != nil {
				return p, err
			}
			p.Script = string(b)
		case strings.HasPrefix(name, "tools/") && (strings.HasSuffix(name, ".exe") || strings.HasSuffix(name, ".msi")):
			p.Embedded = append(p.Embedded, path.Base(f.Name))
		}
	}
	switch {
	case p.Version == "":
		return p, errors.New("o pacote não tem a versão (.nuspec)")
	case p.Script == "":
		return p, errors.New("o pacote não tem o tools/chocolateyInstall.ps1")
	}
	return p, nil
}

func readFile(f *zip.File, max int64) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%s grande demais", f.Name)
	}
	return b, nil
}

// Installer é o que o script baixaria.
type Installer struct {
	URL    string
	SHA256 string // vazio: o pacote não traz SHA-256 (ou traz outro tipo de soma)
	URLVar string // de onde o link saiu (url64bit, $MUIurl64...)
}

var (
	// $nome = 'texto' e, nas hashtables, nome = 'texto' (aspas simples ou duplas, sem $ dentro).
	assignText = regexp.MustCompile(`(?m)^[ \t]*\$?([A-Za-z_][A-Za-z0-9_]*)[ \t]*=[ \t]*(?:'([^'\r\n]*)'|"([^"$\r\n` + "`" + `]*)")`)
	// nome = $outra (a hashtable que aponta para uma variável).
	assignVar = regexp.MustCompile(`(?m)^[ \t]*\$?([A-Za-z_][A-Za-z0-9_]*)[ \t]*=[ \t]*\$([A-Za-z_][A-Za-z0-9_]*)[ \t]*(?:#.*)?$`)
	sha256Hex = regexp.MustCompile(`^[0-9A-Fa-f]{64}$`)
)

// Os nomes que o Chocolatey usa, do preferido (64 bits) para o último.
var (
	urlNames      = []string{"url64bit", "url64", "url_64", "url64bitx", "url"}
	checksumNames = []string{"checksum64", "checksum_64", "checksum"}
)

// Extract acha o link e o SHA-256 no script. urlVar e checksumVar escolhem as atribuições
// (o Adobe usa $MUIurl64 e $MUIchecksum64); vazios, os nomes de sempre, 64 bits primeiro.
func Extract(script, urlVar, checksumVar string) (Installer, error) {
	vals := map[string]string{}
	for _, m := range assignText.FindAllStringSubmatch(script, -1) {
		k := strings.ToLower(m[1])
		if _, seen := vals[k]; !seen {
			vals[k] = m[2] + m[3]
		}
	}
	for _, m := range assignVar.FindAllStringSubmatch(script, -1) {
		k, ref := strings.ToLower(m[1]), strings.ToLower(m[2])
		if _, seen := vals[k]; !seen {
			if v, ok := vals[ref]; ok {
				vals[k] = v
			}
		}
	}
	pick := func(explicit string, names []string) (string, string) {
		if explicit != "" {
			k := strings.ToLower(strings.TrimPrefix(explicit, "$"))
			return vals[k], explicit
		}
		for _, n := range names {
			if v := vals[n]; v != "" {
				return v, n
			}
		}
		return "", ""
	}
	link, from := pick(urlVar, urlNames)
	if link == "" {
		if urlVar != "" {
			return Installer{}, fmt.Errorf("o script do pacote não tem a atribuição %q com o link", urlVar)
		}
		return Installer{}, errors.New("o script do pacote não tem o link do instalador (url64bit, url64, url): informe a variável do link")
	}
	u, err := url.Parse(link)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return Installer{}, fmt.Errorf("o link do pacote (%s) não é https: o patchd só baixa por https", link)
	}
	in := Installer{URL: link, URLVar: from}
	sum, sumFrom := pick(checksumVar, checksumNames)
	if checksumVar == "" && strings.Contains(from, "64") != strings.Contains(sumFrom, "64") {
		// O link de 64 bits com a soma de 32 (ou o contrário): melhor nenhuma.
		sum = ""
	}
	kind := strings.ToLower(vals["checksumtype64"])
	if !strings.Contains(from, "64") || kind == "" {
		kind = strings.ToLower(cmpOr(vals["checksumtype"], kind))
	}
	if sha256Hex.MatchString(sum) && (kind == "" || kind == "sha256") {
		in.SHA256 = strings.ToLower(sum)
	}
	return in, nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// dateFix: a quarta parte com a data da correção do pacote (1.2.3.20251010), que não é da
// versão do programa.
var dateFix = regexp.MustCompile(`^(\d+\.\d+\.\d+)\.(20\d{6})$`)

// ProgramVersion: a versão do programa a partir da do pacote (sem a data da correção).
func ProgramVersion(v string) string {
	if m := dateFix.FindStringSubmatch(v); m != nil {
		return m[1]
	}
	return v
}
