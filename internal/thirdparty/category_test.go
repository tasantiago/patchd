package thirdparty

import (
	"testing"

	"github.com/tasantiago/patchd/internal/protocol"
)

// A categoria padrão na loja: a do catálogo, a que o nome sugere, ou "outros"; toda regra
// com instalação do catálogo cai numa categoria da lista.
func TestCategoriaPadrao(t *testing.T) {
	for id, quer := range map[string]string{
		"google-chrome": "navegadores", "libreoffice": "escritorio", "7zip": "utilitarios", "vscode": "desenvolvimento",
		"python-3.13": "desenvolvimento", "nodejs-22": "desenvolvimento", "temurin-21-jdk": "desenvolvimento",
		"temurin-21-jre": "utilitarios", "zoom-zoom": "comunicacao", "videolan-vlc": "multimidia", "gimp": "multimidia",
		"irpf-2026": "sistemas", "microsoft-teams": "comunicacao", "programa-interno": "outros", "codecs": "outros",
	} {
		if got := DefaultStoreCategory(id); got != quer {
			t.Errorf("%s: %s, quer %s", id, got, quer)
		}
	}
	m, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range m.For("windows") {
		if a.Install != nil && !protocol.ValidStoreCategory(DefaultStoreCategory(a.ID)) {
			t.Errorf("%s: categoria fora da lista", a.ID)
		}
	}
}
