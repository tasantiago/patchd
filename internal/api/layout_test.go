package api_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/campaign"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
)

// Aula 19.1: o layout do tribunal. Barra lateral com os grupos (Administração só para admin),
// recolher sem JavaScript, versões na barra superior.
func TestLayoutDoTribunal(t *testing.T) {
	h, _, _ := ambientePaginas(t, api.WithSystemInfo(api.SystemInfo{Version: "v0.19.1"}))
	adm := pagina(h, "GET", "/painel/", sessaoAdmin, nil).Body.String()
	for _, tr := range []string{
		`<input type="checkbox" id="menu" class="menu-chk"`, `<label for="menu" class="hamb"`, `<aside class="lado"`,
		`<p class="grupo"><span class="rot">Navegação</span></p>`, `<p class="grupo"><span class="rot">Patch</span></p>`,
		`<p class="grupo"><span class="rot">Automação</span></p>`, `<p class="grupo"><span class="rot">Administração</span></p>`,
		`<a href="/painel/configuracao?aba=auditoria" title="Auditoria"`, `<a href="/painel/autoatendimento" title="Autoatendimento"`,
		`<span class="avatar" aria-hidden="true">T</span>`, `<small>Administrador · AD</small>`,
		`<span class="secao">Navegação</span>`, `<span class="titulo">Frota</span>`, `Servidor <b>v0.19.1</b>`,
		`<details class="conta">`, `<form method="post" action="/painel/sair">`, `<span class="escudo">`,
	} {
		if !strings.Contains(adm, tr) {
			t.Errorf("admin: faltou %q", tr)
		}
	}
	for _, proibido := range []string{"<script", "🖥", "🚀", `class="cont"`} {
		if strings.Contains(adm, proibido) {
			t.Errorf("admin: não deveria ter %q", proibido)
		}
	}
	leitura := pagina(h, "GET", "/painel/", sessaoTeste, nil).Body.String()
	if strings.Contains(leitura, "Administração") || strings.Contains(leitura, `href="/painel/configuracao`) ||
		!strings.Contains(leitura, `<small>Leitura · local</small>`) {
		t.Error("leitura: sem o grupo Administração, com o perfil")
	}
	// O item de cada página.
	for path, item := range map[string]string{
		"/painel/autoatendimento":            `<a href="/painel/autoatendimento" title="Autoatendimento" class="ativo"`,
		"/painel/configuracao?aba=auditoria": `<a href="/painel/configuracao?aba=auditoria" title="Auditoria" class="ativo"`,
		"/painel/configuracao?aba=acesso":    `<a href="/painel/configuracao" title="Configuração" class="ativo"`,
		"/painel/patch?aba=terceiros":        `<a href="/painel/patch?aba=terceiros" title="Patch › Programas de terceiros" class="ativo"`,
		"/painel/aposentadas":                `<a href="/painel/aposentadas" title="Aposentadas" class="ativo"`,
		"/painel/maquinas/60ef97e4-0001":     `<a href="/painel/inventario" title="Inventário" class="ativo"`,
	} {
		b := pagina(h, "GET", path, sessaoAdmin, nil).Body.String()
		if !strings.Contains(b, item) || strings.Count(b, `class="ativo"`) != 1 {
			t.Errorf("%s: item do menu", path)
		}
	}
}

// Os contadores: pedidos esperando a TI, campanhas pausadas, máquinas com a fonte falhando
// (sem o catálogo offline). Decidir um pedido baixa o número na volta.
func TestContadoresDoMenu(t *testing.T) {
	h, st, _ := ambientePaginas(t)
	ctx := context.Background()
	for _, conta := range []string{"a", "b"} {
		if _, err := st.CreateSelfServiceRequest(ctx, store.SelfServiceRequest{MachineID: "60ef97e4-0001", Account: conta, AppID: "7zip", Action: "instalar", Version: "24.08"}); err != nil {
			t.Fatal(err)
		}
	}
	id, _ := st.CreateCampaign(ctx, campaign.Campaign{Name: "busca", Type: jobs.TypeRescan, Rings: []int{0}, TTL: time.Hour, CreatedBy: "thyago"})
	c, _, _ := st.Campaign(ctx, id)
	from := c.State
	c.State, c.Detail = campaign.StatePaused, "anel 0: 1 job(s) sem sucesso"
	if ok, err := st.SaveCampaign(ctx, c, from); !ok || err != nil {
		t.Fatal(ok, err)
	}
	for _, m := range []string{"60ef97e4-0001", "7aa604f7-0002"} {
		_ = st.SaveScan(ctx, m, protocol.PatchScanReport{SchemaVersion: protocol.PatchSchemaVersion, ScannedAt: time.Now().UTC(),
			Scans: []protocol.ScanResult{{Source: "wua-default", Missing: []protocol.MissingUpdate{}}, {Source: "wua-offline", Error: "sem catálogo"}}})
	}
	_ = st.SaveScan(ctx, "60ef97e4-0001", protocol.PatchScanReport{SchemaVersion: protocol.PatchSchemaVersion, ScannedAt: time.Now().UTC(),
		Scans: []protocol.ScanResult{{Source: "wua-default", Error: "HRESULT 0x80244007"}}})

	b := pagina(h, "GET", "/painel/", sessaoTeste, nil).Body.String()
	for _, tr := range []string{
		`<span class="rot">Autoatendimento</span><span class="cont" title="pedidos aguardando a TI">2</span>`,
		`<span class="rot">Campanhas</span><span class="cont" title="campanhas pausadas">1</span>`,
		`<span class="rot">Visão geral</span><span class="cont" title="máquinas com a fonte de busca falhando">1</span>`,
	} {
		if !strings.Contains(b, tr) {
			t.Errorf("faltou %q", tr)
		}
	}
	l, _ := st.SelfServiceRequests(ctx, store.RequestFilter{Waiting: true})
	r := pagina(h, "POST", "/painel/autoatendimento/pedidos/"+strconv.FormatInt(l[0].ID, 10)+"/recusar", sessaoAdmin, url.Values{"motivo": {"não precisa"}})
	if r.Code != http.StatusSeeOther {
		t.Fatalf("recusar: %d %s", r.Code, r.Body)
	}
	if b := pagina(h, "GET", "/painel/", sessaoTeste, nil).Body.String(); !strings.Contains(b, `title="pedidos aguardando a TI">1</span>`) {
		t.Error("o contador não baixou depois da recusa")
	}
}

// pngDe gera um PNG w×h.
func pngDe(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		img.Set(x, x%h, color.NRGBA{R: 200, G: 30, B: 60, A: 255})
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func envioDeLogo(h http.Handler, cookie, op string, arquivo []byte) *httptest.ResponseRecorder {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("op", op)
	if arquivo != nil {
		fw, _ := mw.CreateFormFile("arquivo", "logo.png")
		_, _ = fw.Write(arquivo)
	}
	_ = mw.Close()
	req := httptest.NewRequest("POST", "/painel/configuracao/aparencia/logo", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(&http.Cookie{Name: "patchd_sessao", Value: cookie})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// O logotipo: só admin envia; só PNG ou JPEG de verdade, até 256 KB e 1024 px; aparece no menu
// e no login; a troca e a remoção ficam na auditoria.
func TestLogotipoDoPainel(t *testing.T) {
	h, st, _ := ambientePaginas(t)
	ctx := context.Background()
	if r := pagina(h, "GET", "/painel/logo", "", nil); r.Code != http.StatusNotFound {
		t.Errorf("sem logotipo: %d", r.Code)
	}
	b := pagina(h, "GET", "/painel/configuracao", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, "<h2>Aparência</h2>") || !strings.Contains(b, "Sem logotipo: o painel usa o ícone padrão.") ||
		!strings.Contains(b, `enctype="multipart/form-data" action="/painel/configuracao/aparencia/logo"`) {
		t.Error("cartão Aparência sem logotipo")
	}
	if r := envioDeLogo(h, sessaoTeste, "enviar", pngDe(t, 64, 64)); r.Code != http.StatusForbidden {
		t.Errorf("leitura enviando: %d", r.Code)
	}
	gif := []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00;")
	falso := append([]byte("\x89PNG\r\n\x1a\n"), []byte("<script>alert(1)</script>")...)
	grandeDemais := append(pngDe(t, 8, 8), make([]byte, 300<<10)...)
	for nome, c := range map[string]struct {
		arq    []byte
		motivo string
	}{
		"gif":      {gif, "só PNG ou JPEG"},
		"falso":    {falso, "corrompida ou truncada"},
		"lado":     {pngDe(t, 2000, 10), "2000x10 pixels"},
		"tamanho":  {grandeDemais, "passa de 256 KB"},
		"sem nada": {nil, "escolha um arquivo"},
		"truncado": {pngDe(t, 64, 64)[:60], "corrompida ou truncada"},
	} {
		r := envioDeLogo(h, sessaoAdmin, "enviar", c.arq)
		loc := r.Header().Get("Location")
		if r.Code != http.StatusSeeOther || !strings.HasPrefix(loc, "/painel/configuracao?erro=logo") {
			t.Errorf("%s: %d %s", nome, r.Code, loc)
			continue
		}
		if b := pagina(h, "GET", loc, sessaoAdmin, nil).Body.String(); !strings.Contains(b, "O logotipo não foi gravado: ") || !strings.Contains(b, c.motivo) {
			t.Errorf("%s: faltou %q", nome, c.motivo)
		}
	}
	if _, ok, _ := st.PanelAsset(ctx, "logo"); ok {
		t.Fatal("arquivo recusado foi gravado")
	}

	arq := pngDe(t, 128, 96)
	r := envioDeLogo(h, sessaoAdmin, "enviar", arq)
	if r.Code != http.StatusSeeOther || r.Header().Get("Location") != "/painel/configuracao?logo=gravado" {
		t.Fatalf("enviar: %d %s", r.Code, r.Header().Get("Location"))
	}
	l, ok, _ := st.PanelAsset(ctx, "logo")
	if !ok || l.ContentType != "image/png" || l.UpdatedBy != "thyago" {
		t.Fatalf("gravado: %+v", l)
	}
	src := "/painel/logo?v=" + l.SHA256[:16]
	for _, p := range []string{"/painel/", "/painel/configuracao"} {
		if b := pagina(h, "GET", p, sessaoAdmin, nil).Body.String(); !strings.Contains(b, `<img src="`+src+`"`) {
			t.Errorf("%s: sem o logotipo", p)
		}
	}
	if b := pagina(h, "GET", "/painel/entrar", "", nil).Body.String(); !strings.Contains(b, `<img src="`+src+`" alt="">`) {
		t.Error("login sem o logotipo")
	}
	if b := pagina(h, "GET", "/painel/configuracao?logo=gravado", sessaoAdmin, nil).Body.String(); !strings.Contains(b, "Logotipo gravado.") ||
		!strings.Contains(b, "PNG · 1 KB · enviado por thyago") {
		t.Error("cartão depois do envio")
	}
	// Servido sem sessão, com o tipo conferido, cache longo só com a versão certa, 304 pela ETag.
	r = pagina(h, "GET", src, "", nil)
	if r.Code != 200 || r.Header().Get("Content-Type") != "image/png" || !bytes.Equal(r.Body.Bytes(), arq) ||
		r.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(r.Header().Get("Cache-Control"), "immutable") ||
		!strings.Contains(r.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("servir: %d %v", r.Code, r.Header())
	}
	if r := pagina(h, "GET", "/painel/logo?v=0000000000000000", "", nil); strings.Contains(r.Header().Get("Cache-Control"), "immutable") {
		t.Error("versão errada não pode ir para o cache longo")
	}
	if r := pagina(h, "GET", "/painel/logo", "", nil, "If-None-Match", r.Header().Get("ETag")); r.Code != http.StatusNotModified {
		t.Errorf("ETag: %d", r.Code)
	}

	if r := envioDeLogo(h, sessaoAdmin, "remover", nil); r.Header().Get("Location") != "/painel/configuracao?logo=removido" {
		t.Errorf("remover: %s", r.Header().Get("Location"))
	}
	if r := pagina(h, "GET", "/painel/logo", "", nil); r.Code != http.StatusNotFound {
		t.Errorf("depois de remover: %d", r.Code)
	}
	if b := pagina(h, "GET", "/painel/", sessaoAdmin, nil).Body.String(); strings.Contains(b, "/painel/logo") {
		t.Error("o menu ainda aponta para o logotipo")
	}
	ev, _ := st.AuditEvents(ctx, store.AuditFilter{})
	var acoes []string
	for _, e := range ev {
		if strings.HasPrefix(e.Action, "aparencia.") {
			acoes = append(acoes, e.Action+"|"+e.Detail)
		}
	}
	if strings.Join(acoes, ",") != "aparencia.logo_remover|,aparencia.logo|png · 1 KB · 128x96" &&
		strings.Join(acoes, ",") != "aparencia.logo|png · 1 KB · 128x96,aparencia.logo_remover|" {
		t.Errorf("auditoria: %v", acoes)
	}
}

// Aula 19.1b: a loja leva o nome do painel e o SHA-256 do logotipo; o agente baixa o arquivo
// com a credencial da máquina.
func TestLogotipoParaOAgente(t *testing.T) {
	m, _ := thirdparty.Default()
	_, priv, _ := jobs.GenerateKey()
	h, st, _ := ambientePaginas(t, api.WithJobKey(priv), api.WithContent(t.TempDir(), nil), api.WithThirdPartyManifest(&m), api.WithBrand("patchd · TJ"))
	ctx := context.Background()
	tok, hash := identity.NewSecret(identity.EnrollmentPrefix)
	_, _ = st.CreateEnrollmentToken(ctx, hash, "teste", time.Now().Add(time.Hour), 1)
	maq := registra(t, h, tok)
	loja := func() protocol.SelfServiceCatalog {
		t.Helper()
		r := envia(h, "POST", protocol.SelfServiceCatalogPath, maq.Credential, []byte(`{"account":"EXEMPLO\\ana","groups":[]}`))
		var c protocol.SelfServiceCatalog
		if r.Code != http.StatusOK || json.Unmarshal(r.Body.Bytes(), &c) != nil {
			t.Fatalf("loja: %d %s", r.Code, r.Body)
		}
		return c
	}
	if c := loja(); c.Brand != "patchd · TJ" || c.Logo != "" {
		t.Errorf("sem logotipo: %+v", c)
	}
	if r := envia(h, "GET", protocol.SelfServiceLogoPath, maq.Credential, nil); r.Code != http.StatusNotFound {
		t.Errorf("sem logotipo: %d", r.Code)
	}
	arq := pngDe(t, 32, 32)
	if r := envioDeLogo(h, sessaoAdmin, "enviar", arq); r.Header().Get("Location") != "/painel/configuracao?logo=gravado" {
		t.Fatalf("enviar: %s", r.Header().Get("Location"))
	}
	sum := sha256.Sum256(arq)
	if c := loja(); c.Logo != hex.EncodeToString(sum[:]) {
		t.Errorf("a loja anuncia o SHA-256: %q", c.Logo)
	}
	if r := envia(h, "GET", protocol.SelfServiceLogoPath, "", nil); r.Code != http.StatusUnauthorized {
		t.Errorf("sem credencial: %d", r.Code)
	}
	r := envia(h, "GET", protocol.SelfServiceLogoPath, maq.Credential, nil)
	if r.Code != 200 || r.Header().Get("Content-Type") != "image/png" || !bytes.Equal(r.Body.Bytes(), arq) {
		t.Errorf("agente: %d %v", r.Code, r.Header())
	}
}
