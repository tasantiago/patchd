package tpupload

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
	"github.com/tasantiago/patchd/internal/tpinstall"
)

func regra(t *testing.T, tipo string) thirdparty.App {
	t.Helper()
	a, err := thirdparty.BuildCustom(thirdparty.CustomInput{Name: "Sistema Interno", InventoryName: "Sistema Interno", InstallType: tipo,
		Signer: "Empresa Exemplo", Upload: true})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

var msi = append([]byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}, []byte("conteúdo do msi")...)

// O envio guarda no cache pelo SHA-256, registra com a origem upload e os argumentos padrão do
// tipo, confere o tipo pelo conteúdo e grava a referência; o job sai do mesmo jeito que o dos
// instaladores do winget.
func TestEnvioDoInstaladorProprio(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	dir := filepath.Join(t.TempDir(), "pacotes")
	a := regra(t, "msi")

	var pr *Problem
	if _, err := Save(ctx, st, dir, a, Input{Version: "2.0", Arch: "x64"}, bytes.NewReader([]byte("MZ não é msi"))); !errors.As(err, &pr) {
		t.Fatalf("exe no lugar do msi: %v", err)
	}
	if _, err := Save(ctx, st, dir, a, Input{Version: "2.0", Arch: "x64"}, bytes.NewReader(nil)); !errors.As(err, &pr) {
		t.Fatalf("vazio: %v", err)
	}
	for _, in := range []Input{{Version: "2.0", Arch: "ia64"}, {Version: "dois", Arch: "x64"}, {Version: "2.0", Arch: "x64", Args: "/i C:\\outro.msi"},
		{Version: "2.0", Arch: "x64", Args: "TRANSFORMS=x.mst"}} {
		if err := Check(a, in); !errors.As(err, &pr) {
			t.Errorf("%+v aceito: %v", in, err)
		}
	}
	winget := a
	winget.Install = &thirdparty.Install{Winget: "A.B", Type: "msi"}
	if err := Check(winget, Input{Version: "1", Arch: "x64"}); !errors.Is(err, ErrNotUpload) {
		t.Errorf("programa do winget: %v", err)
	}

	pk, err := Save(ctx, st, dir, a, Input{Version: "2.0", Arch: "x64", FileName: `C:\x\Sistema Interno <2.0>.msi`}, bytes.NewReader(msi))
	if err != nil {
		t.Fatal(err)
	}
	if pk.Arguments != "/quiet /norestart" || pk.URL != "upload:Sistema Interno _2.0_.msi" || pk.WingetID != "" || !pk.Uploaded() || pk.Size != int64(len(msi)) {
		t.Errorf("registro: %+v", pk)
	}
	if got, err := os.ReadFile(filepath.Join(dir, pk.SHA256+".msi")); err != nil || !bytes.Equal(got, msi) {
		t.Fatalf("arquivo no cache: %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".enviando-*")); len(left) != 0 {
		t.Errorf("temporários ficaram: %v", left)
	}
	refs, _ := st.ThirdPartyVersions(ctx)
	if len(refs) != 1 || refs[0].Version != "2.0" {
		t.Errorf("referência: %+v", refs)
	}
	// Uma versão mais antiga não baixa a referência.
	if _, err := Save(ctx, st, dir, a, Input{Version: "1.9", Arch: "x64"}, bytes.NewReader(append(msi, '1'))); err != nil {
		t.Fatal(err)
	}
	if refs, _ := st.ThirdPartyVersions(ctx); refs[0].Version != "2.0" {
		t.Errorf("referência caiu para %s", refs[0].Version)
	}
	// O mesmo arquivo de novo: nada muda; outro arquivo na mesma versão: recusado.
	if _, err := Save(ctx, st, dir, a, Input{Version: "2.0", Arch: "x64"}, bytes.NewReader(msi)); err != nil {
		t.Errorf("reenvio do mesmo arquivo: %v", err)
	}
	if _, err := Save(ctx, st, dir, a, Input{Version: "2.0", Arch: "x64"}, bytes.NewReader(append(msi, '2'))); !errors.As(err, &pr) {
		t.Errorf("outro arquivo na mesma versão: %v", err)
	}

	// O job: instalação nova numa máquina x64, pelo mesmo caminho do winget.
	pkgs, _ := st.ThirdPartyPackages(ctx)
	var v2 []store.ThirdPartyPackage
	for _, p := range pkgs {
		if p.Version == "2.0" {
			v2 = append(v2, p)
		}
	}
	d := tpinstall.Decision{App: a, State: tpinstall.StateUpdate, Package: tpinstall.ForNewInstall(a, v2, "x64")}
	if d.Package == nil {
		t.Fatal("sem instalador para a instalação nova")
	}
	p, err := tpinstall.JobParams(d)
	if err != nil || p.Installer != "msi" || p.SHA256 != pk.SHA256 || p.Signer != "Empresa Exemplo" || len(p.Args) != 2 {
		t.Errorf("job: %+v %v", p, err)
	}

	// O exe genérico exige os argumentos.
	exe := regra(t, "exe")
	if err := Check(exe, Input{Version: "1.0", Arch: "x64"}); !errors.As(err, &pr) {
		t.Errorf("exe sem argumentos: %v", err)
	}
	if err := Check(exe, Input{Version: "1.0", Arch: "neutral", Args: "  -q   -overwrite "}); err != nil {
		t.Errorf("exe com argumentos: %v", err)
	}
	if args, _ := Args("exe", "  -q   -overwrite "); args != "-q -overwrite" {
		t.Errorf("args: %q", args)
	}
}
