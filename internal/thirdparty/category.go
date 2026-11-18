package thirdparty

import (
	"strings"

	"github.com/tasantiago/patchd/internal/protocol"
)

// A categoria padrão de um programa na loja da Central (protocol.StoreCategories), quando a
// oferta não escolhe outra: a dos programas do catálogo do patchd e, para os incluídos pelo
// painel, a que o nome do pacote sugere (zoom, vlc, gimp...). Sem pista: "outros".

var storeByID = map[string]string{
	"google-chrome": "navegadores", "mozilla-firefox": "navegadores", "mozilla-firefox-esr": "navegadores",
	"adobe-acrobat": "escritorio", "libreoffice": "escritorio",
	"7zip": "utilitarios", "notepad-plus-plus": "utilitarios",
	"vscode": "desenvolvimento", "powershell-7": "desenvolvimento",
}

// As pistas pelo identificador (o do catálogo ou o que o painel gerou do nome), na ordem.
var storeHints = []struct {
	category string
	words    []string
}{
	{"navegadores", []string{"chrome", "firefox", "edge", "brave", "opera", "vivaldi", "browser", "navegador"}},
	{"comunicacao", []string{"zoom", "teams", "slack", "skype", "webex", "whatsapp", "telegram", "thunderbird", "outlook", "jitsi", "discord"}},
	{"escritorio", []string{"office", "word", "excel", "pdf", "acrobat", "foxit", "libreoffice", "openoffice", "onlyoffice", "sumatra"}},
	{"multimidia", []string{"vlc", "gimp", "inkscape", "audacity", "obs", "spotify", "paint", "krita", "irfanview", "handbrake", "media", "video", "audio", "player"}},
	{"desenvolvimento", []string{"python", "node", "nodejs", "temurin", "jdk", "java", "git", "vscode", "code", "putty", "winscp", "dbeaver", "postman", "powershell", "sql"}},
	{"utilitarios", []string{"7zip", "zip", "rar", "notepad", "jre", "runtime", "redist", "vcredist", "keepass", "greenshot", "everything", "treesize", "anydesk", "teamviewer"}},
	{"sistemas", []string{"irpf", "receita", "sistema", "esocial", "sped", "certificado", "token", "assinador", "pje"}},
}

// DefaultStoreCategory: a categoria do programa na loja quando a oferta não escolhe.
func DefaultStoreCategory(appID string) string {
	if c, ok := storeByID[appID]; ok {
		return c
	}
	id := strings.ToLower(appID)
	// O runtime do Java é utilitário; o JDK, desenvolvimento (a pista do "jre" vem antes).
	if strings.HasPrefix(id, "temurin-") && strings.HasSuffix(id, "-jre") {
		return "utilitarios"
	}
	parts := strings.FieldsFunc(id, func(r rune) bool { return r == '-' || r == '.' || r == '_' })
	for _, h := range storeHints {
		for _, w := range h.words {
			for _, p := range parts {
				if p == w || len(w) >= 5 && strings.HasPrefix(p, w) {
					return h.category
				}
			}
		}
	}
	return protocol.StoreOther
}
