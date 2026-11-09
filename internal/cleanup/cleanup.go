// Package cleanup faz a limpeza de temporários (jobs.TypeCleanup) no agente. Só itens de uma
// lista fechada, só arquivos com mais de MinAge e só dentro das pastas de cada item: o job não
// leva caminho. O agente roda como SYSTEM/root, e um usuário comum pode plantar um link na
// própria pasta de temporários: a varredura não segue links, e no Windows cada arquivo é
// aberto sem seguir pontos de nova análise e só é apagado se o caminho real ainda estiver
// dentro da pasta do item.
package cleanup

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/platform"
)

// MinAge: só sai o arquivo sem mudança há mais que isso (o que está em uso fica).
const MinAge = 48 * time.Hour

// Cleaner limpa os itens pedidos.
type Cleaner struct {
	DataDir string          // a pasta de dados do agente (o item patchd)
	Run     platform.Runner // DISM, systemd-tmpfiles, apt-get, journalctl
	now     func() time.Time
	// free: o espaço livre no disco do sistema (nos testes, um falso). -1: não soube.
	free func() int64
}

// New cria a limpeza.
func New(dataDir string, run platform.Runner) *Cleaner {
	return &Cleaner{DataDir: dataDir, Run: run, now: time.Now, free: systemFree}
}

// itemReport: o que um item fez.
type itemReport struct {
	item    string
	freed   int64
	files   int
	skipped int
	note    string
}

// Clean limpa e devolve o resumo para o detalhe do job. Erro só com o contexto cancelado.
func (c *Cleaner) Clean(ctx context.Context, p jobs.CleanupParams) (string, error) {
	before := c.free()
	var reps []itemReport
	for _, it := range p.Items {
		if ctx.Err() != nil {
			return summary(reps, before, c.free()), ctx.Err()
		}
		reps = append(reps, c.item(ctx, it))
	}
	return summary(reps, before, c.free()), nil
}

// sweep apaga, dentro de root, os arquivos mais velhos que MinAge, sem seguir links nem entrar
// em pastas que são links; depois tira as subpastas que ficaram vazias. del faz a exclusão
// segura do sistema (no Windows, pelo handle, conferindo o caminho real).
func (c *Cleaner) sweep(rep *itemReport, root string, del func(path string) error) {
	cut := c.now().Add(-MinAge)
	var dirs []string // as subpastas velhas (a idade lida antes de apagar o conteúdo, que a renova)
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root {
				return err
			}
			rep.skipped++
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if isLink(path, d) {
			if path == root {
				rep.note = "a pasta é um link: não foi limpa"
				return fs.SkipDir
			}
			rep.skipped++
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if info, err := d.Info(); err == nil && path != root && info.ModTime().Before(cut) {
				dirs = append(dirs, path)
			}
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() || info.ModTime().After(cut) {
			if err != nil || !info.Mode().IsRegular() {
				rep.skipped++
			}
			return nil
		}
		if del(path) != nil {
			rep.skipped++
			return nil
		}
		rep.freed += info.Size()
		rep.files++
		return nil
	})
	// As subpastas vazias, das mais fundas para as de cima (a exclusão só sai se vazia).
	slices.SortFunc(dirs, func(a, b string) int { return len(b) - len(a) })
	for _, d := range dirs {
		_ = del(d) // só sai se ficou vazia
	}
}

func summary(reps []itemReport, before, after int64) string {
	var total int64
	var parts []string
	for _, r := range reps {
		total += r.freed
		s := jobs.CleanupLabel(r.item) + ": "
		switch {
		case r.note != "" && r.files == 0 && r.freed == 0:
			s += r.note
		default:
			s += FormatBytes(r.freed)
			if r.files > 0 {
				s += fmt.Sprintf(" (%d arquivo(s)", r.files)
				if r.skipped > 0 {
					s += fmt.Sprintf(", %d em uso ou pulados", r.skipped)
				}
				s += ")"
			}
			if r.note != "" {
				s += "; " + r.note
			}
		}
		parts = append(parts, s)
	}
	head := "limpeza: " + FormatBytes(total) + " em arquivos apagados"
	if before >= 0 && after >= 0 {
		head = fmt.Sprintf("limpeza: livre no disco do sistema %s → %s (+%s)", FormatBytes(before), FormatBytes(after), FormatBytes(max(after-before, 0)))
	}
	return head + ". " + strings.Join(parts, "; ")
}

// FormatBytes: 1,2 GB, 340 MB...
func FormatBytes(n int64) string {
	const k = 1024
	switch {
	case n >= k*k*k:
		return strings.Replace(fmt.Sprintf("%.1f GB", float64(n)/(k*k*k)), ".", ",", 1)
	case n >= k*k:
		return fmt.Sprintf("%d MB", n/(k*k))
	case n >= k:
		return fmt.Sprintf("%d KB", n/k)
	}
	return fmt.Sprintf("%d B", n)
}

// patchdDirs: as pastas de trabalho do agente que a limpeza pode varrer (instaladores dos jobs,
// as pastas temporárias deles e os bundles de script). Os logs têm a limpeza deles.
func (c *Cleaner) patchdDirs() []string {
	var out []string
	for _, d := range []string{"pacotes", "tmp", "bundles"} {
		p := filepath.Join(c.DataDir, d)
		if info, err := os.Lstat(p); err == nil && info.IsDir() {
			out = append(out, p)
		}
	}
	return out
}
