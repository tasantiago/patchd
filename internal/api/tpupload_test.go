package api_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/tasantiago/patchd/internal/authsig/authsigtest"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
)

// enviaInstalador: o formulário do instalador próprio, com os campos antes do arquivo.
func enviaInstalador(h http.Handler, cookie, app string, campos url.Values, arquivo string, conteudo []byte) *httptest.ResponseRecorder {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for k, vs := range campos {
		for _, v := range vs {
			_ = mw.WriteField(k, v)
		}
	}
	if conteudo != nil {
		fw, _ := mw.CreateFormFile("arquivo", arquivo)
		_, _ = fw.Write(conteudo)
	}
	_ = mw.Close()
	req := httptest.NewRequest("POST", "/painel/patch/terceiros/enviar?app="+url.QueryEscape(app), &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(&http.Cookie{Name: "patchd_sessao", Value: cookie})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func exeFalso(texto string) []byte { return append([]byte("MZ"), []byte(texto)...) }

func shaDe(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// Instalador próprio (o IRPF do ano): incluir pelo painel, enviar o .exe com a versão
// (aprovando), enviar a atualização substituindo a anterior e ver o atalho da área de
// trabalho de todos os usuários no check-in. Dados fictícios.
func TestInstaladorProprioPeloPainel(t *testing.T) {
	ctx := context.Background()
	base, _ := thirdparty.Default()
	live := thirdparty.NewLive(base)
	dir := t.TempDir()
	h, st, _ := ambientePaginas(t, api.WithThirdPartyManifest(live), api.WithCustomApps(base, nil, nil),
		api.WithCompliance(&complianceFalso{}), api.WithContent(dir, nil))

	// O formulário do instalador próprio.
	b := pagina(h, "GET", "/painel/patch/terceiros/incluir?proprio=1", sessaoAdmin, nil).Body.String()
	for _, quer := range []string{`name="origem" value="proprio"`, `name="atalho_executavel"`, `<option value="exe"`} {
		if !strings.Contains(b, quer) {
			t.Errorf("formulário: falta %q", quer)
		}
	}
	if strings.Contains(b, `name="winget"`) {
		t.Error("o instalador próprio não pede o pacote do winget")
	}
	form := url.Values{"origem": {"proprio"}, "nome": {"IRPF 2026"}, "inventario": {"IRPF2026"}, "modo": {"comeca"}, "tipo": {"exe"},
		"assinatura": {"nao"}, "sem_assinatura": {"instalador de teste"}, "atalho_executavel": {`C:\Arquivos de Programas RFB\IRPF2026\irpf.exe`}}
	ruim := url.Values{}
	for k, v := range form {
		ruim[k] = v
	}
	ruim.Set("atalho_executavel", `C:\Programa\..\cmd.exe`)
	if r := pagina(h, "POST", "/painel/patch/terceiros/incluir", sessaoAdmin, ruim); r.Code != http.StatusUnprocessableEntity {
		t.Errorf("atalho com ..: %d", r.Code)
	}
	r := pagina(h, "POST", "/painel/patch/terceiros/incluir", sessaoAdmin, form)
	if r.Code != http.StatusSeeOther || !strings.Contains(local(r), "salvo=irpf-2026") || !strings.Contains(local(r), "proprio=1") {
		t.Fatalf("salvar: %d %s %s", r.Code, local(r), r.Body)
	}
	var rule thirdparty.App
	for _, a := range live.For("windows") {
		if a.ID == "irpf-2026" {
			rule = a
		}
	}
	if !rule.Uploaded() || rule.Shortcut == nil || rule.Shortcut.Name != "IRPF 2026" || !rule.Matches("IRPF2026 - Declaração", "") {
		t.Fatalf("regra: %+v", rule)
	}
	// A edição mantém o id e não vira programa do winget.
	form.Set("editar", "irpf-2026")
	form.Set("nome", "Imposto de Renda 2026")
	form.Set("atalho_nome", "IRPF")
	if r := pagina(h, "POST", "/painel/patch/terceiros/incluir", sessaoAdmin, form); r.Code != http.StatusSeeOther {
		t.Fatalf("editar: %d %s", r.Code, r.Body)
	}

	// A página do programa: o envio, sem o Baixar do winget.
	prog := "/painel/patch/programa?app=irpf-2026"
	b = pagina(h, "GET", prog, sessaoAdmin, nil).Body.String()
	for _, quer := range []string{"instalador próprio, enviado pelo painel", `action="/painel/patch/terceiros/enviar?app=irpf-2026"`,
		`enctype="multipart/form-data"`, "IRPF → C:\\Arquivos de Programas RFB\\IRPF2026\\irpf.exe"} {
		if !strings.Contains(b, quer) {
			t.Errorf("página do programa: falta %q", quer)
		}
	}
	if strings.Contains(b, `action="/painel/patch/terceiros/baixar"`) {
		t.Error("o instalador próprio não baixa do winget")
	}

	// Recusas: o perfil leitura; exe sem argumentos; um arquivo que não é exe; versão inválida.
	campos := url.Values{"versao": {"1.0"}, "arquitetura": {"x64"}, "aprovar": {"1"}, "substituir": {"1"}}
	if r := enviaInstalador(h, sessaoTeste, "irpf-2026", campos, "IRPF2026.exe", exeFalso("v1")); r.Code != http.StatusForbidden {
		t.Errorf("leitura: %d", r.Code)
	}
	for nome, c := range map[string]struct {
		campos url.Values
		dado   []byte
		quer   string
	}{
		"sem argumentos": {campos, exeFalso("v1"), "argumentos padrão"},
		"não é exe":      {url.Values{"versao": {"1.0"}, "arquitetura": {"x64"}, "argumentos": {"-q"}}, []byte("PK\x03\x04zip"), "não é um executável"},
		"versão":         {url.Values{"versao": {"nova"}, "arquitetura": {"x64"}, "argumentos": {"-q"}}, exeFalso("v1"), "comece por um número"},
		"sem arquivo":    {url.Values{"versao": {"1.0"}, "arquitetura": {"x64"}, "argumentos": {"-q"}}, nil, "escolha o arquivo"},
	} {
		r := enviaInstalador(h, sessaoAdmin, "irpf-2026", c.campos, "IRPF2026.exe", c.dado)
		loc, _ := url.Parse(local(r))
		if r.Code != http.StatusSeeOther || loc.Query().Get("erro") != "envio" || !strings.Contains(loc.Query().Get("motivo"), c.quer) {
			t.Errorf("%s: %d %s", nome, r.Code, local(r))
		}
	}
	if pk, _ := st.ThirdPartyPackages(ctx); len(pk) != 0 {
		t.Fatalf("recusas guardaram: %+v", pk)
	}

	// A 1.0, aprovada.
	v1 := exeFalso("versão 1.0")
	campos.Set("argumentos", "-q")
	r = enviaInstalador(h, sessaoAdmin, "irpf-2026", campos, `C:\Downloads\IRPF2026Win64v1.0.exe`, v1)
	if r.Code != http.StatusSeeOther || !strings.Contains(local(r), "enviado=1.0") || !strings.Contains(local(r), "aprovada=1") {
		t.Fatalf("envio 1.0: %d %s", r.Code, local(r))
	}
	pkgs, _ := st.ThirdPartyPackages(ctx)
	if len(pkgs) != 1 || pkgs[0].SHA256 != shaDe(v1) || pkgs[0].Arguments != "-q" || pkgs[0].Type != "exe" || pkgs[0].URL != "upload:IRPF2026Win64v1.0.exe" {
		t.Fatalf("instalador guardado: %+v", pkgs)
	}
	if _, err := os.Stat(filepath.Join(dir, "pacotes", shaDe(v1)+".exe")); err != nil {
		t.Fatalf("arquivo no cache: %v", err)
	}
	refs, _ := st.ThirdPartyVersions(ctx)
	if len(refs) != 1 || refs[0].App != "irpf-2026" || refs[0].Version != "1.0" || refs[0].Source != "upload:irpf-2026" {
		t.Errorf("referência: %+v", refs)
	}
	if b := pagina(h, "GET", prog+"&enviado=1.0&aprovada=1", sessaoAdmin, nil).Body.String(); !strings.Contains(b, "A versão está aprovada") ||
		!strings.Contains(b, "argumentos <code>-q</code>") || !strings.Contains(b, `name="argumentos" value="-q"`) {
		t.Error("página depois do envio")
	}

	// A 1.1 substitui: a 1.0 perde a aprovação e o instalador.
	v2 := exeFalso("versão 1.1")
	campos.Set("versao", "1.1")
	r = enviaInstalador(h, sessaoAdmin, "irpf-2026", campos, "IRPF2026Win64v1.1.exe", v2)
	if r.Code != http.StatusSeeOther || !strings.Contains(local(r), "substituidas=1.0") {
		t.Fatalf("envio 1.1: %d %s", r.Code, local(r))
	}
	pkgs, _ = st.ThirdPartyPackages(ctx)
	if len(pkgs) != 1 || pkgs[0].Version != "1.1" {
		t.Fatalf("depois da substituição: %+v", pkgs)
	}
	if _, err := os.Stat(filepath.Join(dir, "pacotes", shaDe(v1)+".exe")); !os.IsNotExist(err) {
		t.Errorf("o instalador da 1.0 ficou: %v", err)
	}
	aps, _ := st.ThirdPartyApprovals(ctx)
	if len(aps) != 1 || aps[0].Version != "1.1" {
		t.Errorf("aprovações: %+v", aps)
	}
	if refs, _ := st.ThirdPartyVersions(ctx); len(refs) != 1 || refs[0].Version != "1.1" {
		t.Errorf("referência depois: %+v", refs)
	}
	// A mesma versão com outro arquivo é recusada.
	r = enviaInstalador(h, sessaoAdmin, "irpf-2026", campos, "outro.exe", exeFalso("outro"))
	if loc, _ := url.Parse(local(r)); !strings.Contains(loc.Query().Get("motivo"), "já foi enviada com outro arquivo") {
		t.Errorf("mesma versão, outro arquivo: %s", local(r))
	}

	// O check-in leva o atalho.
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	if _, err := st.CreateEnrollmentToken(ctx, hash, "teste", time.Now().Add(time.Hour), 5); err != nil {
		t.Fatal(err)
	}
	ds := checkin(t, h, registra(t, h, tok).Credential).DesktopShortcuts
	if ds == nil || len(ds.Items) != 1 || ds.Items[0].App != "irpf-2026" || ds.Items[0].Name != "IRPF" ||
		ds.Items[0].Target != `C:\Arquivos de Programas RFB\IRPF2026\irpf.exe` {
		t.Fatalf("atalhos no check-in: %+v", ds)
	}

	// Auditoria.
	ev, _ := st.AuditEvents(ctx, store.AuditFilter{Limit: 50})
	acoes := map[string]int{}
	for _, e := range ev {
		acoes[e.Action]++
	}
	for _, a := range []string{"terceiros.incluir", "terceiros.enviar", "terceiros.aprovar", "terceiros.revogar", "terceiros.excluir"} {
		if acoes[a] == 0 {
			t.Errorf("auditoria sem %s: %v", a, acoes)
		}
	}
}

// O instalador enviado diz quem o assinou: a página mostra, sugere exigir a assinatura do
// arquivo (o programa estava "sem assinatura") e, com o botão, a regra passa a exigi-la.
// Com a assinatura trocada, avisa que o agente vai recusar.
func TestAssinaturaDoInstaladorEnviado(t *testing.T) {
	ctx := context.Background()
	base, _ := thirdparty.Default()
	live := thirdparty.NewLive(base)
	h, st, _ := ambientePaginas(t, api.WithThirdPartyManifest(live), api.WithCustomApps(base, nil, nil),
		api.WithCompliance(&complianceFalso{}), api.WithContent(t.TempDir(), nil))
	form := url.Values{"origem": {"proprio"}, "nome": {"Assistente Teste"}, "inventario": {"Assistente Teste"}, "modo": {"comeca"}, "tipo": {"nullsoft"},
		"assinatura": {"nao"}, "sem_assinatura": {"a conferir"}}
	if r := pagina(h, "POST", "/painel/patch/terceiros/incluir", sessaoAdmin, form); r.Code != http.StatusSeeOther {
		t.Fatalf("incluir: %d %s", r.Code, r.Body)
	}
	exe := authsigtest.Exe("Fabricante Teste Ltda")
	campos := url.Values{"versao": {"2.4.4"}, "arquitetura": {"x64"}, "aprovar": {"1"}}
	if r := enviaInstalador(h, sessaoAdmin, "assistente-teste", campos, "assistente.exe", exe); r.Code != http.StatusSeeOther || strings.Contains(local(r), "erro=") {
		t.Fatalf("enviar: %d %s", r.Code, local(r))
	}
	prog := "/painel/patch/programa?app=assistente-teste"
	b := pagina(h, "GET", prog, sessaoAdmin, nil).Body.String()
	for _, quer := range []string{"assinado por Fabricante Teste Ltda", "O instalador da 2.4.4 é assinado por Fabricante Teste Ltda</strong>",
		`<input type="hidden" name="sha" value="` + shaDe(exe) + `"><button type="submit">Exigir a assinatura de Fabricante Teste Ltda</button>`} {
		if !strings.Contains(b, quer) {
			t.Errorf("página: falta %q", quer)
		}
	}
	if strings.Contains(pagina(h, "GET", prog, sessaoTeste, nil).Body.String(), "Exigir a assinatura de") {
		t.Error("leitura vê o botão")
	}
	// SHA-256 que não é de um instalador do programa: nada muda.
	if l := local(pagina(h, "POST", "/painel/patch/terceiros/assinante", sessaoAdmin, url.Values{"app": {"assistente-teste"}, "sha": {strings.Repeat("0", 64)}})); !strings.Contains(l, "erro=assinante-arquivo") {
		t.Errorf("sha forjado: %s", l)
	}
	if l := local(pagina(h, "POST", "/painel/patch/terceiros/assinante", sessaoAdmin, url.Values{"app": {"assistente-teste"}, "sha": {shaDe(exe)}})); !strings.HasSuffix(l, "&assinante=1") {
		t.Fatalf("adotar: %s", l)
	}
	var rule thirdparty.App
	for _, a := range live.For("windows") {
		if a.ID == "assistente-teste" {
			rule = a
		}
	}
	if rule.Install == nil || rule.Install.Signer != "Fabricante Teste Ltda" || rule.Install.UnsignedReason != "" || !rule.Uploaded() {
		t.Fatalf("regra: %+v", rule.Install)
	}
	if ev, _ := st.AuditEvents(ctx, store.AuditFilter{Action: "terceiros.editar"}); len(ev) == 0 || !strings.Contains(ev[0].Detail, "assinatura Fabricante Teste Ltda (do instalador 2.4.4 nullsoft") {
		t.Errorf("auditoria: %+v", ev)
	}
	b = pagina(h, "GET", prog+"&assinante=1", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, "Assinatura atualizada") || strings.Contains(b, "Exigir a assinatura de") {
		t.Error("depois de adotar")
	}
	// Programa exigindo outra organização: o aviso de que o agente vai recusar.
	form.Set("editar", "assistente-teste")
	form.Set("assinatura", "sim")
	form.Set("assinante", "Outra Empresa")
	if r := pagina(h, "POST", "/painel/patch/terceiros/incluir", sessaoAdmin, form); r.Code != http.StatusSeeOther {
		t.Fatalf("editar: %d %s", r.Code, r.Body)
	}
	b = pagina(h, "GET", prog, sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, "O agente vai recusar o instalador da 2.4.4 (nullsoft).</strong> O programa exige a assinatura de <b>Outra Empresa</b>, e o arquivo é assinado por <b>Fabricante Teste Ltda</b>") ||
		!strings.Contains(b, "Exigir a assinatura de Fabricante Teste Ltda") {
		t.Errorf("divergência:\n%s", b)
	}
}
