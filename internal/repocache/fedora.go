package repocache

// Fedora (Aula 6.6, parte 4c). O dnf não passa por proxy aqui: o metalink e quase todos os
// espelhos são HTTPS, e um proxy só veria um túnel. A máquina aponta o baseurl para o
// servidor:
//
//	baseurl=http://<patchd>/repo/fedora/fedora/$releasever/$basearch/
//	baseurl=http://<patchd>/repo/fedora/updates/$releasever/$basearch/
//
// e perde o metalink, que era a única conferência dos metadados (o repomd.xml do Fedora não
// é assinado: repo_gpgcheck=0). Por isso o servidor faz essa conferência no lugar dela:
// busca o metalink por HTTPS e só entrega um repomd.xml cujo SHA-256 esteja nele (o atual
// ou um dos anteriores que o Fedora ainda aceita). Os arquivos de repodata/ têm o SHA-256
// no nome e são guardados pelo hash; os .rpm, pelo caminho, e continuam conferidos pelo
// dnf da máquina (gpgcheck=1, com a chave do Fedora).
//
// https://fedoraproject.org/wiki/Infrastructure/MirrorManager
// https://dnf5.readthedocs.io/en/latest/dnf5.conf.5.html

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/httpx"
)

const (
	// DefaultMetalinkURL é o metalink oficial do Fedora.
	DefaultMetalinkURL = "https://mirrors.fedoraproject.org/metalink"

	fedoraPrefix   = "/repo/fedora/"
	maxMetalink    = 4 << 20
	maxRepomd      = 4 << 20
	mirrorsToTry   = 5
	metalinkOrigin = "metalink" // chave da origem do metalink no registro de falhas
)

// fedoraRepos liga o nome no caminho ao repositório do metalink.
var fedoraRepos = map[string]string{
	"fedora":  "fedora-%s",
	"updates": "updates-released-f%s",
}

var (
	fedoraArches    = map[string]bool{"x86_64": true, "aarch64": true}
	releasePattern  = regexp.MustCompile(`^[1-9][0-9]{1,2}$`)
	repodataPattern = regexp.MustCompile(`^repodata/([0-9a-f]{64})-[A-Za-z0-9._+-]+$`)
	rpmPattern      = regexp.MustCompile(`^Packages/[^/]+/[^/]+\.rpm$`)
)

// metalinkInfo é o que interessa do metalink de um repositório.
type metalinkInfo struct {
	fetched   time.Time
	current   string          // SHA-256 do repomd.xml atual
	allowed   map[string]bool // o atual e os anteriores aceitos (mm0:alternate)
	timestamp time.Time       // publicação do atual
	mirrors   []string        // bases https dos espelhos, terminando em "/"
}

// metalinkXML: os campos usados do formato (os nomes casam em qualquer namespace).
type metalinkXML struct {
	Files []struct {
		Name       string   `xml:"name,attr"`
		Timestamp  int64    `xml:"timestamp"`
		Hashes     []mlHash `xml:"verification>hash"`
		Alternates []struct {
			Hashes []mlHash `xml:"verification>hash"`
		} `xml:"alternates>alternate"`
		URLs []struct {
			Protocol string `xml:"protocol,attr"`
			URL      string `xml:",chardata"`
		} `xml:"resources>url"`
	} `xml:"files>file"`
}

type mlHash struct {
	Type  string `xml:"type,attr"`
	Value string `xml:",chardata"`
}

func sha256Of(hs []mlHash) string {
	for _, h := range hs {
		if h.Type == "sha256" {
			return strings.TrimSpace(h.Value)
		}
	}
	return ""
}

// parseMetalink lê o metalink; httpOK aceita espelhos http (só nos testes).
func parseMetalink(b []byte, httpOK bool) (*metalinkInfo, error) {
	var m metalinkXML
	if err := xml.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("metalink ilegível: %w", err)
	}
	for _, f := range m.Files {
		if f.Name != "repomd.xml" {
			continue
		}
		info := &metalinkInfo{current: sha256Of(f.Hashes), allowed: map[string]bool{}}
		if f.Timestamp > 0 {
			info.timestamp = time.Unix(f.Timestamp, 0).UTC()
		}
		if !sha256Pattern.MatchString(info.current) {
			return nil, errors.New("metalink sem o SHA-256 do repomd.xml")
		}
		info.allowed[info.current] = true
		for _, a := range f.Alternates {
			if s := sha256Of(a.Hashes); sha256Pattern.MatchString(s) {
				info.allowed[s] = true
			}
		}
		for _, u := range f.URLs {
			raw := strings.TrimSpace(u.URL)
			pu, err := url.Parse(raw)
			if err != nil || pu.Host == "" || !strings.HasSuffix(pu.Path, "/repodata/repomd.xml") {
				continue
			}
			if pu.Scheme != "https" && !(httpOK && pu.Scheme == "http") {
				continue
			}
			info.mirrors = append(info.mirrors, strings.TrimSuffix(raw, "repodata/repomd.xml"))
		}
		if len(info.mirrors) == 0 {
			return nil, errors.New("metalink sem espelhos https")
		}
		return info, nil
	}
	return nil, errors.New("metalink sem o repomd.xml")
}

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// metalink devolve o metalink do repositório: o guardado na memória se tiver menos de um
// minuto; senão, o da origem. Com a origem fora, devolve o guardado (se houver) com o erro.
func (c *Cache) metalink(ctx context.Context, repo, rel, arch string) (*metalinkInfo, bool, error) {
	key := repo + "/" + rel + "/" + arch
	c.metaMu.Lock()
	old := c.metas[key]
	c.metaMu.Unlock()
	if old != nil && c.clock().Sub(old.fetched) < indexFresh {
		return old, true, nil
	}
	if old != nil && c.isDown(metalinkOrigin) {
		return old, true, errors.New("o metalink falhou há menos de 1 minuto; sem nova tentativa")
	}
	u := fmt.Sprintf("%s?repo=%s&arch=%s", c.MetalinkURL, url.QueryEscape(fmt.Sprintf(fedoraRepos[repo], rel)), url.QueryEscape(arch))
	info, err := c.fetchMetalink(ctx, u)
	if err != nil {
		c.markDown(metalinkOrigin)
		return old, true, err
	}
	c.markUp(metalinkOrigin)
	info.fetched = c.clock()
	c.metaMu.Lock()
	if c.metas == nil {
		c.metas = map[string]*metalinkInfo{}
	}
	c.metas[key] = info
	c.metaMu.Unlock()
	return info, false, nil
}

func (c *Cache) fetchMetalink(ctx context.Context, u string) (*metalinkInfo, error) {
	b, err := c.get(ctx, u, maxMetalink)
	if err != nil {
		return nil, fmt.Errorf("metalink: %w", err)
	}
	return parseMetalink(b, c.httpOK)
}

// get busca um arquivo pequeno inteiro na memória.
func (c *Cache) get(ctx context.Context, u string, limit int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, c.idle())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s respondeu %s", req.URL.Host, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errors.New("resposta maior que o limite")
	}
	return b, nil
}

// serveFedora atende /repo/fedora/{repo}/{versão}/{arquitetura}/{caminho}.
func (c *Cache) serveFedora(w http.ResponseWriter, r *http.Request) {
	start := c.clock()
	w = httpx.ExtendWrites(w, c.idle())
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, fedoraPrefix), "/", 4)
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "método não permitido", http.StatusMethodNotAllowed)
		return
	}
	if len(parts) != 4 || fedoraRepos[parts[0]] == "" || !releasePattern.MatchString(parts[1]) || !fedoraArches[parts[2]] {
		http.Error(w, "use /repo/fedora/{fedora|updates}/{versão}/{x86_64|aarch64}/...", http.StatusNotFound)
		return
	}
	repo, rel, arch, rest := parts[0], parts[1], parts[2], parts[3]
	if rest == "" || r.URL.RawQuery != "" || path.Clean("/"+rest) != "/"+rest || strings.Contains(rest, "\\") {
		http.Error(w, "caminho inválido", http.StatusBadRequest)
		return
	}

	var result string
	var n int64
	var err error
	switch {
	case rest == "repodata/repomd.xml" && r.Method == http.MethodGet:
		result, n, err = c.serveRepomd(w, r, repo, rel, arch)
	default:
		ml, _, merr := c.metalink(r.Context(), repo, rel, arch)
		if ml == nil {
			w.Header().Set(cacheHeader, "MISS")
			http.Error(w, "metalink do Fedora indisponível", http.StatusBadGateway)
			result, err = "MISS", merr
			break
		}
		origins := make([]string, 0, mirrorsToTry)
		for _, m := range ml.mirrors[:min(len(ml.mirrors), mirrorsToTry)] {
			origins = append(origins, m+rest)
		}
		switch m := repodataPattern.FindStringSubmatch(rest); {
		case r.Method == http.MethodHead:
			result, n, err = c.pass(w, r, "", origins[0])
		case m != nil:
			result, n, err = c.serveObject(w, r, "", origins, c.shaFile(m[1]), m[1])
		case rpmPattern.MatchString(rest):
			file := filepath.Join(c.Dir, "pool", "fedora", repo, rel, arch, filepath.FromSlash(rest))
			result, n, err = c.serveObject(w, r, "", origins, file, "")
		default:
			result, n, err = c.pass(w, r, "", origins[0])
		}
	}
	c.logResult(r, start, result, "fedora", repo+"/"+rel+"/"+arch+"/"+rest, n, err)
}

// serveRepomd entrega o repomd.xml só se o SHA-256 dele estiver no metalink.
func (c *Cache) serveRepomd(w http.ResponseWriter, r *http.Request, repo, rel, arch string) (string, int64, error) {
	file := filepath.Join(c.Dir, "index", "fedora", repo, rel, arch, "repomd.xml")
	unlock := c.lock("repomd:" + repo + "/" + rel + "/" + arch)
	defer unlock()

	storedSum := ""
	if b, err := os.ReadFile(file); err == nil {
		s := sha256.Sum256(b)
		storedSum = hex.EncodeToString(s[:])
	}
	ml, cached, merr := c.metalink(r.Context(), repo, rel, arch)
	switch {
	case merr != nil && storedSum == "":
		w.Header().Set(cacheHeader, "MISS")
		http.Error(w, "metalink do Fedora indisponível", http.StatusBadGateway)
		return "MISS", 0, merr
	case merr != nil:
		// Sem metalink novo, não há como saber se surgiu um repomd.xml mais novo: serve o
		// guardado (a idade dele aparece no log) em vez de deixar a frota sem metadados.
		res, n, err := c.serveFile(w, r, file, "STALE")
		if err == nil {
			err = fmt.Errorf("metalink indisponível, servido o guardado: %w", merr)
		}
		return res, n, err
	case storedSum == ml.current && cached:
		return c.serveFile(w, r, file, "FRESH")
	case storedSum == ml.current:
		return c.serveFile(w, r, file, "REVALIDATED")
	}

	// O guardado não é o atual: procura o atual nos espelhos (um espelho atrasado ainda
	// pode ter um dos anteriores aceitos, que serve se nenhum tiver o atual).
	var best []byte
	bestSum := ""
	var lastErr error
	for _, m := range ml.mirrors[:min(len(ml.mirrors), mirrorsToTry)] {
		b, err := c.get(r.Context(), m+"repodata/repomd.xml", maxRepomd)
		if err != nil {
			lastErr = err
			continue
		}
		s := sha256.Sum256(b)
		sum := hex.EncodeToString(s[:])
		if sum == ml.current {
			best, bestSum = b, sum
			break
		}
		if ml.allowed[sum] && best == nil {
			best, bestSum = b, sum
		}
		if !ml.allowed[sum] {
			lastErr = fmt.Errorf("%s entregou um repomd.xml fora do metalink (%s...)", hostOf(m), sum[:16])
		}
	}
	switch {
	case best == nil && ml.allowed[storedSum]:
		// Nenhum espelho com o atual, mas o guardado ainda é aceito pelo Fedora.
		res, n, err := c.serveFile(w, r, file, "KEPT")
		if err == nil {
			err = fmt.Errorf("nenhum espelho entregou o repomd.xml atual: %w", lastErr)
		}
		return res, n, err
	case best == nil:
		w.Header().Set(cacheHeader, "MISS")
		http.Error(w, "nenhum espelho entregou um repomd.xml aceito pelo metalink", http.StatusBadGateway)
		return "MISS", 0, lastErr
	case bestSum == storedSum:
		return c.serveFile(w, r, file, "KEPT")
	}
	if err := writeAtomic(file, best); err != nil {
		return "", 0, err
	}
	if !ml.timestamp.IsZero() {
		_ = os.Chtimes(file, ml.timestamp, ml.timestamp)
	}
	return c.serveFile(w, r, file, "MISS")
}

func hostOf(u string) string {
	if pu, err := url.Parse(u); err == nil {
		return pu.Host
	}
	return u
}
