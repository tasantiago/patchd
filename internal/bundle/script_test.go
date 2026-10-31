package bundle

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestPackScriptNormalizaEPreservaConteudo(t *testing.T) {
	for _, name := range []string{"instalar.ps1", "instalar.cmd", "instalar.bat", "instalar.vbs", "instalar.sh"} {
		t.Run(name, func(t *testing.T) {
			var out, again bytes.Buffer
			if err := PackScript(&out, name, strings.NewReader("conteudo\r\n")); err != nil {
				t.Fatal(err)
			}
			if err := PackScript(&again, name, strings.NewReader("conteudo\r\n")); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(out.Bytes(), again.Bytes()) {
				t.Fatal("o mesmo script produziu outro ZIP")
			}
			info, err := Inspect(bytes.NewReader(out.Bytes()), int64(out.Len()))
			if err != nil || len(info.Entries) != 1 || info.Entries[0] != name || info.Total != 10 {
				t.Fatalf("pacote: %+v %v", info, err)
			}
		})
	}
}

func TestPackScriptRecusaFormatoECaminho(t *testing.T) {
	for _, name := range []string{"x.exe", "x.txt", "../x.sh", "dir/x.ps1", `C:\x.cmd`} {
		if err := PackScript(io.Discard, name, strings.NewReader("x")); err == nil {
			t.Errorf("aceitou %q", name)
		}
	}
	if err := PackScript(io.Discard, "x.sh", io.LimitReader(zeroReader{}, MaxScriptBytes+1)); err == nil {
		t.Fatal("aceitou script acima do limite")
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}
