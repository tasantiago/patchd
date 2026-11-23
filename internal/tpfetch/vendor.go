package tpfetch

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/authsig"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/pkgcache"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
	"github.com/tasantiago/patchd/internal/winget"
)

// O instalador do site do fabricante (store.InstallerSource com Kind url), no lugar do
// winget: o link com {versao} (a versão de referência que o patchd acompanha, ou a pedida),
// o tipo e os argumentos. O SHA-256 vem do arquivo de somas do fabricante (SHA256SUMS), se
// houver; sem ele, o servidor calcula o SHA-256 do que baixou e exige a assinatura da regra
// (conferida aqui e de novo no agente, com o Windows). Exemplo, o Firefox:
//
//	https://ftp.mozilla.org/pub/firefox/releases/{versao}/win64/pt-BR/Firefox%20Setup%20{versao}.msi
//	https://ftp.mozilla.org/pub/firefox/releases/{versao}/SHA256SUMS

// VersionPlaceholder: onde o link leva a versão.
const VersionPlaceholder = "{versao}"

var (
	vendorVersion = regexp.MustCompile(`^[0-9][0-9A-Za-z._+-]{0,63}$`)
	sha256Line    = regexp.MustCompile(`^[0-9A-Fa-f]{64}$`)
)

// ExpandVendor monta o link da versão.
func ExpandVendor(template, version string) string {
	return strings.ReplaceAll(template, VersionPlaceholder, version)
}

// CheckVendor confere a fonte do fabricante antes de gravar.
func CheckVendor(s store.InstallerSource, rule thirdparty.App) error {
	if rule.Install == nil || rule.Uploaded() || rule.OS != "windows" {
		return errors.New("a fonte do fabricante vale para os programas do Windows com instalação pelo winget")
	}
	check := func(what, raw string, needVersion bool) error {
		if needVersion && !strings.Contains(raw, VersionPlaceholder) {
			return fmt.Errorf("o %s precisa ter %s no lugar da versão (ex.: .../releases/%s/...)", what, VersionPlaceholder, VersionPlaceholder)
		}
		u, err := url.Parse(ExpandVendor(raw, "1.0"))
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
			return fmt.Errorf("o %s tem de ser um endereço https sem usuário e senha", what)
		}
		return nil
	}
	if err := check("link do instalador", s.URLTemplate, true); err != nil {
		return err
	}
	if s.ChecksumURL != "" {
		if err := check("link do arquivo de SHA-256", s.ChecksumURL, false); err != nil {
			return err
		}
	}
	if !winget.Supported(s.Type) {
		return fmt.Errorf("tipo %q: use msi, wix, nullsoft, inno, burn ou exe", s.Type)
	}
	if s.Type == "exe" && strings.TrimSpace(s.Arguments) == "" {
		return errors.New("o exe genérico exige os argumentos da instalação silenciosa do fabricante")
	}
	if _, err := jobs.SplitArgs(s.Type, cmp.Or(s.Arguments, winget.SilentDefault(s.Type))); err != nil {
		return fmt.Errorf("argumentos: %w", err)
	}
	if s.ChecksumURL == "" && rule.Install.Signer == "" {
		return errors.New("sem o arquivo de SHA-256 do fabricante, o programa precisa exigir a assinatura (O=): é ela que garante o arquivo")
	}
	return nil
}

// vendorFile: o nome do arquivo no link (o que o arquivo de somas lista).
func vendorFile(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return ""
	}
	name, err := url.PathUnescape(path.Base(p.EscapedPath()))
	if err != nil {
		return path.Base(p.Path)
	}
	return name
}

// sumFor acha, no arquivo de somas ("<sha256>  <caminho>" por linha, o formato do sha256sum),
// o SHA-256 do arquivo: a linha cujo caminho termina no nome, com o caminho mais parecido
// com o do link (o SHA256SUMS da Mozilla lista todos os idiomas e sistemas).
func sumFor(sums []byte, link string) (string, error) {
	name := vendorFile(link)
	p, _ := url.Parse(link)
	full, _ := url.PathUnescape(p.EscapedPath())
	best, bestLen := "", -1
	sc := bufio.NewScanner(bytes.NewReader(sums))
	sc.Buffer(make([]byte, 64<<10), 64<<10)
	for sc.Scan() {
		hash, file, ok := strings.Cut(strings.TrimSpace(sc.Text()), " ")
		file = strings.TrimLeft(strings.TrimSpace(file), "*")
		if !ok || !sha256Line.MatchString(hash) || path.Base(file) != name {
			continue
		}
		if strings.HasSuffix(full, "/"+file) || strings.HasSuffix(full, file) {
			if len(file) > bestLen {
				best, bestLen = strings.ToLower(hash), len(file)
			}
		} else if best == "" {
			best = strings.ToLower(hash) // o nome bate, o caminho não: fica, se não houver melhor
		}
	}
	if best == "" {
		return "", fmt.Errorf("o arquivo de SHA-256 não tem a linha de %q", name)
	}
	return best, nil
}

// maxSums: o arquivo de somas (o da Mozilla tem alguns MB: todos os idiomas e sistemas).
const maxSums = 16 << 20

func getSums(ctx context.Context, hc *http.Client, link, ua string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", ua)
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", link, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxSums+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxSums {
		return nil, errors.New("o arquivo de SHA-256 passou de 16 MiB")
	}
	return b, nil
}

// Vendor baixa, confere e registra o instalador do site do fabricante para a versão
// (version: a de referência ou a pedida), escrevendo o andamento em out, como o One.
func Vendor(ctx context.Context, hc *http.Client, f *pkgcache.Fetcher, st Store, a thirdparty.App, src store.InstallerSource,
	version, dir string, out io.Writer) bool {
	fail := func(format string, args ...any) bool {
		fmt.Fprintf(out, "  ERRO        "+format+"\n", args...)
		return false
	}
	link := ExpandVendor(src.URLTemplate, version)
	fmt.Fprintf(out, "%s (%s) ← site do fabricante · %s · %s\n  link        %s\n", a.Name, a.ID, version, src.Type, link)
	if !vendorVersion.MatchString(version) {
		return fail("versão %q: precisa começar por um número (a de referência do programa ainda não foi buscada?)", version)
	}
	if err := CheckVendor(src, a); err != nil {
		return fail("%v", err)
	}
	msi := src.Type == "msi" || src.Type == "wix"
	progress := func(line string) { fmt.Fprintf(out, "              %s\n", line) }
	var res pkgcache.Result
	var sha string
	if src.ChecksumURL != "" {
		sumsLink := ExpandVendor(src.ChecksumURL, version)
		sums, err := getSums(ctx, hc, sumsLink, f.UserAgent)
		if err != nil {
			return fail("arquivo de SHA-256: %v", err)
		}
		if sha, err = sumFor(sums, link); err != nil {
			return fail("%v (%s)", err, sumsLink)
		}
		fmt.Fprintf(out, "  SHA-256     %s… (do arquivo do fabricante)\n", sha[:12])
		if res, err = f.Fetch(ctx, dir, link, sha, msi, progress); err != nil {
			var hm *pkgcache.HashMismatchError
			if errors.As(err, &hm) {
				fmt.Fprintf(out, "  RECUSADO    %v\n", err)
				return false
			}
			return fail("%v", err)
		}
	} else {
		var err error
		if res, sha, err = f.FetchAny(ctx, dir, link, msi, progress); err != nil {
			return fail("%v", err)
		}
		fmt.Fprintf(out, "  SHA-256     %s… (calculado: o fabricante não publica; vale a assinatura)\n", sha[:12])
	}
	// A assinatura: a da regra, já aqui (o agente confere de novo, com o Windows).
	if want := a.Install.Signer; want != "" {
		if err := checkSigner(res.Path, want); err != nil {
			if src.ChecksumURL == "" {
				_ = os.Remove(res.Path) // sem o SHA-256 do fabricante, o arquivo não fica
			}
			fmt.Fprintf(out, "  RECUSADO    %v; nada foi guardado\n", err)
			return false
		}
		fmt.Fprintf(out, "  assinatura  %s\n", want)
	}
	if res.Cached {
		fmt.Fprintf(out, "  no cache    %s (%s), conferido\n", pkgcache.FileName(sha, msi), pkgcache.Bytes(res.Size))
	} else {
		fmt.Fprintf(out, "  baixado     %s (%s) em %s\n", pkgcache.FileName(sha, msi), pkgcache.Bytes(res.Size), res.Duration.Round(100*time.Millisecond))
	}
	created, err := st.SaveThirdPartyPackage(ctx, store.ThirdPartyPackage{AppID: a.ID, Version: version, Architecture: "x64", Type: src.Type,
		Scope: "machine", URL: link, SHA256: sha, Size: res.Size, Arguments: cmp.Or(strings.TrimSpace(src.Arguments), winget.SilentDefault(src.Type))})
	var pc *store.PackageChangedError
	switch {
	case errors.As(err, &pc):
		fmt.Fprintf(out, "  RECUSADO    %v\n", err)
		return false
	case err != nil:
		return fail("registrar: %v", err)
	case created:
		fmt.Fprintln(out, "  registrado  novo")
	default:
		fmt.Fprintln(out, "  registrado  já estava")
	}
	return true
}

// checkSigner: o arquivo é assinado pela organização (lido, sem conferir a cadeia).
func checkSigner(file, org string) error {
	fh, err := os.Open(file)
	if err != nil {
		return err
	}
	defer fh.Close()
	st, err := fh.Stat()
	if err != nil {
		return err
	}
	s, err := authsig.Read(fh, st.Size())
	switch {
	case errors.Is(err, authsig.ErrUnsigned):
		return fmt.Errorf("o arquivo baixado não é assinado, e o programa exige a assinatura de %q", org)
	case err != nil:
		return fmt.Errorf("não deu para ler a assinatura do arquivo baixado: %w", err)
	case s.Organization != org:
		return fmt.Errorf("o arquivo baixado é assinado por %q, não por %q", s.Organization, org)
	}
	return nil
}
