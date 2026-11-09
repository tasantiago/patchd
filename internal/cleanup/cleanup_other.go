//go:build !windows

package cleanup

import (
	"context"
	"io/fs"
	"os"
	"runtime"
	"time"

	"golang.org/x/sys/unix"

	"github.com/tasantiago/patchd/internal/jobs"
)

func systemFree() int64 {
	var st unix.Statfs_t
	if unix.Statfs("/", &st) != nil {
		return -1
	}
	return int64(st.Bavail) * int64(st.Bsize) //nolint:unconvert // Bsize muda de tipo entre sistemas
}

// isLink: no Unix, só o link simbólico (a varredura não entra nem apaga).
func isLink(_ string, d fs.DirEntry) bool { return d.Type()&(fs.ModeSymlink|fs.ModeIrregular) != 0 }

// firstExisting: o primeiro caminho que existe (o binário pode estar em /usr/bin ou /bin).
func firstExisting(paths ...string) string {
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func (c *Cleaner) runTool(ctx context.Context, rep *itemReport, timeout time.Duration, name string, args ...string) {
	before := c.free()
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if _, err := c.Run.Run(rctx, name, args...); err != nil {
		rep.note = err.Error()
	}
	if after := c.free(); before >= 0 && after > before {
		rep.freed = after - before
	}
}

func (c *Cleaner) item(ctx context.Context, item string) itemReport {
	rep := itemReport{item: item}
	linux := runtime.GOOS == "linux"
	switch {
	case item == jobs.CleanupPatchd:
		// A pasta de dados é só do root: nenhum usuário planta link nela.
		for _, d := range c.patchdDirs() {
			c.sweep(&rep, d, os.Remove)
		}
	case !linux:
		rep.note = "não se aplica neste sistema"
	case item == jobs.CleanupSystemTemp:
		// O systemd-tmpfiles limpa /tmp e /var/tmp pela idade, com a regra do próprio sistema,
		// e é feito para rodar como root sem seguir links.
		if t := firstExisting("/usr/bin/systemd-tmpfiles", "/bin/systemd-tmpfiles"); t != "" {
			c.runTool(ctx, &rep, 10*time.Minute, t, "--clean")
		} else {
			rep.note = "sem o systemd-tmpfiles"
		}
	case item == jobs.CleanupUserTemp:
		rep.note = "não se aplica no Linux (os temporários são o /tmp, no item do sistema)"
	case item == jobs.CleanupUpdateCache:
		switch {
		case firstExisting("/usr/bin/apt-get") != "":
			c.runTool(ctx, &rep, 10*time.Minute, "/usr/bin/apt-get", "clean")
		case firstExisting("/usr/bin/dnf") != "":
			c.runTool(ctx, &rep, 10*time.Minute, "/usr/bin/dnf", "clean", "packages")
		default:
			rep.note = "sem apt nem dnf"
		}
	case item == jobs.CleanupComponents:
		if j := firstExisting("/usr/bin/journalctl", "/bin/journalctl"); j != "" {
			c.runTool(ctx, &rep, 10*time.Minute, j, "--vacuum-size=200M")
		} else {
			rep.note = "sem o journalctl"
		}
	}
	return rep
}
