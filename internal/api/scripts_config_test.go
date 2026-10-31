package api_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/jobs"
)

func TestScriptAvulsoConfigSalvaEDisparo(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	dir := t.TempDir()
	h, st, _ := ambientePaginas(t, api.WithJobKey(priv), api.WithContent(dir, nil))
	semeiaEquipamentos(t, st, 3)
	ctx := context.Background()
	script := []byte("Write-Output $args\r\n")
	args := []string{"-Destino", `C:\Program Files\Empresa\`}
	config := url.Values{"argumentos": {`-Destino "C:\Program Files\Empresa\"`}, "minutos": {"60"}, "interpretador": {"powershell"}}
	if l := local(sobePacote(h, sessaoAdmin, "exemplo", "instalar.ps1", script, config)); !strings.Contains(l, "subiu=") {
		t.Fatalf("script avulso: %s", l)
	}
	list, err := st.ScriptBundles(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("cadastro: %v %v", list, err)
	}
	b := list[0]
	if b.Entry != "instalar.ps1" || b.Timeout != 3600 || !reflect.DeepEqual(b.Args, args) {
		t.Fatalf("configuração perdida: %+v", b)
	}
	// O script avulso chega ao agente pelo mesmo contrato ZIP, com conteúdo preservado.
	z, err := os.ReadFile(filepath.Join(dir, "bundles", b.SHA256+".zip"))
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(z), int64(len(z)))
	if err != nil || len(zr.File) != 1 {
		t.Fatalf("ZIP normalizado: %v", err)
	}
	rc, err := zr.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil || !bytes.Equal(got, script) {
		t.Fatalf("conteúdo alterado: %q %v", got, err)
	}
	base := "/painel/scripts/" + itoa(b.ID)
	page := html.UnescapeString(pagina(h, "GET", "/painel/scripts?bundle="+itoa(b.ID), sessaoAdmin, nil).Body.String())
	for _, want := range []string{`value="instalar.ps1" selected`, `value="60"`, `C:\Program Files\Empresa\`, base + "/configurar"} {
		if !strings.Contains(page, want) {
			t.Errorf("página perdeu %q", want)
		}
	}
	// Só informar os alvos reutiliza o cadastro; não é necessário redigitar a execução.
	if l := local(pagina(h, "POST", base+"/executar", sessaoAdmin, url.Values{"maquina": {"e001-0000"}})); !strings.Contains(l, "criados=1") {
		t.Fatalf("disparo com padrões: %s", l)
	}
	// Editar padrões não muda o job já assinado.
	if r := pagina(h, "POST", base+"/configurar", sessaoTeste, url.Values{}); r.Code != http.StatusForbidden {
		t.Fatalf("leitor alterou padrões: %d", r.Code)
	}
	if l := local(pagina(h, "POST", base+"/configurar", sessaoAdmin,
		url.Values{"entrada": {b.Entry}, "argumentos": {""}, "minutos": {"0.5"}})); !strings.Contains(l, "salvo=1") {
		t.Fatalf("salvar padrões: %s", l)
	}
	if l := local(pagina(h, "POST", base+"/executar", sessaoAdmin, url.Values{"maquina": {"e002-0000"}})); !strings.Contains(l, "criados=1") {
		t.Fatalf("disparo com novos padrões: %s", l)
	}
	for _, tc := range []struct{ machine string; timeout int; args []string }{
		{"e001-0000", 3600, args}, {"e002-0000", 30, nil},
	} {
		queue, err := st.DeliverJobs(ctx, tc.machine, time.Now())
		if err != nil || len(queue) != 1 {
			t.Fatalf("fila %s: %v %v", tc.machine, queue, err)
		}
		job, err := jobs.Verify(priv.Public().(ed25519.PublicKey), queue[0], tc.machine, time.Now(), 0)
		if err != nil {
			t.Fatal(err)
		}
		p, err := jobs.ParseScriptParams(job.Params)
		if err != nil || p.Timeout != tc.timeout || !reflect.DeepEqual(p.Args, tc.args) || p.Entry != b.Entry {
			t.Fatalf("job assinado mudou: %+v %v", p, err)
		}
	}
	// Valores inválidos não sobrescrevem a configuração anterior.
	for _, form := range []url.Values{
		{"entrada": {"fora.ps1"}}, {"minutos": {"61"}}, {"minutos": {"abc"}},
		{"interpretador": {"bash"}}, {"argumentos": {"x\ny"}},
	} {
		if l := local(pagina(h, "POST", base+"/configurar", sessaoAdmin, form)); !strings.Contains(l, "erro=params") {
			t.Errorf("configuração inválida aceita: %v %s", form, l)
		}
	}
	b, _, _ = st.ScriptBundle(ctx, b.ID)
	if b.Timeout != 30 || len(b.Args) != 0 {
		t.Errorf("erro alterou configuração: %+v", b)
	}
}

func TestUploadScriptsFormatosERun(t *testing.T) {
	h, st, _ := ambientePaginas(t, api.WithContent(t.TempDir(), nil))
	for _, name := range []string{"a.vbs", "a.cmd", "a.bat", "a.sh"} {
		if l := local(sobeBundle(h, sessaoAdmin, "", name, []byte("exemplo"))); !strings.Contains(l, "subiu=") {
			t.Errorf("%s: %s", name, l)
		}
	}
	for _, name := range []string{"a.exe", "a.txt", "a.tar.gz"} {
		if l := local(sobeBundle(h, sessaoAdmin, "", name, []byte("exemplo"))); !strings.Contains(l, "erro=script") {
			t.Errorf("formato não suportado: %s %s", name, l)
		}
	}
	z, _ := zipDeTeste(t, map[string]string{"setup.exe": "MZ", "scripts/instalar.cmd": "@echo ok"})
	if l := local(sobePacote(h, sessaoAdmin, "zip-completo", "pacote.zip", z,
		url.Values{"entrada": {"scripts/instalar.cmd"}, "argumentos": {`"C:\Program Files\Empresa" /quiet`}, "minutos": {"20"}})); !strings.Contains(l, "subiu=") {
		t.Fatalf("ZIP com Run: %s", l)
	}
	list, _ := st.ScriptBundles(context.Background())
	if list[0].Entry != "scripts/instalar.cmd" || list[0].Timeout != 1200 || len(list[0].Args) != 2 {
		t.Fatalf("ZIP perdeu execução: %+v", list[0])
	}
	for _, config := range []url.Values{
		{"entrada": {"../x.cmd"}}, {"entrada": {"scripts/instalar.cmd"}, "argumentos": {"x&whoami"}},
	} {
		if l := local(sobePacote(h, sessaoAdmin, "invalido", "pacote.zip", z, config)); !strings.Contains(l, "erro=params") {
			t.Errorf("aceitou Run/argumentos inválidos: %s", l)
		}
	}
}
