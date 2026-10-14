package api_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
)

// O botão Atualizar da página da máquina (Aula 14.6): o plano da máquina, o que falta para
// o botão aparecer e o job que ele cria, igual ao do package install.
func TestAtualizarTerceirosPeloPainel(t *testing.T) {
	pub, priv, _ := jobs.GenerateKey()
	dir := t.TempDir()
	m, _ := thirdparty.Default()
	h, st, log := ambientePaginas(t, api.WithJobKey(priv), api.WithContent(dir, nil), api.WithThirdPartyManifest(&m))
	ctx := context.Background()
	const id = "92fc3b93-0003"
	maq := "/painel/maquinas/" + id
	sw := func(name, ver, pub, productCode string) protocol.Software {
		return protocol.Software{Name: name, Version: ver, Publisher: pub, Scope: "machine", ProductCode: productCode}
	}
	if _, err := st.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: "w", OS: protocol.OSInfo{ID: "windows", Arch: "amd64"},
		Software: []protocol.Software{
			sw("Notepad++ (64-bit x64)", "8.9.7", "Notepad++ Team", ""),
			sw("Mozilla Firefox (x64 pt-BR)", "156.0", "Mozilla", ""),
			sw("Google Chrome", "155.0.8059.40", "Google LLC", "{C1A1}"),
			sw("7-Zip 26.02 (x64 edition)", "26.02.00.0", "Igor Pavlov", "{7Z}"),
			sw("7-Zip 26.02 (x64)", "26.02", "Igor Pavlov", ""),
		}}); err != nil {
		t.Fatal(err)
	}
	// O instalador do Notepad++ está no cache; o do Firefox, não.
	inst := []byte("MZ instalador do notepad++ 8.9.8.1")
	soma := sha256.Sum256(inst)
	npp := hex.EncodeToString(soma[:])
	pacotes := filepath.Join(dir, "pacotes")
	os.MkdirAll(pacotes, 0o750)
	if err := os.WriteFile(filepath.Join(pacotes, npp+".exe"), inst, 0o640); err != nil {
		t.Fatal(err)
	}
	ff := strings.Repeat("f", 64)
	for _, p := range []store.ThirdPartyPackage{
		{AppID: "notepad-plus-plus", WingetID: "Notepad++.Notepad++", Version: "8.9.8.1", Architecture: "x64", Type: "nullsoft",
			URL: "https://x", SHA256: npp, Size: int64(len(inst)), Arguments: "/S"},
		{AppID: "mozilla-firefox", WingetID: "Mozilla.Firefox.pt-BR", Version: "157.0.1", Architecture: "x64", Type: "nullsoft",
			URL: "https://x", SHA256: ff, Size: 64, Arguments: "/S /PreventRebootRequired=true"},
		{AppID: "google-chrome", WingetID: "Google.Chrome", Version: "155.0.8059.40", Architecture: "x64", Type: "wix",
			URL: "https://x", SHA256: strings.Repeat("c", 64), Size: 1, Arguments: "/quiet /norestart"},
	} {
		if _, err := st.SaveThirdPartyPackage(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	aba := maq + "?aba=pendencias"
	tem := func(quem, b string, trechos ...string) {
		t.Helper()
		for _, tr := range trechos {
			if !strings.Contains(b, tr) {
				t.Errorf("%s: faltou %q", quem, tr)
			}
		}
	}
	pede := func(app string) string {
		return local(pagina(h, "POST", maq+"/terceiros/atualizar", sessaoAdmin, url.Values{"app": {app}}))
	}

	// Sem aprovação: o plano aparece, sem botão; o pedido forjado é recusado e vai para o log.
	b := pagina(h, "GET", aba, sessaoAdmin, nil).Body.String()
	tem("sem aprovação", b, "<h2>Atualização pelo patchd</h2>", "2 para atualizar",
		`<span class="estado reboot_pendente">atualizar</span><div class="muted nota">8.9.7 → 8.9.8.1 · nullsoft · x64</div>`,
		`<a href="/painel/patch/programa?app=notepad-plus-plus">aguardando aprovação de 8.9.8.1</a>`,
		`<span class="estado desconhecido">duplicado</span>`, "<code>26.02.00.0 (msi), 26.02 (exe)</code>",
		`<span class="estado em_dia">em dia</span>`)
	if strings.Contains(b, "/terceiros/atualizar") {
		t.Error("botão sem aprovação")
	}
	if l := pede("notepad-plus-plus"); l != maq+"?erro=terceiros-nao-aprovado" {
		t.Errorf("sem aprovação: %s", l)
	}
	tem("log", log.String(), `msg="instalação de terceiros pelo painel recusada: versão sem aprovação" machine_id=92fc3b93-0003 app=notepad-plus-plus`)

	for _, a := range []store.ThirdPartyApproval{{AppID: "notepad-plus-plus", Version: "8.9.8.1"}, {AppID: "mozilla-firefox", Version: "157.0.1"}} {
		a.ApprovedBy = "ana"
		if _, err := st.ApproveThirdParty(ctx, a); err != nil {
			t.Fatal(err)
		}
	}

	// Aprovado: o admin vê o botão do Notepad++; o Firefox não tem o arquivo; o perfil
	// leitura vê o plano sem formulário.
	b = pagina(h, "GET", aba, sessaoAdmin, nil).Body.String()
	tem("admin", b, `<form method="post" action="/painel/maquinas/92fc3b93-0003/terceiros/atualizar"><input type="hidden" name="app" value="notepad-plus-plus"><button type="submit">Atualizar para 8.9.8.1</button></form>`,
		`<span class="muted">falta o instalador no cache: package fetch -app mozilla-firefox</span>`)
	if strings.Count(b, "/terceiros/atualizar") != 1 {
		t.Error("um botão só: o do Notepad++")
	}
	b = pagina(h, "GET", aba, sessaoTeste, nil).Body.String()
	tem("leitura", b, "<h2>Atualização pelo patchd</h2>", "aprovado; pedir exige o perfil admin")
	if strings.Contains(b, "/terceiros/atualizar") {
		t.Error("o perfil leitura não vê o botão")
	}
	if r := pagina(h, "POST", maq+"/terceiros/atualizar", sessaoTeste, url.Values{"app": {"notepad-plus-plus"}}); r.Code != http.StatusForbidden {
		t.Errorf("leitura pedindo: %d", r.Code)
	}

	// Recusas: sem arquivo; arquivo com o tamanho certo e o conteúdo errado (a página mostra
	// o botão, o pedido confere o SHA-256); em dia; duplicado; programa que a regra não tem.
	if l := pede("mozilla-firefox"); l != maq+"?erro=terceiros-sem-arquivo" {
		t.Errorf("sem arquivo: %s", l)
	}
	if err := os.WriteFile(filepath.Join(pacotes, ff+".exe"), []byte(strings.Repeat("x", 64)), 0o640); err != nil {
		t.Fatal(err)
	}
	if b := pagina(h, "GET", aba, sessaoAdmin, nil).Body.String(); !strings.Contains(b, `value="mozilla-firefox"`) {
		t.Error("tamanho certo: a página mostra o botão")
	}
	if l := pede("mozilla-firefox"); l != maq+"?erro=terceiros-sem-arquivo" {
		t.Errorf("conteúdo errado: %s", l)
	}
	tem("log do SHA-256", log.String(), "instalador ausente ou diferente no cache")
	for _, app := range []string{"google-chrome", "7zip", "adobe-acrobat", ""} {
		if l := pede(app); l != maq+"?erro=terceiros-nada" {
			t.Errorf("%q: %s", app, l)
		}
	}
	b = pagina(h, "GET", maq+"?erro=terceiros-sem-arquivo", sessaoAdmin, nil).Body.String()
	tem("aviso", b, "não confere com o SHA-256 guardado", "<h2>Atualização pelo patchd</h2>")
	if l, _ := st.Jobs(ctx, "", 10); len(l) != 0 {
		t.Fatalf("nenhuma recusa cria job: %+v", l)
	}

	// O pedido: o job thirdparty assinado, com o autor, 24 h e os parâmetros do plano.
	if l := pede("notepad-plus-plus"); l != maq+"?criado=1&job=1" {
		t.Fatalf("pedido: %s", l)
	}
	tem("log do pedido", log.String(), `msg="job criado pelo painel" job_id=1 machine_id=92fc3b93-0003 type=thirdparty app=notepad-plus-plus version=8.9.8.1 username=thyago`)
	env, _ := st.DeliverJobs(ctx, id, time.Now().UTC())
	if len(env) != 1 {
		t.Fatal("entrega")
	}
	j, err := jobs.Verify(pub, env[0], id, time.Now().UTC(), 0)
	if err != nil || j.Type != jobs.TypeThirdParty || j.NotAfter.Sub(j.IssuedAt) != 24*time.Hour {
		t.Fatalf("job: %+v %v", j, err)
	}
	p, err := jobs.ParseThirdPartyParams(j.Params)
	if err != nil || p.App != "notepad-plus-plus" || p.Version != "8.9.8.1" || p.SHA256 != npp || p.Size != int64(len(inst)) ||
		p.Installer != "nullsoft" || strings.Join(p.Args, " ") != "/S" || p.Signer != "NOTEPAD++" {
		t.Errorf("parâmetros: %+v %v", p, err)
	}
	if l, _ := st.Jobs(ctx, id, 1); l[0].CreatedBy != "thyago" {
		t.Errorf("autor: %+v", l[0])
	}

	// Com o job em aberto: nenhum botão, e um segundo pedido diz qual job.
	b = pagina(h, "GET", aba, sessaoAdmin, nil).Body.String()
	tem("em aberto", b, "job 1 em aberto: uma instalação por vez")
	if strings.Contains(b, "/terceiros/atualizar") {
		t.Error("botão com job em aberto")
	}
	if l := pede("notepad-plus-plus"); l != maq+"?erro=terceiros-aberto&job=1" {
		t.Errorf("segundo pedido: %s", l)
	}
	b = pagina(h, "GET", maq+"?erro=terceiros-aberto&job=1", sessaoAdmin, nil).Body.String()
	tem("aviso de aberto", b, "Já existe uma instalação de programa de terceiros em aberto para esta máquina (job 1)", "instalar programa de terceiros")

	// A máquina Linux não tem o cartão.
	if b := pagina(h, "GET", "/painel/maquinas/60ef97e4-0001?aba=pendencias", sessaoAdmin, nil).Body.String(); strings.Contains(b, "Atualização pelo patchd") {
		t.Error("cartão numa máquina sem inventário do Windows")
	}
}

// Sem a chave de jobs, o plano aparece e o pedido não cria nada.
func TestAtualizarTerceirosSemChave(t *testing.T) {
	m, _ := thirdparty.Default()
	h, st, _ := ambientePaginas(t, api.WithContent(t.TempDir(), nil), api.WithThirdPartyManifest(&m))
	ctx := context.Background()
	const id = "92fc3b93-0003"
	st.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: "w", OS: protocol.OSInfo{ID: "windows", Arch: "amd64"},
		Software: []protocol.Software{{Name: "Notepad++ (64-bit x64)", Version: "8.9.7", Publisher: "Notepad++ Team", Scope: "machine"}}})
	st.SaveThirdPartyPackage(ctx, store.ThirdPartyPackage{AppID: "notepad-plus-plus", Version: "8.9.8.1", Architecture: "x64", Type: "nullsoft",
		URL: "https://x", SHA256: strings.Repeat("a", 64), Size: 1, Arguments: "/S"})
	st.ApproveThirdParty(ctx, store.ThirdPartyApproval{AppID: "notepad-plus-plus", Version: "8.9.8.1", ApprovedBy: "ana"})
	b := pagina(h, "GET", "/painel/maquinas/"+id+"?aba=pendencias", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, "pedir exige a chave de jobs no servidor") || strings.Contains(b, "/terceiros/atualizar") {
		t.Errorf("sem chave:\n%s", b)
	}
	if l := local(pagina(h, "POST", "/painel/maquinas/"+id+"/terceiros/atualizar", sessaoAdmin, url.Values{"app": {"notepad-plus-plus"}})); l != "/painel/maquinas/"+id+"?erro=sem-chave" {
		t.Errorf("pedido: %s", l)
	}
}

// A página do programa diz quando o agente não instala (Aula 14.6b).
func TestProgramaComProcessos(t *testing.T) {
	agora := time.Now().UTC()
	src := &complianceFalso{
		fleet: protocol.ComplianceFleet{EvaluatedAt: agora, Machines: []protocol.ComplianceStatus{{MachineID: "w1", Hostname: "pc-a"}}},
		terceiros: map[string][]protocol.ThirdPartyItem{"w1": {{AppID: "vscode", Program: "Visual Studio Code",
			Name: "Microsoft Visual Studio Code", State: "DESATUALIZADO", Installed: "1.123.0", Reference: "1.141.0"}}},
	}
	st := store.NewMemory()
	abreSessao(t, st, sessaoTeste, agora)
	m, _ := thirdparty.Default()
	h := api.New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), api.WithCompliance(src), api.WithThirdPartyManifest(&m))
	b := pagina(h, "GET", "/painel/patch/programa?app=vscode", sessaoTeste, nil).Body.String()
	if !strings.Contains(b, "assinatura exigida: Microsoft Corporation · não instala com Code.exe aberto") {
		t.Errorf("programa vscode:\n%s", b)
	}
}
