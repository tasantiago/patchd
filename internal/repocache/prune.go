package repocache

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Limpeza do cache (Aula 6.6, parte 5). Os arquivos guardados pelo hash (sha256/) e pelo
// caminho (pool/) nunca mudam, mas deixam de ser pedidos: uma versão de pacote substituída,
// as listas de uma publicação antiga. A data de modificação de cada um é o último uso: ela
// é a do download e é renovada num HIT (no máximo uma vez por dia, para não escrever no
// disco a cada pedido). A limpeza apaga o que não é usado há mais de MaxAge e, se o total
// ainda passar de MaxSize, os menos usados primeiro. Os índices (index/) são pequenos e
// precisam da data da origem para o 304 do apt: ficam fora.

const (
	touchEvery = 24 * time.Hour // um HIT renova a data só se ela tiver mais que isso
	tmpMaxAge  = 24 * time.Hour // sobras de downloads interrompidos (o servidor caiu no meio)
)

// formatFile marca o cache cuja data dos arquivos já é o último uso. Até a parte 4c, a data
// era a da origem (anos atrás, nas listas de uma versão do Ubuntu): sem a marca, a primeira
// limpeza apagaria um cache em pleno uso.
const formatFile = ".formato-uso"

// upgradeTimes, uma vez por cache: a data de todo arquivo de sha256/ e pool/ vira agora
// (o último uso de verdade é desconhecido; ganham o prazo inteiro).
func (c *Cache) upgradeTimes() error {
	marker := filepath.Join(c.Dir, formatFile)
	if _, err := os.Stat(marker); err == nil {
		return nil
	}
	now := c.clock()
	for _, area := range []string{"sha256", "pool"} {
		es, err := c.list(area)
		if err != nil {
			return err
		}
		for _, e := range es {
			_ = os.Chtimes(e.path, now, now)
		}
	}
	return os.WriteFile(marker, []byte("a data dos arquivos de sha256/ e pool/ é o último uso (Aula 6.6, parte 5)\n"), 0o640)
}

// touch renova a data de último uso de um arquivo guardado.
func (c *Cache) touch(file string, mod time.Time) {
	now := c.clock()
	if now.Sub(mod) < touchEvery {
		return
	}
	_ = os.Chtimes(file, now, now)
}

// PruneOptions são os limites da limpeza; zero desliga cada um.
type PruneOptions struct {
	MaxAge  time.Duration
	MaxSize int64
	DryRun  bool // só conta o que sairia
}

// Area é o retrato de uma pasta do cache.
type Area struct {
	Files  int64
	Bytes  int64
	Oldest time.Time // último uso mais antigo
}

// PruneResult é o que a limpeza encontrou e apagou.
type PruneResult struct {
	Before       map[string]Area // sha256, pool, index, antes da limpeza
	Removed      int64
	RemovedBytes int64
	ByAge        int64 // removidos por idade
	BySize       int64 // removidos pelo limite de tamanho
	Tmp          int64 // sobras de downloads interrompidos
}

type entry struct {
	path string
	size int64
	mod  time.Time
}

// Status devolve o retrato do cache, sem apagar nada.
func (c *Cache) Status() (map[string]Area, error) {
	out := map[string]Area{}
	for _, area := range []string{"sha256", "pool", "index"} {
		es, err := c.list(area)
		if err != nil {
			return nil, err
		}
		out[area] = summarize(es)
	}
	return out, nil
}

func summarize(es []entry) Area {
	var a Area
	for _, e := range es {
		a.Files++
		a.Bytes += e.size
		if a.Oldest.IsZero() || e.mod.Before(a.Oldest) {
			a.Oldest = e.mod
		}
	}
	return a
}

// list devolve os arquivos de uma pasta do cache (sem os .meta.json e temporários).
func (c *Cache) list(area string) ([]entry, error) {
	var es []entry
	root := filepath.Join(c.Dir, area)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() || strings.HasPrefix(d.Name(), ".tmp-") {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil // apagado no meio da varredura
		}
		es = append(es, entry{path: p, size: fi.Size(), mod: fi.ModTime()})
		return nil
	})
	return es, err
}

// Prune aplica os limites. Pode rodar com o servidor atendendo: um arquivo apagado no
// meio de uma entrega continua legível por quem já o abriu, e o próximo pedido é um MISS.
func (c *Cache) Prune(opts PruneOptions) (PruneResult, error) {
	res := PruneResult{Before: map[string]Area{}}
	now := c.clock()
	var all []entry
	for _, area := range []string{"sha256", "pool", "index"} {
		es, err := c.list(area)
		if err != nil {
			return res, err
		}
		res.Before[area] = summarize(es)
		if area != "index" {
			all = append(all, es...)
		}
	}

	remove := func(e entry) {
		res.Removed++
		res.RemovedBytes += e.size
		if !opts.DryRun {
			_ = os.Remove(e.path)
			c.removeEmptyParents(e.path)
		}
	}
	var keep []entry
	var total int64
	for _, e := range all {
		if opts.MaxAge > 0 && now.Sub(e.mod) > opts.MaxAge {
			res.ByAge++
			remove(e)
			continue
		}
		keep = append(keep, e)
		total += e.size
	}
	if opts.MaxSize > 0 && total > opts.MaxSize {
		// Os menos usados primeiro, até caber.
		slices.SortFunc(keep, func(a, b entry) int { return a.mod.Compare(b.mod) })
		for _, e := range keep {
			if total <= opts.MaxSize {
				break
			}
			res.BySize++
			total -= e.size
			remove(e)
		}
	}

	tmps, _ := os.ReadDir(filepath.Join(c.Dir, "tmp"))
	for _, d := range tmps {
		if fi, err := d.Info(); err == nil && now.Sub(fi.ModTime()) > tmpMaxAge {
			res.Tmp++
			if !opts.DryRun {
				_ = os.Remove(filepath.Join(c.Dir, "tmp", d.Name()))
			}
		}
	}
	return res, nil
}

// removeEmptyParents apaga as pastas que ficaram vazias, até a raiz da área.
func (c *Cache) removeEmptyParents(p string) {
	for dir := filepath.Dir(p); ; dir = filepath.Dir(dir) {
		rel, err := filepath.Rel(c.Dir, dir)
		if err != nil || !strings.Contains(rel, string(filepath.Separator)) {
			return // chegou em sha256/, pool/ ou index/
		}
		if os.Remove(dir) != nil {
			return // não está vazia
		}
	}
}

// ParseSize lê um tamanho como "50G", "500M", "1T" ou bytes; "0" desliga.
func ParseSize(v string) (int64, error) {
	v = strings.TrimSpace(strings.ToUpper(v))
	v = strings.TrimSuffix(strings.TrimSuffix(v, "B"), "I") // 50GiB, 50GB, 50G
	mult := int64(1)
	if n := len(v); n > 0 {
		switch v[n-1] {
		case 'K':
			mult = 1 << 10
		case 'M':
			mult = 1 << 20
		case 'G':
			mult = 1 << 30
		case 'T':
			mult = 1 << 40
		}
		if mult > 1 {
			v = v[:n-1]
		}
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("tamanho inválido %q (use bytes ou K, M, G, T, ex.: 50G)", v)
	}
	return n * mult, nil
}
