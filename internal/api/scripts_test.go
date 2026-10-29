package api_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

// zipDeTeste monta um ZIP com os arquivos dados e devolve bytes e SHA-256.
func zipDeTeste(t *testing.T, arquivos map[string]string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for nome, conteudo := range arquivos {
		w, err := zw.Create(nome)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(conteudo)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), hex.EncodeToString(sum[:])
}

func sobeBundle(h http.Handler, cookie, nome, arquivo string, conteudo []byte) *httptest.ResponseRecorder {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if conteudo != nil {
		fw, _ := mw.CreateFormFile("arquivo", arquivo)
		_, _ = fw.Write(conteudo)
	}
	if nome != "" {
		_ = mw.WriteField("nome", nome)
	}
	_ = mw.Close()
	req := httptest.NewRequest("POST", "/painel/scripts", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(&http.Cookie{Name: "patchd_sessao", Value: cookie})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// Aula 23.8: subir o bundle, conferir o ZIP, escolher o ponto de entrada e disparar um job
// por máquina. Dados fictícios.
func TestScriptsBundleEExecucao(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	dir := t.TempDir()
	h, st, _ := ambientePaginas(t, api.WithJobKey(priv), api.WithContent(dir, nil))
	ctx := context.Background()
	semeiaEquipamentos(t, st, 3) // pc-000..pc-002, Windows

	z, sha := zipDeTeste(t, map[string]string{
		"instalar.ps1":  "Write-Host 'ok'\n",
		"config.ini":    "[geral]\nchave=valor\n",
		"bin/apoio.exe": "MZ",
	})

	// Só admin.
	if r := sobeBundle(h, sessaoTeste, "", "x.zip", z); r.Code != http.StatusForbidden {
		t.Fatalf("leitura sobe bundle: %d", r.Code)
	}
	// ZIP sem ponto de entrada é recusado.
	semEntrada, _ := zipDeTeste(t, map[string]string{"leiame.txt": "nada aqui"})
	if l := local(sobeBundle(h, sessaoAdmin, "", "x.zip", semEntrada)); !strings.Contains(l, "erro=zip") {
		t.Errorf("sem ponto de entrada: %s", l)
	}
	// Um ZIP com caminho para fora é recusado antes de tocar no disco.
	zipSlip, _ := zipDeTeste(t, map[string]string{"../fora.ps1": "x"})
	if l := local(sobeBundle(h, sessaoAdmin, "", "x.zip", zipSlip)); !strings.Contains(l, "erro=zip") {
		t.Errorf("zip slip: %s", l)
	}
	// Não é ZIP.
	if l := local(sobeBundle(h, sessaoAdmin, "", "x.zip", []byte("isto não é um zip"))); !strings.Contains(l, "erro=zip") {
		t.Errorf("lixo: %s", l)
	}

	// O bundle bom.
	if l := local(sobeBundle(h, sessaoAdmin, "instala-exemplo", "pacote.zip", z)); !strings.Contains(l, "subiu=") {
		t.Fatalf("subir: %s", l)
	}
	// O mesmo ZIP de novo é recusado.
	if l := local(sobeBundle(h, sessaoAdmin, "outro-nome", "pacote.zip", z)); !strings.Contains(l, "erro=repetido") {
		t.Errorf("repetido: %s", l)
	}
	bundles, _ := st.ScriptBundles(ctx)
	if len(bundles) != 1 || bundles[0].SHA256 != sha || bundles[0].OS != "windows" || len(bundles[0].Files) != 3 {
		t.Fatalf("bundle guardado: %+v", bundles)
	}
	id := bundles[0].ID
	// O ZIP foi para o disco, com o hash como nome.
	if _, err := os.Stat(filepath.Join(dir, "bundles", sha+".zip")); err != nil {
		t.Fatalf("arquivo no servidor: %v", err)
	}

	// A página lista o bundle e, com ?bundle=N, abre o formulário com os pontos de entrada.
	b := pagina(h, "GET", "/painel/scripts", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, "instala-exemplo") || !strings.Contains(b, "Windows") {
		t.Error("lista de bundles")
	}
	b = pagina(h, "GET", "/painel/scripts?bundle="+itoa(id), sessaoAdmin, nil).Body.String()
	for _, quer := range []string{`<option value="instalar.ps1">`, `<option value="bin/apoio.exe">`, `name="maquina"`, "pc-001"} {
		if !strings.Contains(b, quer) {
			t.Errorf("formulário: faltou %q", quer)
		}
	}
	if strings.Contains(b, `<option value="config.ini">`) {
		t.Error("o .ini não devia ser ponto de entrada")
	}

	exec := func(form url.Values) string {
		return local(pagina(h, "POST", "/painel/scripts/"+itoa(id)+"/executar", sessaoAdmin, form))
	}
	// Ponto de entrada fora da lista do bundle é recusado.
	if l := exec(url.Values{"entrada": {"../../etc/passwd"}, "maquina": {"e001-0000"}}); !strings.Contains(l, "erro=entrada") {
		t.Errorf("entrada inválida: %s", l)
	}
	if l := exec(url.Values{"entrada": {"config.ini"}, "maquina": {"e001-0000"}}); !strings.Contains(l, "erro=entrada") {
		t.Errorf("entrada não executável: %s", l)
	}
	// Argumento com aspas ou caminho é recusado.
	for _, arg := range []string{`"x"`, `C:\Windows\x`, "a;rm -rf /"} {
		if l := exec(url.Values{"entrada": {"instalar.ps1"}, "argumentos": {arg}, "maquina": {"e001-0000"}}); !strings.Contains(l, "erro=") {
			t.Errorf("argumento %q devia recusar: %s", arg, l)
		}
	}
	// Sem máquina marcada.
	if l := exec(url.Values{"entrada": {"instalar.ps1"}}); !strings.Contains(l, "erro=nenhuma") {
		t.Errorf("sem máquina: %s", l)
	}

	// Disparo bom: duas máquinas, um job cada.
	l := exec(url.Values{"entrada": {"instalar.ps1"}, "argumentos": {"/quiet MODO=1"}, "prazo": {"300"},
		"maquina": {"e001-0000", "e002-0000", "nao-existe-0000"}})
	if !strings.Contains(l, "criados=2") || !strings.Contains(l, "pulados=1") {
		t.Fatalf("executar: %s", l)
	}
	fila, err := st.DeliverJobs(ctx, "e001-0000", time.Now())
	if err != nil || len(fila) != 1 {
		t.Fatalf("fila: %v %v", fila, err)
	}
	// O job é do tipo script, assinado, e leva o hash do ZIP e o ponto de entrada — nunca
	// uma linha de comando.
	j, err := jobs.Verify(priv.Public().(ed25519.PublicKey), fila[0], "e001-0000", time.Now(), 0)
	if err != nil {
		t.Fatalf("verificar: %v", err)
	}
	if j.Type != jobs.TypeScript {
		t.Fatalf("tipo: %s", j.Type)
	}
	p, err := jobs.ParseScriptParams(j.Params)
	if err != nil {
		t.Fatal(err)
	}
	if p.SHA256 != sha || p.Entry != "instalar.ps1" || p.Runner != "powershell" || p.Timeout != 300 ||
		strings.Join(p.Args, "|") != "/quiet|MODO=1" || p.Bundle != "instala-exemplo" {
		t.Errorf("params: %+v", p)
	}
	var cru map[string]any
	_ = json.Unmarshal(j.Params, &cru)
	if _, tem := cru["comando"]; tem {
		t.Error("o job não deve levar linha de comando")
	}

	// O agente baixa o ZIP pelo hash; outro nome não existe.
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	if _, err := st.CreateEnrollmentToken(ctx, hash, "teste", time.Now().Add(time.Hour), 2); err != nil {
		t.Fatal(err)
	}
	m := registra(t, h, tok)
	req := httptest.NewRequest("GET", protocol.BundlesPath+sha+".zip", nil)
	req.Header.Set("Authorization", "Bearer "+m.Credential)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), z) {
		t.Fatalf("download: %d %d bytes", rec.Code, rec.Body.Len())
	}
	for _, nome := range []string{"../../etc/passwd", "x.zip", strings.Repeat("0", 64) + ".zip"} {
		req := httptest.NewRequest("GET", protocol.BundlesPath+url.PathEscape(nome), nil)
		req.Header.Set("Authorization", "Bearer "+m.Credential)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code == 200 {
			t.Errorf("download de %q devia falhar", nome)
		}
	}
	// Sem credencial de máquina, não baixa.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", protocol.BundlesPath+sha+".zip", nil))
	if rec.Code == 200 {
		t.Error("download sem credencial")
	}

	// Excluir tira do banco e do disco.
	if l := local(pagina(h, "POST", "/painel/scripts/"+itoa(id)+"/excluir", sessaoAdmin, url.Values{})); !strings.Contains(l, "removido=1") {
		t.Errorf("excluir: %s", l)
	}
	if _, err := os.Stat(filepath.Join(dir, "bundles", sha+".zip")); !os.IsNotExist(err) {
		t.Errorf("o ZIP ficou no disco: %v", err)
	}

	// Auditoria: a subida e a execução.
	ev, _ := st.AuditEvents(ctx, store.AuditFilter{Action: "script."})
	var temBundle, temExec bool
	for _, e := range ev {
		switch e.Action {
		case "script.bundle":
			temBundle = true
		case "script.executar":
			temExec = true
		}
	}
	if !temBundle || !temExec {
		t.Errorf("auditoria: %+v", ev)
	}
}
