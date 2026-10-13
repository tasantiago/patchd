package hoststate

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/tasantiago/patchd/internal/protocol"
)

// mountsFile: a tabela de montagens vista pelo próprio processo.
const mountsFile = "/proc/self/mounts"

// disks lê /proc/self/mounts e pergunta o espaço de cada volume local com statfs.
func disks(ctx context.Context) ([]protocol.Disk, []error) {
	f, err := os.Open(mountsFile)
	if err != nil {
		return nil, []error{err}
	}
	defer f.Close()
	mounts, err := parseMounts(f)
	if err != nil {
		return nil, []error{err}
	}
	var out []protocol.Disk
	var errs []error
	for _, m := range mounts {
		if ctx.Err() != nil {
			return out, append(errs, ctx.Err())
		}
		// Um bind mount de arquivo (o /etc/hosts de todo container) não é raiz de volume.
		if fi, err := os.Stat(m.Mount); err != nil || !fi.IsDir() {
			continue
		}
		var st unix.Statfs_t
		if err := unix.Statfs(m.Mount, &st); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", m.Mount, err))
			continue
		}
		// As contagens de blocos vêm na unidade f_frsize (o df usa a mesma regra).
		unit := uint64(st.Frsize)
		if unit == 0 {
			unit = uint64(st.Bsize)
		}
		m.TotalBytes, m.FreeBytes = st.Blocks*unit, st.Bavail*unit
		if m.TotalBytes == 0 {
			continue // sistema de arquivos sem tamanho (não é volume de dados)
		}
		out = append(out, m)
	}
	return dedupe(out), errs
}

// skipFS: tipos que aparecem em /dev/... mas não são volume de dados da máquina: imagens
// somente leitura (os snaps são squashfs em /dev/loopN) e mídia óptica.
var skipFS = map[string]bool{"squashfs": true, "iso9660": true, "udf": true, "erofs": true}

// parseMounts fica só com volumes locais: a origem é um dispositivo de bloco (/dev/...).
// Isso deixa de fora, sem lista de nomes, tudo que é virtual (proc, sysfs, cgroup, tmpfs,
// overlay do Docker) e tudo que é de rede (nfs, cifs): um servidor NFS travado travaria o
// statfs, e o check-in junto.
func parseMounts(r io.Reader) ([]protocol.Disk, error) {
	var out []protocol.Disk
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 3 {
			continue
		}
		dev, mount, fs := unescapeMount(f[0]), unescapeMount(f[1]), f[2]
		if !strings.HasPrefix(dev, "/dev/") || skipFS[fs] {
			continue
		}
		out = append(out, protocol.Disk{Mount: mount, FS: fs, Device: dev})
	}
	return out, sc.Err()
}

// unescapeMount desfaz o escape do kernel: espaço, tab, quebra de linha e barra invertida
// viram \040, \011, \012 e \134.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+4 <= len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
