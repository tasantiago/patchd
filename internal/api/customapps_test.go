package api_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/choco"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
	"github.com/tasantiago/patchd/internal/winget"
)

// Incluir programa pelo painel: a busca no winget preenche o formulário; salvar grava a regra,
// troca o manifesto vivo (o programa aparece na hora na página dele e no autoatendimento) e
// pede a versão de referência; editar troca a regra; excluir é recusado com o programa em uso.
func TestIncluirProgramaPeloPainel(t *testing.T) {
	ctx := context.Background()
	base, _ := thirdparty.Default()
	live := thirdparty.NewLive(base)
	descrever := func(_ context.Context, id string) (winget.Description, error) {
		if id != "VideoLAN.VLC" {
			return winget.Description{}, errors.New("não existe")
		}
		return winget.Description{ID: id, Version: "3.0.21", Name: "VLC media player", Publisher: "VideoLAN", Types: []string{"nullsoft", "wix"},
			DisplayName: "VLC media player", DisplayPublisher: "VideoLAN", SignerOrg: "VideoLAN", SignerSubject: "CN=VideoLAN, O=VideoLAN, L=Paris, C=FR"}, nil
	}
	var mu sync.Mutex
	var sincronizados []string
	sync := func(_ context.Context, a thirdparty.App) {
		mu.Lock()
		sincronizados = append(sincronizados, a.ID)
		mu.Unlock()
	}
	h, st, _ := ambientePaginas(t, api.WithThirdPartyManifest(live), api.WithCustomApps(base, descrever, sync),
		api.WithCompliance(&complianceFalso{}))
	const pagina_ = "/painel/patch/terceiros/incluir"
	if r := pagina(h, "GET", pagina_, sessaoTeste, nil); r.Code != http.StatusForbidden {
		t.Errorf("leitura: %d", r.Code)
	}
	// A busca preenche.
	b := pagina(h, "GET", pagina_+"?winget=VideoLAN.VLC", sessaoAdmin, nil).Body.String()
	for _, quer := range []string{"Achado: <b>VLC media player</b> 3.0.21", `name="nome" value="VLC media player"`, `name="inventario" value="VLC media player"`,
		`name="fabricante" value="VideoLAN"`, `<option value="nullsoft" selected>`,
		// A assinatura, lida do instalador pelo servidor, já no campo.
		`name="assinante" value="VideoLAN"`, "Assinatura do instalador: <b>VideoLAN</b>"} {
		if !strings.Contains(b, quer) {
			t.Errorf("busca: falta %q", quer)
		}
	}
	if b := pagina(h, "GET", pagina_+"?winget=Nao.Existe", sessaoAdmin, nil).Body.String(); !strings.Contains(b, "Não achei o pacote") {
		t.Error("pacote que não existe")
	}
	if b := pagina(h, "GET", pagina_+"?winget=semponto", sessaoAdmin, nil).Body.String(); !strings.Contains(b, "Fabricante.Programa") {
		t.Error("pacote fora da forma")
	}
	form := url.Values{"nome": {"VLC"}, "winget": {"VideoLAN.VLC"}, "inventario": {"VLC media player"}, "modo": {"igual"},
		"fabricante": {"VideoLAN"}, "tipo": {"nullsoft"}, "assinatura": {"sim"}, "processos": {"vlc.exe"},
		// O Subject inteiro colado do PowerShell: fica só o O=.
		"assinante": {"C=FR, L=Paris, O=VideoLAN, CN=VideoLAN"}}
	// Sem assinante: volta com o formulário preenchido.
	ruim := url.Values{}
	for k, v := range form {
		ruim[k] = v
	}
	ruim.Set("assinante", "")
	if r := pagina(h, "POST", pagina_, sessaoAdmin, ruim); r.Code != http.StatusUnprocessableEntity || !strings.Contains(r.Body.String(), "organização que assina") ||
		!strings.Contains(r.Body.String(), `value="VideoLAN.VLC"`) {
		t.Errorf("sem assinante: %d", r.Code)
	}
	// Um programa do manifesto não se inclui de novo.
	chrome := url.Values{"nome": {"Chrome"}, "winget": {"Google.Chrome"}, "inventario": {"Google Chrome"}, "assinatura": {"sim"}, "assinante": {"Google LLC"}}
	if r := pagina(h, "POST", pagina_, sessaoAdmin, chrome); r.Code != http.StatusUnprocessableEntity {
		// o id do Google.Chrome é google-chrome, que o manifesto já tem
		t.Errorf("programa do manifesto: %d", r.Code)
	}
	r := pagina(h, "POST", pagina_, sessaoAdmin, form)
	if r.Code != http.StatusSeeOther || !strings.Contains(local(r), "salvo=videolan-vlc") {
		t.Fatalf("salvar: %d %s %s", r.Code, local(r), r.Body)
	}
	// Depois de salvar: o botão leva direto à página do programa (onde se baixa o instalador).
	if b := pagina(h, "GET", local(r), sessaoAdmin, nil).Body.String(); !strings.Contains(b,
		`<a class="btn" href="/painel/patch/programa?app=videolan-vlc">Abrir VLC e baixar o instalador</a>`) {
		t.Error("sem o botão para abrir o programa salvo")
	}
	if !live.Get().Has("videolan-vlc/windows") {
		t.Fatal("o manifesto vivo não recebeu o programa")
	}
	for i := 0; i < 100; i++ {
		mu.Lock()
		n := len(sincronizados)
		mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(sincronizados) != 1 || sincronizados[0] != "videolan-vlc" {
		t.Errorf("versão de referência pedida: %v", sincronizados)
	}
	// Na hora: a página do programa (sem nenhuma máquina com ele) e o autoatendimento.
	b = pagina(h, "GET", "/painel/patch/programa?app=videolan-vlc", sessaoAdmin, nil).Body.String()
	for _, quer := range []string{"<h1>VLC</h1>", "Incluído pelo painel", "Instaladores guardados", "winget <code>VideoLAN.VLC</code>", "Nenhuma máquina tem este programa"} {
		if !strings.Contains(b, quer) {
			t.Errorf("página do programa: falta %q", quer)
		}
	}
	if b := pagina(h, "GET", "/painel/configuracao?aba=autoatendimento", sessaoAdmin, nil).Body.String(); !strings.Contains(b, "<code>videolan-vlc</code>") {
		t.Error("o programa não aparece no autoatendimento")
	}
	// A lista e a edição (o nome no inventário volta como texto).
	b = pagina(h, "GET", pagina_+"?editar=videolan-vlc", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, `name="editar" value="videolan-vlc"`) || !strings.Contains(b, `name="inventario" value="VLC media player"`) || !strings.Contains(b, `name="assinante" value="VideoLAN"`) ||
		!strings.Contains(b, `name="processos" value="vlc.exe"`) || !strings.Contains(b, "Incluídos pelo painel") {
		t.Error("edição")
	}
	form.Set("editar", "videolan-vlc")
	form.Set("modo", "comeca")
	form.Set("inventario", "VLC media player (x64)")
	if r := pagina(h, "POST", pagina_, sessaoAdmin, form); r.Code != http.StatusSeeOther {
		t.Fatalf("editar: %d %s", r.Code, r.Body)
	}
	if got := live.For("windows"); !got[len(got)-1].Matches("VLC media player (x64) 3.0", "VideoLAN") {
		t.Error("a edição não trocou a regra")
	}
	form.Set("winget", "Outro.Pacote")
	if r := pagina(h, "POST", pagina_, sessaoAdmin, form); r.Code != http.StatusUnprocessableEntity {
		t.Errorf("trocar o pacote na edição: %d", r.Code)
	}
	// Em uso (oferta no autoatendimento): não exclui; sem a oferta, exclui e some na hora.
	if err := st.SetSelfServiceOffer(ctx, store.SelfServiceOffer{AppID: "videolan-vlc", CanInstall: true, UpdatedBy: "thyago"}); err != nil {
		t.Fatal(err)
	}
	if loc := local(pagina(h, "POST", "/painel/patch/terceiros/incluidos/excluir", sessaoAdmin, url.Values{"id": {"videolan-vlc"}})); !strings.Contains(loc, "erro=em-uso") {
		t.Errorf("em uso: %s", loc)
	}
	_, _ = st.DeleteSelfServiceOffer(ctx, "videolan-vlc")
	if loc := local(pagina(h, "POST", "/painel/patch/terceiros/incluidos/excluir", sessaoAdmin, url.Values{"id": {"videolan-vlc"}})); !strings.Contains(loc, "excluido=") {
		t.Errorf("excluir: %s", loc)
	}
	if live.Get().Has("videolan-vlc/windows") || len(live.Get().Apps) != len(base.Apps) {
		t.Error("o programa excluído ficou no manifesto")
	}
	if loc := local(pagina(h, "POST", "/painel/patch/terceiros/incluidos/excluir", sessaoAdmin, url.Values{"id": {"google-chrome"}})); !strings.Contains(loc, "erro=nao-encontrado") {
		t.Errorf("programa do manifesto: %s", loc)
	}
	// Auditoria.
	ev, _ := st.AuditEvents(ctx, store.AuditFilter{Limit: 20})
	acoes := map[string]bool{}
	for _, e := range ev {
		acoes[e.Action] = true
	}
	for _, a := range []string{"terceiros.incluir", "terceiros.editar", "terceiros.excluir_programa"} {
		if !acoes[a] {
			t.Errorf("auditoria sem %s", a)
		}
	}
}

// "Remover a versão instalada antes de instalar a nova": a busca do winget marca quando o
// fabricante pede (uninstallPrevious), a regra guarda e a página do programa mostra.
func TestIncluirComRemoverAntes(t *testing.T) {
	base, _ := thirdparty.Default()
	live := thirdparty.NewLive(base)
	descrever := func(_ context.Context, id string) (winget.Description, error) {
		return winget.Description{ID: id, Version: "6.3.3", Name: "GlobalProtect", Publisher: "Palo Alto Networks", Types: []string{"msi"},
			DisplayName: "GlobalProtect", DisplayPublisher: "Palo Alto Networks", UninstallPrevious: true, SignerOrg: "Palo Alto Networks, Inc"}, nil
	}
	h, _, _ := ambientePaginas(t, api.WithThirdPartyManifest(live), api.WithCustomApps(base, descrever, nil), api.WithCompliance(&complianceFalso{}))
	b := pagina(h, "GET", "/painel/patch/terceiros/incluir?winget=PaloAltoNetworks.GlobalProtect", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, `name="remover_antes" value="1" checked`) || !strings.Contains(b, "O fabricante pede remover a versão anterior") {
		t.Errorf("busca:\n%s", b)
	}
	form := url.Values{"nome": {"GlobalProtect"}, "winget": {"PaloAltoNetworks.GlobalProtect"}, "inventario": {"GlobalProtect"}, "modo": {"igual"},
		"fabricante": {"Palo Alto Networks"}, "tipo": {"msi"}, "assinatura": {"sim"}, "assinante": {"Palo Alto Networks, Inc"}, "remover_antes": {"1"}}
	if r := pagina(h, "POST", "/painel/patch/terceiros/incluir", sessaoAdmin, form); r.Code != http.StatusSeeOther {
		t.Fatalf("salvar: %d %s", r.Code, r.Body)
	}
	var rule thirdparty.App
	for _, a := range live.For("windows") {
		if a.ID == "paloaltonetworks-globalprotect" {
			rule = a
		}
	}
	if rule.Install == nil || !rule.Install.UninstallFirst {
		t.Fatalf("regra: %+v", rule.Install)
	}
	if b := pagina(h, "GET", "/painel/patch/terceiros/incluir?editar=paloaltonetworks-globalprotect", sessaoAdmin, nil).Body.String(); !strings.Contains(b, `name="remover_antes" value="1" checked`) {
		t.Error("edição sem a opção marcada")
	}
	if b := pagina(h, "GET", "/painel/patch/programa?app=paloaltonetworks-globalprotect", sessaoAdmin, nil).Body.String(); !strings.Contains(b, "atualiza removendo antes a versão instalada") {
		t.Error("página do programa")
	}
	// Desmarcar na edição tira a opção.
	form.Set("editar", "paloaltonetworks-globalprotect")
	form.Del("remover_antes")
	if r := pagina(h, "POST", "/painel/patch/terceiros/incluir", sessaoAdmin, form); r.Code != http.StatusSeeOther {
		t.Fatalf("editar: %d", r.Code)
	}
	for _, a := range live.For("windows") {
		if a.ID == "paloaltonetworks-globalprotect" && a.Install.UninstallFirst {
			t.Error("a opção ficou")
		}
	}
}

// Instalador do winget só por usuário (o Canva, Electron): a busca avisa e já põe o "/S
// /allusers"; os argumentos ficam na regra (e exigem o tipo).
func TestIncluirInstaladorPorUsuario(t *testing.T) {
	base, _ := thirdparty.Default()
	live := thirdparty.NewLive(base)
	descrever := func(_ context.Context, id string) (winget.Description, error) {
		return winget.Description{ID: id, Version: "1.129.0", Name: "Canva", Publisher: "Canva Pty Ltd", Types: []string{"nullsoft"}, UserScope: true}, nil
	}
	h, _, _ := ambientePaginas(t, api.WithThirdPartyManifest(live), api.WithCustomApps(base, descrever, nil), api.WithCompliance(&complianceFalso{}))
	b := pagina(h, "GET", "/painel/patch/terceiros/incluir?winget=Canva.Canva", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, `name="argumentos" value="/S /allusers"`) || !strings.Contains(b, "O instalador do winget é só por usuário") {
		t.Errorf("busca:\n%s", b)
	}
	form := url.Values{"nome": {"Canva"}, "winget": {"Canva.Canva"}, "inventario": {"Canva"}, "modo": {"comeca"}, "tipo": {""},
		"assinatura": {"sim"}, "assinante": {"Canva Pty Ltd"}, "argumentos": {"/S /allusers"}}
	if r := pagina(h, "POST", "/painel/patch/terceiros/incluir", sessaoAdmin, form); r.Code != http.StatusUnprocessableEntity || !strings.Contains(r.Body.String(), "escolha o tipo do instalador") {
		t.Errorf("sem tipo: %d", r.Code)
	}
	form.Set("tipo", "nullsoft")
	if r := pagina(h, "POST", "/painel/patch/terceiros/incluir", sessaoAdmin, form); r.Code != http.StatusSeeOther {
		t.Fatalf("salvar: %d %s", r.Code, r.Body)
	}
	for _, a := range live.For("windows") {
		if a.ID == "canva-canva" && (a.Install.Arguments != "/S /allusers" || a.Install.Type != "nullsoft") {
			t.Errorf("regra: %+v", a.Install)
		}
	}
	if b := pagina(h, "GET", "/painel/patch/terceiros/incluir?editar=canva-canva", sessaoAdmin, nil).Body.String(); !strings.Contains(b, `name="argumentos" value="/S /allusers"`) {
		t.Error("edição sem os argumentos")
	}
}

// A ajuda dos parâmetros: o que o manifesto do winget declara de cada instalador, na busca e
// no Editar (?ajuda=1).
func TestAjudaDosParametrosDoWinget(t *testing.T) {
	base, _ := thirdparty.Default()
	live := thirdparty.NewLive(base)
	m, err := winget.ParseInstaller([]byte(`PackageIdentifier: Flameshot.Flameshot
PackageVersion: 15.0.0
InstallerType: wix
Scope: machine
InstallModes:
- interactive
- silent
InstallerSwitches:
  InstallLocation: INSTALL_ROOT="<INSTALLPATH>"
UpgradeBehavior: uninstallPrevious
ExpectedReturnCodes:
- InstallerReturnCode: 1618
  ReturnResponse: installInProgress
Installers:
- Architecture: x64
  InstallerUrl: https://github.com/flameshot-org/flameshot/releases/download/v15.0.0/Flameshot-15.0.0-win64.msi
  InstallerSha256: 69B8245AB4CB060E70716F493A7B2319E0421649D3E1B194A1B81503F3199A2D
ManifestType: installer
ManifestVersion: 1.28.0
`))
	if err != nil {
		t.Fatal(err)
	}
	descrever := func(_ context.Context, id string) (winget.Description, error) {
		return winget.Description{ID: id, Version: "15.0.0", Name: "Flameshot", Publisher: "flameshot-org", Types: []string{"wix"},
			UninstallPrevious: true, Help: winget.HelpFor(m)}, nil
	}
	h, _, _ := ambientePaginas(t, api.WithThirdPartyManifest(live), api.WithCustomApps(base, descrever, nil), api.WithCompliance(&complianceFalso{}))
	b := pagina(h, "GET", "/painel/patch/terceiros/incluir?winget=Flameshot.Flameshot", sessaoAdmin, nil).Body.String()
	for _, quer := range []string{"Parâmetros do instalador, do manifesto do winget", "<b>wix</b> · x64 · escopo machine · atualização: uninstallPrevious · modos: interactive, silent",
		"<td>InstallLocation</td><td><code>INSTALL_ROOT=&#34;&lt;INSTALLPATH&gt;&#34;</code></td>", "<code>/quiet /norestart</code>",
		"<code>1618</code> outro instalador rodando", "Conhecidos do tipo wix:", "REBOOT=ReallySuppress"} {
		if !strings.Contains(b, quer) {
			t.Errorf("busca: faltou %q", quer)
		}
	}
	form := url.Values{"nome": {"Flameshot"}, "winget": {"Flameshot.Flameshot"}, "inventario": {"Flameshot"}, "modo": {"comeca"}, "tipo": {"wix"},
		"assinatura": {"nao"}, "sem_assinatura": {"teste"}, "remover_antes": {"1"}}
	if r := pagina(h, "POST", "/painel/patch/terceiros/incluir", sessaoAdmin, form); r.Code != http.StatusSeeOther {
		t.Fatalf("salvar: %d %s", r.Code, r.Body)
	}
	ed := "/painel/patch/terceiros/incluir?editar=flameshot-flameshot"
	if b := pagina(h, "GET", ed, sessaoAdmin, nil).Body.String(); !strings.Contains(b, `href="/painel/patch/terceiros/incluir?editar=flameshot-flameshot&amp;ajuda=1">Ver os parâmetros`) ||
		strings.Contains(b, "Parâmetros do instalador, do manifesto") {
		t.Error("edição sem o link")
	}
	if b := pagina(h, "GET", ed+"&ajuda=1", sessaoAdmin, nil).Body.String(); !strings.Contains(b, `<details class="ajuda" open>`) || !strings.Contains(b, "<td>InstallLocation</td>") {
		t.Error("edição com a ajuda")
	}
}

// Incluir do Chocolatey: a busca lê o pacote (nome, tipo, argumentos, nome em Programas e
// Recursos, assinatura) e preenche; salvar grava a regra com o pacote, sem winget; a página do
// programa mostra o Chocolatey como origem e a fonte sem a opção do winget.
func TestIncluirDoChocolatey(t *testing.T) {
	base, _ := thirdparty.Default()
	live := thirdparty.NewLive(base)
	buscar := func(_ context.Context, id string) (api.ChocoDescription, error) {
		if id != "notepadplusplus.install" {
			return api.ChocoDescription{}, errors.New("o pacote não existe no Chocolatey")
		}
		return api.ChocoDescription{Description: choco.Description{ID: id, Title: "Notepad++ (Install)", Version: "8.9.8", Authors: "Don Ho",
			URL: "https://example.com/npp.8.9.8.Installer.x64.exe", URLVar: "url64bit", SHA256: strings.Repeat("a", 64), Type: "nullsoft",
			SilentArgs: "/S", DisplayName: "Notepad++", Prefix: true}, SignerOrg: "Notepad++", SignerSubject: "CN=Notepad++, O=Notepad++"}, nil
	}
	h, _, _ := ambientePaginas(t, api.WithThirdPartyManifest(live), api.WithCustomApps(base, nil, nil), api.WithChocoSearch(buscar),
		api.WithCompliance(&complianceFalso{}))
	const pagina_ = "/painel/patch/terceiros/incluir"
	b := pagina(h, "GET", pagina_+"?chocolatey=1", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, `Do Chocolatey</a>`) || !strings.Contains(b, `name="pacote"`) || !strings.Contains(b, `name="origem" value="chocolatey"`) {
		t.Error("a aba do Chocolatey")
	}
	b = pagina(h, "GET", pagina_+"?pacote=notepadplusplus.install", sessaoAdmin, nil).Body.String()
	for _, quer := range []string{"Achado no Chocolatey: <b>Notepad&#43;&#43; (Install)</b> 8.9.8", `name="chocolatey" value="notepadplusplus.install"`,
		`name="inventario" value="Notepad&#43;&#43;"`, `<option value="comeca" selected>`, `<option value="nullsoft" selected>`,
		`name="assinante" value="Notepad&#43;&#43;"`, "silentArgs do script: <code>/S</code>"} {
		if !strings.Contains(b, quer) {
			t.Errorf("busca: falta %q", quer)
		}
	}
	if b := pagina(h, "GET", pagina_+"?pacote=nada", sessaoAdmin, nil).Body.String(); !strings.Contains(b, "não existe no Chocolatey") {
		t.Error("pacote que não existe")
	}
	form := url.Values{"origem": {"chocolatey"}, "nome": {"Notepad++"}, "chocolatey": {"notepadplusplus.install"}, "inventario": {"Notepad++"},
		"modo": {"comeca"}, "tipo": {"nullsoft"}, "assinatura": {"sim"}, "assinante": {"Notepad++"}, "winget": {"Ignorado.Aqui"}}
	r := pagina(h, "POST", pagina_, sessaoAdmin, form)
	if r.Code != http.StatusSeeOther || !strings.Contains(local(r), "salvo=notepadplusplus-install") || !strings.Contains(local(r), "chocolatey=1") {
		t.Fatalf("salvar: %d %s %s", r.Code, local(r), r.Body)
	}
	var rule thirdparty.App
	for _, a := range live.For("windows") {
		if a.ID == "notepadplusplus-install" {
			rule = a
		}
	}
	if !rule.FromChocolatey() || rule.Install.Winget != "" || rule.Source.Type != thirdparty.SourceChocolatey {
		t.Fatalf("regra: %+v", rule)
	}
	b = pagina(h, "GET", "/painel/patch/programa?app=notepadplusplus-install", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, "Chocolatey <code>notepadplusplus.install</code>") || strings.Contains(b, `name="fonte" value="winget"`) ||
		!strings.Contains(b, `name="fonte" value="chocolatey" checked`) {
		t.Error("a página do programa do Chocolatey")
	}
	// A edição mantém a aba; não vira programa do winget.
	if b := pagina(h, "GET", pagina_+"?editar=notepadplusplus-install", sessaoAdmin, nil).Body.String(); !strings.Contains(b, `name="chocolatey" value="notepadplusplus.install"`) {
		t.Error("edição")
	}
	form.Set("editar", "notepadplusplus-install")
	form.Set("origem", "")
	form.Set("winget", "Notepad.Plus")
	if r := pagina(h, "POST", pagina_, sessaoAdmin, form); r.Code != http.StatusUnprocessableEntity {
		t.Errorf("trocar de origem na edição: %d", r.Code)
	}
	// O exe genérico sem os argumentos: recusado.
	form = url.Values{"origem": {"chocolatey"}, "nome": {"X"}, "chocolatey": {"xis"}, "inventario": {"X"}, "tipo": {"exe"}, "assinatura": {"sim"}, "assinante": {"X"}}
	if r := pagina(h, "POST", pagina_, sessaoAdmin, form); r.Code != http.StatusUnprocessableEntity || !strings.Contains(r.Body.String(), "exige arguments") {
		t.Errorf("exe sem argumentos: %d", r.Code)
	}
	// O que o manifesto já tem: aponta para a Fonte do instalador.
	form = url.Values{"origem": {"chocolatey"}, "nome": {"7-Zip"}, "chocolatey": {"7zip"}, "inventario": {"7-Zip"}, "tipo": {"msi"}, "assinatura": {"sim"}, "assinante": {"X"}}
	if r := pagina(h, "POST", pagina_, sessaoAdmin, form); r.Code != http.StatusUnprocessableEntity || !strings.Contains(r.Body.String(), "Fonte do instalador") {
		t.Errorf("do manifesto: %d", r.Code)
	}
}

// O instalador só por usuário sem os argumentos do Electron (o exe do fabricante): a busca
// marca "instala no perfil do usuário"; salvar guarda a regra por usuário, e a página do programa
// diz que só a Central instala.
func TestIncluirProgramaPorUsuario(t *testing.T) {
	base, _ := thirdparty.Default()
	live := thirdparty.NewLive(base)
	descrever := func(_ context.Context, id string) (winget.Description, error) {
		return winget.Description{ID: id, Version: "2.0", Name: "Ferramenta", Publisher: "Exemplo", Types: []string{"inno"}, UserScope: true}, nil
	}
	h, _, _ := ambientePaginas(t, api.WithThirdPartyManifest(live), api.WithCustomApps(base, descrever, nil), api.WithCompliance(&complianceFalso{}))
	b := pagina(h, "GET", "/painel/patch/terceiros/incluir?winget=Exemplo.Ferramenta", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, `name="por_usuario" value="1" checked`) || strings.Contains(b, `value="/S /allusers"`) {
		t.Errorf("busca:\n%s", b)
	}
	form := url.Values{"nome": {"Ferramenta"}, "winget": {"Exemplo.Ferramenta"}, "inventario": {"Ferramenta"}, "modo": {"comeca"}, "tipo": {"inno"},
		"assinatura": {"sim"}, "assinante": {"Exemplo"}, "por_usuario": {"1"}}
	if r := pagina(h, "POST", "/painel/patch/terceiros/incluir", sessaoAdmin, form); r.Code != http.StatusSeeOther {
		t.Fatalf("salvar: %d %s", r.Code, r.Body)
	}
	ok := false
	for _, a := range live.For("windows") {
		ok = ok || a.ID == "exemplo-ferramenta" && a.PerUser()
	}
	if !ok {
		t.Error("a regra não ficou por usuário")
	}
	if b := pagina(h, "GET", "/painel/patch/programa?app=exemplo-ferramenta", sessaoAdmin, nil).Body.String(); !strings.Contains(b, "instala no perfil de cada usuário, só pela Central de software") {
		t.Error("página do programa")
	}
	// Com "remover antes": a regra não passa.
	form.Set("remover_antes", "1")
	form.Set("editar", "exemplo-ferramenta")
	if r := pagina(h, "POST", "/painel/patch/terceiros/incluir", sessaoAdmin, form); r.Code != http.StatusUnprocessableEntity {
		t.Errorf("com remover antes: %d", r.Code)
	}
}
