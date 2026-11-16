package macinstall

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
)

// Um Installomator falso, em sh: escreve as variáveis que recebeu e sai com o código que o
// rótulo pede (rotulo-12 sai com 12).
const falso = `#!/bin/sh
echo "2026-11-16 10:00:00 : INFO  : $1 : recebido: $*"
case "$1" in
  rotulo-*) code="${1#rotulo-}" ;;
  *) code=0 ;;
esac
echo "2026-11-16 10:00:01 : REQ   : $1 : fim com $code"
exit "$code"
`

func montar(t *testing.T) (*Executor, jobs.ThirdPartyParams, *int) {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("sem /bin/sh")
	}
	soma := sha256.Sum256([]byte(falso))
	p := jobs.ThirdPartyParams{App: "google-chrome", Version: "155.0.8059.40", SHA256: hex.EncodeToString(soma[:]), Size: int64(len(falso)),
		Installer: jobs.InstallerInstallomator, Args: []string{"googlechromepkg"}}
	baixados := 0
	dir := t.TempDir()
	e := &Executor{
		Download: func(_ context.Context, path string, w io.Writer, limit int64) (int64, error) {
			baixados++
			if path != protocol.PackagesPath+p.SHA256+".sh" || limit != p.Size {
				return 0, errors.New("caminho ou limite errado: " + path)
			}
			n, err := io.Copy(w, strings.NewReader(falso))
			return n, err
		},
		Dir: filepath.Join(dir, "installomator"), LogDir: filepath.Join(dir, "logs"), Shell: "/bin/sh",
	}
	return e, p, &baixados
}

func TestInstalarPeloInstallomator(t *testing.T) {
	e, p, baixados := montar(t)
	ctx := context.Background()
	detail, err := e.Install(ctx, 7, p)
	if err != nil || detail != "Installomator googlechromepkg: fim com 0" {
		t.Fatalf("instalar: %q %v", detail, err)
	}
	log, err := os.ReadFile(filepath.Join(e.LogDir, "installomator-job-7.log"))
	if err != nil || !bytes.Contains(log, []byte("googlechromepkg DEBUG=0 NOTIFY=silent BLOCKING_PROCESS_ACTION=silent_fail REOPEN=no INTERRUPT_DND=no")) {
		t.Errorf("log: %s %v", log, err)
	}
	// O script fica, conferido, só para root: a segunda vez não baixa de novo.
	if st, err := os.Stat(filepath.Join(e.Dir, p.SHA256+".sh")); err != nil || st.Mode().Perm() != 0o400 {
		t.Errorf("script guardado: %v %v", st, err)
	}
	if _, err := e.Install(ctx, 8, p); err != nil || *baixados != 1 {
		t.Errorf("segunda vez: %v, %d downloads", err, *baixados)
	}

	// Os códigos de saída do Installomator viram as convenções do agente.
	for code, want := range map[string]error{"1": jobs.ErrRefused, "12": jobs.ErrProgramOpen, "24": jobs.ErrRetry, "2": jobs.ErrNotStarted, "23": jobs.ErrRefused} {
		q := p
		q.Args = []string{"rotulo-" + code}
		_, err := e.Install(ctx, 9, q)
		if !errors.Is(err, want) {
			t.Errorf("código %s: %v (queria %v)", code, err, want)
		}
	}
	q := p
	q.Args = []string{"rotulo-5"}
	if _, err := e.Install(ctx, 9, q); err == nil || errors.Is(err, jobs.ErrRetry) || !strings.Contains(err.Error(), "Team ID") {
		t.Errorf("Team ID: %v", err)
	}
	if _, err := e.Install(ctx, 9, func() jobs.ThirdPartyParams { q.Args = []string{"rotulo-99"}; return q }()); err == nil || !strings.Contains(err.Error(), "código 99") {
		t.Errorf("código desconhecido: %v", err)
	}
}

func TestRecusasAntesDeRodar(t *testing.T) {
	e, p, _ := montar(t)
	ctx := context.Background()
	// Instalador do Windows num Mac.
	w := p
	w.Installer, w.Args = "msi", []string{"/qn"}
	if _, err := e.Install(ctx, 1, w); !errors.Is(err, jobs.ErrRefused) {
		t.Errorf("msi no Mac: %v", err)
	}
	// O servidor entregou outro conteúdo: recusa, e nada fica na pasta.
	e.Download = func(_ context.Context, _ string, w io.Writer, _ int64) (int64, error) {
		n, err := io.Copy(w, strings.NewReader(strings.Repeat("x", len(falso))))
		return n, err
	}
	if _, err := e.Install(ctx, 2, p); !errors.Is(err, jobs.ErrRefused) || !strings.Contains(err.Error(), "não é o do job") {
		t.Errorf("conteúdo trocado: %v", err)
	}
	if es, _ := os.ReadDir(e.Dir); len(es) != 0 {
		t.Errorf("sobrou na pasta: %v", es)
	}
	// O download falhou: nada rodou.
	e.Download = func(context.Context, string, io.Writer, int64) (int64, error) { return 0, errors.New("HTTP 404") }
	if _, err := e.Install(ctx, 3, p); !errors.Is(err, jobs.ErrNotStarted) {
		t.Errorf("sem download: %v", err)
	}
	// Rótulo fora da forma não chega a rodar.
	r := p
	r.Args = []string{"x; rm -rf /"}
	if _, err := e.Install(ctx, 4, r); !errors.Is(err, jobs.ErrRefused) {
		t.Errorf("rótulo: %v", err)
	}
}

func TestUltimaLinha(t *testing.T) {
	out := []byte("2026-11-16 10:00:00 : INFO  : x : baixando\n2026-11-16 10:00:05 : REQ   : x : All done!\n\n")
	if l := lastLine(out); l != "All done!" {
		t.Errorf("%q", l)
	}
	if l := lastLine([]byte("sem carimbo")); l != "sem carimbo" {
		t.Errorf("%q", l)
	}
	var c capped
	c.Write(bytes.Repeat([]byte("a"), maxOutput))
	c.Write([]byte("fim"))
	if c.Len() != maxOutput || !bytes.HasSuffix(c.Bytes(), []byte("fim")) {
		t.Errorf("corte: %d", c.Len())
	}
}
