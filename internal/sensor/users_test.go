package sensor

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
)

func escreve(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

const (
	idBloqueador = "cjpalhdlnbpafiamejdnhcphjbkeiagm"
	idTradutor   = "aapbdbdomjkkjkaonfhkkikfgjllcleb"
	idSolta      = "abcdefghijklmnopabcdefghijklmnop"
	idComponente = "nmmhkkegccagdldgiimedpiccmgmieda"
)

// parque monta dois usuários: ana (Chrome com dois perfis e Firefox) e bruno (Edge).
func parque(t *testing.T) (Sensors, string) {
	t.Helper()
	root := t.TempDir()
	ana, bruno, vazio := filepath.Join(root, "ana"), filepath.Join(root, "bruno"), filepath.Join(root, "carla")
	chrome := filepath.Join(ana, ".config", "google-chrome")
	// Perfil Default: o bloqueador (nome por idioma, acesso a todos os sites) e o tradutor
	// (desativado, de fora da loja).
	escreve(t, filepath.Join(chrome, "Default", "Extensions", idBloqueador, "1.60.0_0", "manifest.json"),
		`{"name":"__MSG_extName__","version":"1.60.0","default_locale":"en","manifest_version":3,"host_permissions":["<all_urls>"]}`)
	escreve(t, filepath.Join(chrome, "Default", "Extensions", idBloqueador, "1.60.0_0", "_locales", "en", "messages.json"),
		`{"extname":{"message":"Bloqueador Origem"}}`)
	escreve(t, filepath.Join(chrome, "Default", "Extensions", idTradutor, "2.0_0", "manifest.json"),
		`{"name":"Tradutor","version":"2.0","permissions":["storage", {"fileSystem":["write"]}]}`)
	// O componente do navegador não é extensão de ninguém.
	escreve(t, filepath.Join(chrome, "Default", "Extensions", idComponente, "1.0_0", "manifest.json"), `{"name":"Pagamentos","version":"1.0"}`)
	escreve(t, filepath.Join(chrome, "Default", "Extensions", "Temp", "lixo.txt"), "x")
	solta := filepath.Join(ana, "Downloads", "ext-solta")
	escreve(t, filepath.Join(solta, "manifest.json"), `{"name":"Ladra de Senhas","version":"0.1","content_scripts":[{"matches":["*://*/*"]}]}`)
	escreve(t, filepath.Join(chrome, "Default", "Preferences"), `{"extensions":{"settings":{}},"profile":{"name":"Pessoa 1"}}`)
	prefs := `{"extensions":{"settings":{
		"` + idBloqueador + `":{"location":1,"from_webstore":true,"state":1,"path":"` + idBloqueador + `/1.60.0_0"},
		"` + idTradutor + `":{"location":1,"from_webstore":false,"disable_reasons":[1]},
		"` + idComponente + `":{"location":5},
		"` + idSolta + `":{"location":4,"path":` + jsonText(solta) + `},
		"mhjfbmdgcfjbbpaeojofohoefgiehjai":{"location":5,"path":"/opt/google/chrome/resources/pdf"}}}}`
	escreve(t, filepath.Join(chrome, "Default", "Secure Preferences"), prefs)
	// Segundo perfil do mesmo usuário: só o bloqueador, noutra versão.
	escreve(t, filepath.Join(chrome, "Profile 1", "Extensions", idBloqueador, "1.59.0_0", "manifest.json"), `{"name":"Bloqueador Origem","version":"1.59.0"}`)
	escreve(t, filepath.Join(chrome, "Profile 1", "Preferences"), `{}`)
	// Pastas do navegador que não são perfil.
	escreve(t, filepath.Join(chrome, "Crashpad", "settings.dat"), "x")
	// Firefox: uma extensão de verdade, uma embutida, um tema.
	escreve(t, filepath.Join(ana, ".mozilla", "firefox", "abc.default-release", "extensions.json"), `{"schemaVersion":37,"addons":[
		{"id":"uBlock0@raymondhill.net","version":"1.61.2","type":"extension","location":"app-profile","active":true,"userDisabled":false,
		 "signedState":2,"defaultLocale":{"name":"uBlock Origin"},"userPermissions":{"origins":["<all_urls>"],"permissions":["tabs"]}},
		{"id":"sem-assinatura@exemplo","version":"0.3","type":"extension","location":"app-profile","active":false,"userDisabled":true,
		 "signedState":0,"defaultLocale":{"name":"Baixador"}},
		{"id":"formautofill@mozilla.org","version":"1.0","type":"extension","location":"app-builtin","active":true},
		{"id":"default-theme@mozilla.org","version":"1.3","type":"theme","location":"app-builtin","active":true}]}`)
	// bruno: Edge com uma extensão; carla não tem navegador.
	escreve(t, filepath.Join(bruno, ".config", "microsoft-edge", "Default", "Extensions", idBloqueador, "1.60.0_0", "manifest.json"),
		`{"name":"Bloqueador Origem","version":"1.60.0"}`)
	escreve(t, filepath.Join(bruno, "Downloads", "instalador.exe"), "MZ")
	escreve(t, filepath.Join(ana, "Downloads", "instalador.exe"), "MZ-outro")
	if err := os.MkdirAll(vazio, 0o750); err != nil {
		t.Fatal(err)
	}
	s := Sensors{OS: "linux", Profiles: func() ([]Profile, error) {
		return []Profile{{User: "ana", Home: ana}, {User: "bruno", Home: bruno}, {User: "carla", Home: vazio},
			{User: "sumiu", Home: filepath.Join(root, "nao-existe")}}, nil
	}}
	return s, root
}

func jsonText(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func acha(items []protocol.AnswerItem, user, source, where, key string) (protocol.AnswerItem, bool) {
	for _, it := range items {
		if it.User == user && it.Source == source && it.Where == where && it.Key == key {
			return it, true
		}
	}
	return protocol.AnswerItem{}, false
}

func TestExtensoesDeTodosOsUsuarios(t *testing.T) {
	s, _ := parque(t)
	a := s.Answer(context.Background(), jobs.QueryParams{Sensor: jobs.SensorExtensions})
	if a.Value != "com extensões" || !strings.Contains(a.Extra, "7 extensão(ões) de 2 usuário(s); 3 perfil(is) de usuário na máquina, 2 com navegador") {
		t.Fatalf("resposta: %q / %q\n%+v", a.Value, a.Extra, a.Items)
	}
	if it, ok := acha(a.Items, "ana", "Chrome", "Default", idBloqueador); !ok || it.Name != "Bloqueador Origem" || it.Version != "1.60.0" ||
		it.Note != "acesso a todos os sites" {
		t.Errorf("bloqueador do Chrome: %+v", it)
	}
	if it, ok := acha(a.Items, "ana", "Chrome", "Default", idTradutor); !ok || it.Note != "desativada; fora da loja" {
		t.Errorf("tradutor: %+v", it)
	}
	if it, ok := acha(a.Items, "ana", "Chrome", "Profile 1", idBloqueador); !ok || it.Version != "1.59.0" || it.Note != "" {
		t.Errorf("segundo perfil: %+v", it)
	}
	// A carregada de uma pasta (modo de desenvolvedor) só existe nas preferências.
	if it, ok := acha(a.Items, "ana", "Chrome", "Default", idSolta); !ok || it.Name != "Ladra de Senhas" || it.Version != "0.1" ||
		!strings.Contains(it.Note, "sem compactação (modo de desenvolvedor)") || !strings.Contains(it.Note, "acesso a todos os sites") ||
		!strings.Contains(it.Note, "ext-solta") {
		t.Errorf("extensão solta: %+v", it)
	}
	if it, ok := acha(a.Items, "ana", "Firefox", "abc.default-release", "uBlock0@raymondhill.net"); !ok || it.Name != "uBlock Origin" ||
		it.Version != "1.61.2" || it.Note != "acesso a todos os sites" {
		t.Errorf("Firefox: %+v", it)
	}
	if it, ok := acha(a.Items, "ana", "Firefox", "abc.default-release", "sem-assinatura@exemplo"); !ok ||
		it.Note != "desativada; sem assinatura da Mozilla" {
		t.Errorf("Firefox sem assinatura: %+v", it)
	}
	if _, ok := acha(a.Items, "bruno", "Edge", "Default", idBloqueador); !ok {
		t.Errorf("Edge do bruno: %+v", a.Items)
	}
	for _, it := range a.Items {
		if it.Key == idComponente || strings.Contains(it.Key, "mozilla.org") || it.Key == "mhjfbmdgcfjbbpaeojofohoefgiehjai" {
			t.Errorf("parte do navegador na lista: %+v", it)
		}
	}

	// Com filtro: pelo nome ou pelo ID, sem diferença de maiúsculas.
	a = s.Answer(context.Background(), jobs.QueryParams{Sensor: jobs.SensorExtensions, Arg: "BLOQUEADOR"})
	if a.Value != "encontrada" || len(a.Items) != 3 {
		t.Errorf("filtro pelo nome: %q, %d itens", a.Value, len(a.Items))
	}
	a = s.Answer(context.Background(), jobs.QueryParams{Sensor: jobs.SensorExtensions, Arg: idSolta})
	if a.Value != "encontrada" || len(a.Items) != 1 {
		t.Errorf("filtro pelo ID: %q, %d itens", a.Value, len(a.Items))
	}
	a = s.Answer(context.Background(), jobs.QueryParams{Sensor: jobs.SensorExtensions, Arg: "nao-tem"})
	if a.Value != "não encontrada" || len(a.Items) != 0 {
		t.Errorf("filtro sem achado: %q, %d itens", a.Value, len(a.Items))
	}

	// Sem a lista de usuários, o sensor não se aplica.
	if got := (Sensors{}).Answer(context.Background(), jobs.QueryParams{Sensor: jobs.SensorExtensions}); got.Value != NotApplicable {
		t.Errorf("sem perfis: %q", got.Value)
	}
}

// Nada do que está na pasta do usuário é seguido: um link plantado no lugar de uma pasta ou
// de um arquivo não leva o agente (SYSTEM ou root) a ler outro lugar.
func TestExtensoesNaoSeguemLink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("criar link simbólico no Windows pede privilégio")
	}
	root := t.TempDir()
	fora := filepath.Join(root, "fora")
	escreve(t, filepath.Join(fora, "Default", "Extensions", idBloqueador, "1_0", "manifest.json"), `{"name":"De Fora","version":"1"}`)
	escreve(t, filepath.Join(fora, "segredo.json"), `{"name":"SEGREDO","version":"9"}`)
	home := filepath.Join(root, "ana")
	// 1) a pasta de dados do navegador é um link;
	if err := os.MkdirAll(filepath.Join(home, ".config"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fora, filepath.Join(home, ".config", "google-chrome")); err != nil {
		t.Fatal(err)
	}
	// 2) o manifesto de uma extensão é um link;
	edge := filepath.Join(home, ".config", "microsoft-edge", "Default")
	if err := os.MkdirAll(filepath.Join(edge, "Extensions", idTradutor, "1_0"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(fora, "segredo.json"), filepath.Join(edge, "Extensions", idTradutor, "1_0", "manifest.json")); err != nil {
		t.Fatal(err)
	}
	// 3) a pasta de uma extensão é um link;
	if err := os.Symlink(filepath.Join(fora, "Default", "Extensions", idBloqueador), filepath.Join(edge, "Extensions", idBloqueador)); err != nil {
		t.Fatal(err)
	}
	// 4) a extensão "sem compactação" aponta para uma pasta que é link, e outra para a rede.
	if err := os.Symlink(filepath.Join(fora, "Default", "Extensions", idBloqueador, "1_0"), filepath.Join(home, "atalho")); err != nil {
		t.Fatal(err)
	}
	escreve(t, filepath.Join(edge, "Preferences"), `{"extensions":{"settings":{
		"`+idSolta+`":{"location":4,"path":`+jsonText(filepath.Join(home, "atalho"))+`},
		"`+idComponente+`":{"location":4,"path":"//servidor/pasta/ext"}}}}`)
	s := Sensors{OS: "linux", Profiles: func() ([]Profile, error) { return []Profile{{User: "ana", Home: home}}, nil }}
	a := s.Answer(context.Background(), jobs.QueryParams{Sensor: jobs.SensorExtensions})
	for _, it := range a.Items {
		if strings.Contains(it.Name, "SEGREDO") || strings.Contains(it.Name, "De Fora") {
			t.Errorf("o agente seguiu um link: %+v", it)
		}
	}
	// As duas "sem compactação" aparecem (estão nas preferências), mas só com o caminho.
	if len(a.Items) != 2 {
		t.Fatalf("itens: %+v", a.Items)
	}
	for _, it := range a.Items {
		if !strings.HasPrefix(it.Name, "(pasta) ") || it.Version != "" {
			t.Errorf("extensão solta lida através de link ou da rede: %+v", it)
		}
	}
	if !strings.Contains(a.Extra, "1 pasta(s) sem leitura") {
		t.Errorf("a pasta que é link devia contar como não lida: %q", a.Extra)
	}
}

func TestArquivoNaPastaDeCadaUsuario(t *testing.T) {
	s, root := parque(t)
	ctx := context.Background()
	a := s.Answer(ctx, jobs.QueryParams{Sensor: jobs.SensorUserFile, Arg: "~/Downloads/instalador.exe"})
	if a.Value != "encontrado" || a.Extra != "em 2 de 3 usuário(s)" || len(a.Items) != 2 {
		t.Fatalf("resposta: %q / %q / %+v", a.Value, a.Extra, a.Items)
	}
	it := a.Items[1]
	if it.User != "bruno" || it.Name != "existe" || it.Key != "existe" || it.Where != filepath.Join(root, "bruno", "Downloads", "instalador.exe") ||
		!strings.Contains(it.Note, "2 bytes") || !strings.Contains(it.Note, "sha256 ") {
		t.Errorf("item: %+v", it)
	}
	// A barra do Windows e o %USERPROFILE% valem igual.
	if b := s.Answer(ctx, jobs.QueryParams{Sensor: jobs.SensorUserFile, Arg: `%USERPROFILE%\Downloads\instalador.exe`}); len(b.Items) != 2 {
		t.Errorf("%%USERPROFILE%%: %+v", b)
	}
	// Uma pasta responde que é pasta.
	if b := s.Answer(ctx, jobs.QueryParams{Sensor: jobs.SensorUserFile, Arg: "~/Downloads/ext-solta"}); len(b.Items) != 1 || b.Items[0].Name != "é uma pasta" {
		t.Errorf("pasta: %+v", b)
	}
	if b := s.Answer(ctx, jobs.QueryParams{Sensor: jobs.SensorUserFile, Arg: "~/nao/existe.txt"}); b.Value != "não encontrado" || len(b.Items) != 0 {
		t.Errorf("não existe: %+v", b)
	}
	// %APPDATA% só existe no Windows; lá, cai em AppData\Roaming do perfil.
	if b := s.Answer(ctx, jobs.QueryParams{Sensor: jobs.SensorUserFile, Arg: `%APPDATA%\App\cfg.ini`}); b.Value != NotApplicable {
		t.Errorf("%%APPDATA%% fora do Windows: %+v", b)
	}
	escreve(t, filepath.Join(root, "ana", "AppData", "Roaming", "App", "cfg.ini"), "a=1")
	escreve(t, filepath.Join(root, "ana", "AppData", "Local", "App", "cache.db"), "x")
	win := s
	win.OS = "windows"
	win.FileVersion = func(string) string { return "3.2.1.0" }
	if b := win.Answer(ctx, jobs.QueryParams{Sensor: jobs.SensorUserFile, Arg: `%APPDATA%\App\cfg.ini`}); len(b.Items) != 1 || b.Items[0].Name != "3.2.1.0" {
		t.Errorf("%%APPDATA%% no Windows: %+v", b)
	}
	if b := win.Answer(ctx, jobs.QueryParams{Sensor: jobs.SensorUserFile, Arg: `%LocalAppData%\App\cache.db`}); len(b.Items) != 1 {
		t.Errorf("%%LOCALAPPDATA%% no Windows: %+v", b)
	}
	if runtime.GOOS != "windows" {
		// Um link no caminho não é seguido: o item diz que havia um link, sem ler o destino.
		if err := os.Symlink(filepath.Join(root, "bruno", "Downloads"), filepath.Join(root, "carla", "Downloads")); err != nil {
			t.Fatal(err)
		}
		b := s.Answer(ctx, jobs.QueryParams{Sensor: jobs.SensorUserFile, Arg: "~/Downloads/instalador.exe"})
		it, ok := acha(b.Items, "carla", "", filepath.Join(root, "carla", "Downloads"), errNotPlain.Error())
		if !ok || it.Note != "" {
			t.Errorf("link no caminho: %+v", b.Items)
		}
	}
}
