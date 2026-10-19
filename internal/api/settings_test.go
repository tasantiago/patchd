package api_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/release"
)

// versaoPublicada monta uma pasta de versões com a v0.14.4 publicada (dois executáveis).
func versaoPublicada(t *testing.T) (string, []byte) {
	t.Helper()
	dir := t.TempDir()
	exe := []byte("MZ agente do windows v0.14.4")
	vdir := filepath.Join(dir, "v0.14.4")
	os.MkdirAll(vdir, 0o755)
	m := release.Manifest{SchemaVersion: release.ManifestSchemaVersion, Version: "v0.14.4", CreatedAt: time.Now().UTC().Truncate(time.Second)}
	for name, data := range map[string][]byte{"patchd-agent-windows-amd64.exe": exe, "patchd-agent-linux-amd64": []byte("ELF agente linux")} {
		os.WriteFile(filepath.Join(vdir, name), data, 0o644)
		osName, arch := "windows", "amd64"
		if !strings.HasSuffix(name, ".exe") {
			osName = "linux"
		}
		m.Files = append(m.Files, release.File{OS: osName, Arch: arch, Name: name, SHA256: release.HashBytes(data), Size: int64(len(data))})
	}
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	man, sig, err := release.Sign(priv, m)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(vdir, release.ManifestFile), man, 0o644)
	os.WriteFile(filepath.Join(vdir, release.SignatureFile), sig, 0o644)
	if err := release.Dir(dir).Publish("v0.14.4"); err != nil {
		t.Fatal(err)
	}
	return dir, exe
}

const caDeTeste = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"

// Configuração (Aula 15.2): só admin; a aba Sistema; a aba Agente com os executáveis, a CA
// e os tokens de registro.
func TestConfiguracao(t *testing.T) {
	pub, priv, _ := jobs.GenerateKey()
	rel, exe := versaoPublicada(t)
	inicio := time.Now().Add(-3 * time.Hour)
	h, st, log := ambientePaginas(t, api.WithJobKey(priv), api.WithPublicURL("https://patchd.exemplo:8443"),
		api.WithReleases(release.Dir(rel), "v0.14.4"), api.WithAgentCA([]byte(caDeTeste)),
		api.WithSystemInfo(api.SystemInfo{Version: "v0.14.4", Commit: "8ba97055b6cae67f266bde790a92f33475a01361", GoVersion: "go1.27.1",
			StartedAt: inicio, Database: true, Schema: 30, TLSNotAfter: func() time.Time { return time.Now().Add(10 * 24 * time.Hour) },
			Catalog: "sincronizado a cada 6h0m0s", Retention: "inventários e buscas: 90 dias"}))
	ctx := context.Background()

	// O perfil leitura não vê o menu nem entra.
	if b := pagina(h, "GET", "/painel/", sessaoTeste, nil).Body.String(); strings.Contains(b, "/painel/configuracao") {
		t.Error("leitura vê o menu")
	}
	for _, p := range []string{"/painel/configuracao", "/painel/configuracao?aba=agente", "/painel/configuracao/agente/arquivo/patchd-agent-linux-amd64", "/painel/configuracao/agente/ca.crt"} {
		if r := pagina(h, "GET", p, sessaoTeste, nil); r.Code != http.StatusForbidden {
			t.Errorf("leitura em %s: %d", p, r.Code)
		}
	}

	// Sistema.
	b := pagina(h, "GET", "/painel/configuracao", sessaoAdmin, nil).Body.String()
	for _, tr := range []string{`href="/painel/configuracao" title="Configuração" class="ativo"`, "<dt>Servidor</dt><dd>v0.14.4 · commit 8ba97055b6ca · go1.27.1</dd>",
		"<dt>Banco</dt><dd>PostgreSQL · migração 30</dd>", `<dt>TLS</dt><dd class="nivel-atencao">ligado · o certificado vence em`,
		"<dt>URL pública</dt><dd>https://patchd.exemplo:8443</dd>", "<dt>Jobs</dt><dd>ligados · chave " + jobs.KeyID(pub) + "</dd>",
		"<dt>Atualização do agente</dt><dd>publicada v0.14.4</dd>", `<dt>Conteúdo</dt><dd class="nivel-atencao">desligado`,
		"<dt>Login do painel</dt><dd>usuários locais (contingência)</dd>", "UTC (há 3 h)"} {
		if !strings.Contains(b, tr) {
			t.Errorf("sistema: faltou %q", tr)
		}
	}

	// Agente: os executáveis, a CA e o formulário.
	b = pagina(h, "GET", "/painel/configuracao?aba=agente", sessaoAdmin, nil).Body.String()
	for _, tr := range []string{"Executáveis da versão v0.14.4", `<a href="/painel/configuracao/agente/arquivo/patchd-agent-windows-amd64.exe"><code>patchd-agent-windows-amd64.exe</code></a>`,
		`href="/painel/configuracao/agente/ca.crt"`, `action="/painel/configuracao/agente/token"`, "Nenhum token criado."} {
		if !strings.Contains(b, tr) {
			t.Errorf("agente: faltou %q", tr)
		}
	}
	r := pagina(h, "GET", "/painel/configuracao/agente/arquivo/patchd-agent-windows-amd64.exe", sessaoAdmin, nil)
	if r.Code != http.StatusOK || r.Body.String() != string(exe) || !strings.Contains(r.Header().Get("Content-Disposition"), "patchd-agent-windows-amd64.exe") {
		t.Errorf("download: %d %q", r.Code, r.Header().Get("Content-Disposition"))
	}
	if !strings.Contains(log.String(), "agente baixado pelo painel") {
		t.Error("o download vai para o log")
	}
	for _, n := range []string{"manifest.json", "manifest.sig", "..%2Fcurrent", "x.exe"} {
		if r := pagina(h, "GET", "/painel/configuracao/agente/arquivo/"+n, sessaoAdmin, nil); r.Code != http.StatusNotFound {
			t.Errorf("%s: %d", n, r.Code)
		}
	}
	if r := pagina(h, "GET", "/painel/configuracao/agente/ca.crt", sessaoAdmin, nil); r.Body.String() != caDeTeste {
		t.Errorf("ca: %q", r.Body)
	}

	// Token: aparece uma vez, com os comandos; não vai para o log; registra uma máquina.
	r = pagina(h, "POST", "/painel/configuracao/agente/token", sessaoAdmin, url.Values{"nota": {"laboratório"}, "validade": {"24"}, "usos": {"2"}})
	b = r.Body.String()
	tok := regexp.MustCompile(`patchd_enr_[A-Za-z0-9_-]+`).FindString(b)
	if r.Code != http.StatusOK || tok == "" || r.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("token: %d %q", r.Code, r.Header().Get("Cache-Control"))
	}
	for _, tr := range []string{"Guarde agora", "para 2 máquina(s) (laboratório)",
		`.\patchd-agent-windows-amd64.exe install -server https://patchd.exemplo:8443 -enroll-token-file $t -ca-file C:\ProgramData\patchd\ca.crt`,
		`Copy-Item .\ca.crt C:\ProgramData\patchd\ca.crt`, "sudo ./patchd-agent-linux-amd64 install -server https://patchd.exemplo:8443 -enroll-token-file &#34;$t&#34; -ca-file /etc/patchd/ca.crt"} {
		if !strings.Contains(b, tr) {
			t.Errorf("token criado: faltou %q", tr)
		}
	}
	if strings.Contains(log.String(), tok) || !strings.Contains(log.String(), "token de registro criado pelo painel") {
		t.Error("log do token")
	}
	registra(t, h, tok)
	list, _ := st.EnrollmentTokens(ctx)
	if len(list) != 1 || list[0].Note != "laboratório" || list[0].MaxUses != 2 || list[0].Uses != 1 {
		t.Errorf("gravado: %+v", list)
	}
	// Validade fora da lista: volta com o motivo.
	if r := pagina(h, "POST", "/painel/configuracao/agente/token", sessaoAdmin, url.Values{"validade": {"5"}, "usos": {"1"}}); r.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(r.Body.String(), "Escolha a validade da lista") {
		t.Errorf("validade: %d", r.Code)
	}
	if r := pagina(h, "POST", "/painel/configuracao/agente/token", sessaoTeste, url.Values{"validade": {"24"}, "usos": {"1"}}); r.Code != http.StatusForbidden {
		t.Errorf("leitura cria token: %d", r.Code)
	}

	// Revogar.
	b = pagina(h, "GET", "/painel/configuracao?aba=agente", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, "<td class=\"num\">1 de 2</td><td>válido</td>") || !strings.Contains(b, `action="/painel/configuracao/agente/token/1/revogar"`) {
		t.Errorf("lista:\n%s", b)
	}
	if l := local(pagina(h, "POST", "/painel/configuracao/agente/token/1/revogar", sessaoAdmin, url.Values{})); l != "/painel/configuracao?aba=agente&revogado=1" {
		t.Errorf("revogar: %s", l)
	}
	if b := pagina(h, "GET", "/painel/configuracao?aba=agente&revogado=1", sessaoAdmin, nil).Body.String(); !strings.Contains(b, "Token 1 revogado") ||
		!strings.Contains(b, "<td>revogado</td>") || strings.Contains(b, "/token/1/revogar") {
		t.Errorf("revogado:\n%s", b)
	}
	if l := local(pagina(h, "POST", "/painel/configuracao/agente/token/9/revogar", sessaoAdmin, url.Values{})); l != "/painel/configuracao?aba=agente&erro=revogar" {
		t.Errorf("inexistente: %s", l)
	}
}

// Sem a CA, sem versão publicada e sem URL pública: os avisos, e o comando sem -ca-file.
func TestConfiguracaoMinima(t *testing.T) {
	h, _, _ := ambientePaginas(t)
	b := pagina(h, "GET", "/painel/configuracao?aba=agente", sessaoAdmin, nil).Body.String()
	for _, tr := range []string{"A atualização do agente está desligada", "Sem PATCHD_AGENT_CA_FILE"} {
		if !strings.Contains(b, tr) {
			t.Errorf("faltou %q", tr)
		}
	}
	b = pagina(h, "POST", "/painel/configuracao/agente/token", sessaoAdmin, url.Values{"validade": {"1"}, "usos": {"1"}}).Body.String()
	if !strings.Contains(b, "install -server https://&lt;endereço-do-servidor&gt; -enroll-token-file $t\n") || strings.Contains(b, "-ca-file") {
		t.Errorf("comando sem CA:\n%s", b)
	}
	b = pagina(h, "GET", "/painel/configuracao", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, `<dt>Jobs</dt><dd class="nivel-erro">desligados`) {
		t.Errorf("sem chave:\n%s", b)
	}
}
