// Package repoconfig grava, nas máquinas Linux, a configuração que faz o apt e o dnf usarem
// o cache de repositórios do patchd-server (Aula 6.6, parte 4d).
//
//   - apt: /etc/apt/apt.conf.d/90patchd-cache, com "Acquire::http::Proxy::<host>" só para as
//     origens anunciadas. As fontes da máquina não mudam.
//   - dnf5: /etc/dnf/repos.override.d/90-patchd-cache.repo, que troca o metalink dos
//     repositórios fedora e updates pelo baseurl do cache. Os .repo da distribuição não
//     mudam (uma atualização do fedora-repos não desfaz nada, e apagar o arquivo desfaz tudo).
//
// Os dois arquivos são do patchd: o agente os reescreve quando o anúncio muda e os apaga
// quando o servidor deixa de anunciar o cache ou quando o cache não responde (o firewall é
// transparente: sem o cache, a máquina volta a buscar direto na origem).
//
// https://manpages.ubuntu.com/manpages/resolute/man5/apt.conf.5.html
// https://dnf5.readthedocs.io/en/latest/dnf5.conf.5.html
package repoconfig

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

const (
	AptFile    = "/etc/apt/apt.conf.d/90patchd-cache"
	DnfFile    = "/etc/dnf/repos.override.d/90-patchd-cache.repo"
	aptGet     = "/usr/bin/apt-get"
	dnf5       = "/usr/bin/dnf5"
	aptConfDir = "/etc/apt/apt.conf.d"
	checkWait  = 5 * time.Second
)

// Config grava e remove os arquivos. Root é a raiz do sistema de arquivos ("" = /; outra
// nos testes).
type Config struct {
	Root string
	HTTP *http.Client
}

// Result diz o que mudou numa aplicação.
type Result struct {
	Written  []string // arquivos criados ou reescritos
	Removed  []string // arquivos apagados
	Managers []string // gerenciadores encontrados: apt, dnf
}

// Changed diz se algum arquivo mudou.
func (r Result) Changed() bool { return len(r.Written)+len(r.Removed) > 0 }

func (c *Config) path(p string) string { return filepath.Join(c.Root, filepath.FromSlash(p)) }

func (c *Config) exists(p string) bool {
	_, err := os.Stat(c.path(p))
	return err == nil
}

// Managers lista os gerenciadores de pacotes desta máquina que o patchd sabe configurar.
func (c *Config) Managers() []string {
	var ms []string
	if c.exists(aptGet) && c.exists(aptConfDir) {
		ms = append(ms, "apt")
	}
	if c.exists(dnf5) {
		ms = append(ms, "dnf")
	}
	return ms
}

// AptContent é o arquivo do apt para o anúncio.
func AptContent(rc protocol.RepoCache) []byte {
	var b bytes.Buffer
	b.WriteString("// Gerado pelo patchd-agent: o apt usa o cache de repositórios do patchd-server para estas\n")
	b.WriteString("// origens. O agente apaga este arquivo quando o servidor deixa de anunciar o cache.\n")
	for _, h := range rc.AptHosts {
		fmt.Fprintf(&b, "Acquire::http::Proxy::%s %q;\n", h, rc.Base())
	}
	return b.Bytes()
}

// DnfContent é o override do dnf5 para o anúncio.
func DnfContent(rc protocol.RepoCache) []byte {
	var b bytes.Buffer
	b.WriteString("# Gerado pelo patchd-agent: o dnf usa o cache de repositórios do patchd-server, que confere\n")
	b.WriteString("# o repomd.xml contra o metalink do Fedora. O agente apaga este arquivo quando o servidor\n")
	b.WriteString("# deixa de anunciar o cache.\n")
	for _, repo := range []string{"fedora", "updates"} {
		fmt.Fprintf(&b, "\n[%s]\nbaseurl=%s/repo/fedora/%s/$releasever/$basearch/\nmetalink=\nmirrorlist=\n", repo, rc.Base(), repo)
	}
	return b.Bytes()
}

// Apply grava a configuração do anúncio para os gerenciadores encontrados, depois de
// conferir que o cache responde. Arquivos iguais ao desejado não são reescritos.
func (c *Config) Apply(ctx context.Context, rc protocol.RepoCache) (Result, error) {
	res := Result{Managers: c.Managers()}
	if err := rc.Validate(); err != nil {
		return res, fmt.Errorf("anúncio recusado: %w", err)
	}
	if len(res.Managers) == 0 {
		return res, nil
	}
	if err := c.check(ctx, rc); err != nil {
		return res, err
	}
	for _, m := range res.Managers {
		file, content := AptFile, AptContent(rc)
		if m == "dnf" {
			file, content = DnfFile, DnfContent(rc)
		}
		changed, err := c.write(file, content)
		if err != nil {
			return res, err
		}
		if changed {
			res.Written = append(res.Written, file)
		}
	}
	return res, nil
}

// Remove apaga os arquivos do patchd (os que existirem).
func (c *Config) Remove() (Result, error) {
	res := Result{Managers: c.Managers()}
	var errs []error
	for _, f := range []string{AptFile, DnfFile} {
		err := os.Remove(c.path(f))
		switch {
		case err == nil:
			res.Removed = append(res.Removed, f)
		case !errors.Is(err, fs.ErrNotExist):
			errs = append(errs, err)
		}
	}
	return res, errors.Join(errs...)
}

// Sync é o que o laço de check-in faz: anúncio presente e cache respondendo, aplica;
// anúncio ausente ou cache fora, remove. Devolve o erro que levou à remoção, se houver.
func (c *Config) Sync(ctx context.Context, rc *protocol.RepoCache) (Result, error) {
	if rc == nil {
		return c.Remove()
	}
	res, err := c.Apply(ctx, *rc)
	if err == nil {
		return res, nil
	}
	rm, rerr := c.Remove()
	rm.Managers = res.Managers
	return rm, errors.Join(err, rerr)
}

// Installed devolve os arquivos do patchd presentes, com o conteúdo.
func (c *Config) Installed() map[string]string {
	out := map[string]string{}
	for _, f := range []string{AptFile, DnfFile} {
		if b, err := os.ReadFile(c.path(f)); err == nil {
			out[f] = string(b)
		}
	}
	return out
}

// check confere que o cache responde (/repo/stats só existe com o cache ligado).
func (c *Config) check(ctx context.Context, rc protocol.RepoCache) error {
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: checkWait}
	}
	ctx, cancel := context.WithTimeout(ctx, checkWait)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rc.Base()+"/repo/stats", nil)
	if err != nil {
		return err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("o cache não respondeu em %s: %w", rc.Base(), err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("o cache em %s respondeu %s em /repo/stats", rc.Base(), resp.Status)
	}
	return nil
}

// write grava só se o conteúdo mudou (0644: o apt e o dnf rodam como root, mas leem
// configuração legível por todos).
func (c *Config) write(file string, content []byte) (bool, error) {
	p := c.path(file)
	if old, err := os.ReadFile(p); err == nil && bytes.Equal(old, content) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return false, err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, content, 0o644); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return false, err
	}
	return true, nil
}

// Summary resume o resultado numa linha (log e terminal).
func (r Result) Summary() string {
	parts := []string{"gerenciadores: " + strings.Join(r.Managers, ",")}
	if len(r.Managers) == 0 {
		parts[0] = "nenhum gerenciador suportado (apt ou dnf5)"
	}
	if len(r.Written) > 0 {
		parts = append(parts, "gravado: "+strings.Join(r.Written, ", "))
	}
	if len(r.Removed) > 0 {
		parts = append(parts, "removido: "+strings.Join(r.Removed, ", "))
	}
	return strings.Join(parts, "; ")
}
