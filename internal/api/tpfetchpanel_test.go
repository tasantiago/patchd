package api_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/pkgcache"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
)

// Aula 23.3: baixar e excluir instaladores de terceiros pelo painel.
func TestBaixarEExcluirPeloPainel(t *testing.T) {
	agora := time.Now().UTC()
	src := &complianceFalso{
		fleet: protocol.ComplianceFleet{EvaluatedAt: agora, Machines: []protocol.ComplianceStatus{{MachineID: "w1", Hostname: "pc-a"}}},
		terceiros: map[string][]protocol.ThirdPartyItem{"w1": {
			{AppID: "google-chrome", Program: "Google Chrome", Name: "Google Chrome", State: "DESATUALIZADO", Installed: "150.0.1", Reference: "155.0.8059.40"},
		}},
	}
	st := store.NewMemory()
	ctx := context.Background()
	abreSessao(t, st, sessaoTeste, agora)
	st.CreatePanelSession(ctx, identity.Hash(sessaoAdmin), panel.Session{Username: "thyago", Role: panel.RoleAdmin,
		Source: panel.SourceAD, CreatedAt: agora, ExpiresAt: agora.Add(time.Hour), LastSeenAt: agora})

	// Três versões guardadas; a 153 usa o mesmo arquivo da 154 (o mesmo SHA-256 em duas linhas).
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "pacotes"), 0o755); err != nil {
		t.Fatal(err)
	}
	guarda := func(ver, sha string) {
		st.SaveThirdPartyPackage(ctx, store.ThirdPartyPackage{AppID: "google-chrome", WingetID: "Google.Chrome", Version: ver, Architecture: "x64",
			Type: "wix", URL: "https://x", SHA256: sha, Size: 3})
		if err := os.WriteFile(filepath.Join(dir, "pacotes", pkgcache.FileName(sha, true)), []byte("msi"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	shaA, shaB := strings.Repeat("a", 64), strings.Repeat("b", 64)
	guarda("153.0.1", shaA)
	guarda("154.0.7000.1", shaA)
	guarda("155.0.8059.40", shaB)
	if _, err := st.ApproveThirdParty(ctx, store.ThirdPartyApproval{AppID: "google-chrome", Version: "155.0.8059.40", ApprovedBy: "thyago"}); err != nil {
		t.Fatal(err)
	}

	// O download falso: espera a liberação do teste, escreve o andamento (a última linha sem
	// quebra) e registra a versão pedida.
	libera := make(chan struct{})
	pedidas := make(chan string, 4)
	baixa := func(ctx context.Context, rule thirdparty.App, ver string, out io.Writer) bool {
		pedidas <- rule.ID + " " + ver
		fmt.Fprintf(out, "%s (%s) ← winget %s %s · wix · x64\n", rule.Name, rule.ID, rule.Install.Winget, ver)
		<-libera
		fmt.Fprint(out, "  baixado     pacotes/cc.msi (3 B)\n  registrado  novo")
		st.SaveThirdPartyPackage(ctx, store.ThirdPartyPackage{AppID: rule.ID, WingetID: "Google.Chrome", Version: ver, Architecture: "x64",
			Type: "wix", URL: "https://x", SHA256: strings.Repeat("c", 64), Size: 3})
		return true
	}
	m, _ := thirdparty.Default()
	log := &syncBuffer{}
	h := api.New(st, slog.New(slog.NewTextHandler(log, nil)), api.WithCompliance(src), api.WithThirdPartyManifest(&m),
		api.WithContent(dir, nil), api.WithPackageFetch(baixa))
	prog := "/painel/patch/programa?app=google-chrome"
	post := func(cookie, path string, f url.Values) *http.Response {
		return pagina(h, "POST", path, cookie, f).Result()
	}

	// Leitura: nem baixar nem excluir, nem pelo formulário nem direto.
	b := pagina(h, "GET", prog, sessaoTeste, nil).Body.String()
	if strings.Contains(b, "/painel/patch/terceiros/baixar") || strings.Contains(b, "/painel/patch/terceiros/excluir") ||
		!strings.Contains(b, "Aprovar, baixar e excluir exigem o perfil admin") {
		t.Errorf("leitura:\n%s", b)
	}
	for _, p := range []string{"/painel/patch/terceiros/baixar", "/painel/patch/terceiros/excluir"} {
		if r := post(sessaoTeste, p, url.Values{"app": {"google-chrome"}, "version": {"153.0.1"}}); r.StatusCode != http.StatusForbidden {
			t.Errorf("leitura em %s: %d", p, r.StatusCode)
		}
	}

	// Admin: o formulário de baixar e o "Excluir" só nas versões não aprovadas.
	b = pagina(h, "GET", prog, sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, `action="/painel/patch/terceiros/baixar"`) || strings.Count(b, "<summary>Excluir esta versão</summary>") != 2 ||
		strings.Contains(b, "Excluir os instaladores da 155.0.8059.40") || strings.Contains(b, "http-equiv") {
		t.Errorf("admin:\n%s", b)
	}

	// Versão digitada inválida; depois, o download da versão pedida.
	if l := local(pagina(h, "POST", "/painel/patch/terceiros/baixar", sessaoAdmin, url.Values{"app": {"google-chrome"}, "versao": {"1 2"}})); l != prog+"&erro=versao" {
		t.Errorf("versão inválida: %s", l)
	}
	if l := local(pagina(h, "POST", "/painel/patch/terceiros/baixar", sessaoAdmin, url.Values{"app": {"google-chrome"}, "versao": {"156.0.1"}})); l != prog+"&baixando=1" {
		t.Fatalf("baixar: %s", l)
	}
	if p := <-pedidas; p != "google-chrome 156.0.1" {
		t.Errorf("pedida: %q", p)
	}
	// Em andamento: a página se atualiza, mostra o andamento e não oferece outro download;
	// um segundo pedido e a exclusão de uma versão do mesmo programa esperam.
	b = pagina(h, "GET", prog+"&baixando=1", sessaoAdmin, nil).Body.String()
	for _, tr := range []string{`<meta http-equiv="refresh" content="5">`, "Download iniciado", "<strong>Baixando</strong> · versão 156.0.1 · pedido por thyago",
		"Google Chrome (google-chrome) ← winget Google.Chrome 156.0.1"} {
		if !strings.Contains(b, tr) {
			t.Errorf("em andamento: faltou %q", tr)
		}
	}
	if strings.Contains(b, `action="/painel/patch/terceiros/baixar"`) {
		t.Error("formulário de baixar durante o download")
	}
	if l := local(pagina(h, "POST", "/painel/patch/terceiros/baixar", sessaoAdmin, url.Values{"app": {"google-chrome"}})); l != prog+"&erro=baixando" {
		t.Errorf("segundo download: %s", l)
	}
	if l := local(pagina(h, "POST", "/painel/patch/terceiros/excluir", sessaoAdmin, url.Values{"app": {"google-chrome"}, "version": {"153.0.1"}})); l != prog+"&erro=baixando" {
		t.Errorf("excluir durante o download: %s", l)
	}
	close(libera)
	prazo := time.Now().Add(5 * time.Second)
	for {
		b = pagina(h, "GET", prog, sessaoAdmin, nil).Body.String()
		if strings.Contains(b, "<strong>Download concluído</strong>") || time.Now().After(prazo) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, tr := range []string{"<strong>Download concluído</strong>", "registrado  novo", "<code>156.0.1</code>", `action="/painel/patch/terceiros/baixar"`} {
		if !strings.Contains(b, tr) {
			t.Errorf("concluído: faltou %q\n%s", tr, b)
		}
	}
	if strings.Contains(b, "http-equiv") {
		t.Error("concluído e ainda se atualizando")
	}

	// Excluir: a aprovada não; a 154 sai e o arquivo fica (a 153 usa o mesmo); a 153 sai
	// e leva o arquivo; de novo, nada a excluir.
	exclui := func(ver string) string {
		return local(pagina(h, "POST", "/painel/patch/terceiros/excluir", sessaoAdmin, url.Values{"app": {"google-chrome"}, "version": {ver}}))
	}
	arqA := filepath.Join(dir, "pacotes", pkgcache.FileName(shaA, true))
	if l := exclui("155.0.8059.40"); l != prog+"&erro=aprovada" {
		t.Errorf("aprovada: %s", l)
	}
	if l := exclui("154.0.7000.1"); l != prog+"&excluido=1" {
		t.Fatalf("excluir a 154: %s", l)
	}
	if _, err := os.Stat(arqA); err != nil {
		t.Errorf("o arquivo da 153 sumiu com a 154: %v", err)
	}
	if l := exclui("153.0.1"); l != prog+"&excluido=1" {
		t.Fatalf("excluir a 153: %s", l)
	}
	if _, err := os.Stat(arqA); !os.IsNotExist(err) {
		t.Errorf("o arquivo sem dono ficou: %v", err)
	}
	if l := exclui("153.0.1"); l != prog+"&erro=nada" {
		t.Errorf("de novo: %s", l)
	}
	if _, err := os.Stat(filepath.Join(dir, "pacotes", pkgcache.FileName(shaB, true))); err != nil {
		t.Errorf("o arquivo da aprovada: %v", err)
	}
	l, _ := st.ThirdPartyPackages(ctx)
	var ficou []string
	for _, p := range l {
		ficou = append(ficou, p.Version)
	}
	if strings.Join(ficou, " ") != "156.0.1 155.0.8059.40" && strings.Join(ficou, " ") != "155.0.8059.40 156.0.1" {
		t.Errorf("ficaram: %v", ficou)
	}
	b = pagina(h, "GET", prog+"&excluido=1", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, "Versão excluída") {
		t.Error("mensagem da exclusão")
	}
	for _, tr := range []string{`msg="download de instalador pedido pelo painel" app=google-chrome version=156.0.1 username=thyago`,
		`msg="versão de instalador de terceiros excluída" app=google-chrome version=153.0.1 installers=1 bytes_freed=3 username=thyago`} {
		if !strings.Contains(log.String(), tr) {
			t.Errorf("log: faltou %q\n%s", tr, log)
		}
	}
	ev, _ := st.AuditEvents(ctx, store.AuditFilter{Limit: 20})
	acoes := map[string]int{}
	for _, e := range ev {
		acoes[e.Action]++
	}
	if acoes["terceiros.baixar"] != 1 || acoes["terceiros.excluir"] != 2 {
		t.Errorf("auditoria: %v", acoes)
	}
}

// Sem o download ligado (armazenamento em memória, sem pasta de conteúdo), o painel continua
// indicando a linha de comando.
func TestBaixarSemOServico(t *testing.T) {
	agora := time.Now().UTC()
	src := &complianceFalso{
		fleet: protocol.ComplianceFleet{EvaluatedAt: agora, Machines: []protocol.ComplianceStatus{{MachineID: "w1", Hostname: "pc-a"}}},
		terceiros: map[string][]protocol.ThirdPartyItem{"w1": {
			{AppID: "7zip", Program: "7-Zip", Name: "7-Zip", State: "DESATUALIZADO", Installed: "23.01", Reference: "26.04"},
		}},
	}
	st := store.NewMemory()
	ctx := context.Background()
	st.CreatePanelSession(ctx, identity.Hash(sessaoAdmin), panel.Session{Username: "thyago", Role: panel.RoleAdmin,
		Source: panel.SourceAD, CreatedAt: agora, ExpiresAt: agora.Add(time.Hour), LastSeenAt: agora})
	m, _ := thirdparty.Default()
	h := api.New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), api.WithCompliance(src), api.WithThirdPartyManifest(&m))
	b := pagina(h, "GET", "/painel/patch/programa?app=7zip", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, "<code>patchd-server package fetch -app 7zip</code>") || strings.Contains(b, `action="/painel/patch/terceiros/baixar"`) {
		t.Errorf("sem o serviço:\n%s", b)
	}
	if l := local(pagina(h, "POST", "/painel/patch/terceiros/baixar", sessaoAdmin, url.Values{"app": {"7zip"}})); l != "/painel/patch?aba=terceiros" {
		t.Errorf("baixar sem o serviço: %s", l)
	}
}
