package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/protocol"
)

// Inventário › Programas instalados: por sistema, junto por programa e fabricante, com os
// componentes do sistema escondidos; a página do programa mostra as versões e as máquinas.
func TestProgramasInstalados(t *testing.T) {
	h, st, _ := ambientePaginas(t)
	ctx := context.Background()
	win := func(id, host string, sw ...protocol.Software) {
		t.Helper()
		if _, err := st.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: "h-" + id,
			OS: protocol.OSInfo{Family: "windows", ID: "windows", Name: "Windows 11 Pro", Arch: "amd64", Hostname: host}, Software: sw}); err != nil {
			t.Fatal(err)
		}
	}
	lo := func(v, un string) protocol.Software {
		return protocol.Software{Name: "LibreOffice " + v, Version: v, Publisher: "The Document Foundation", Scope: "machine", Uninstall: un}
	}
	win("92fc3b93-0003", "PC-A", lo("25.2.1.1", "msi"), protocol.Software{Name: "Edge Update", Version: "1.3", Publisher: "Microsoft", Scope: "machine", SystemComponent: true})
	win("92fc3b93-0004", "PC-B", lo("26.8.3.1", "msi"), protocol.Software{Name: "Ferramenta", Version: "2.0", Publisher: "X", Scope: "user"})

	r := pagina(h, "GET", "/painel/inventario/programas", sessaoAdmin, nil)
	b := r.Body.String()
	if r.Code != http.StatusOK || !strings.Contains(b, "Programas instalados</a>") || !strings.Contains(b, ">LibreOffice</a>") ||
		!strings.Contains(b, "<td>The Document Foundation</td><td class=\"num\">2</td>") || strings.Contains(b, ">Edge Update</a>") ||
		!strings.Contains(b, "1 componente(s) do sistema") || !strings.Contains(b, `class="ativa" aria-current="page">Windows <span class="muted">2</span>`) {
		t.Fatalf("lista: %d\n%s", r.Code, b)
	}
	if !strings.Contains(b, `<td class="muted">silenciosa</td>`) || !strings.Contains(b, `<td class="muted">sem desinstalação silenciosa</td>`) {
		t.Error("remoção na lista")
	}
	if b := pagina(h, "GET", "/painel/inventario/programas?sistema=1", sessaoAdmin, nil).Body.String(); !strings.Contains(b, ">Edge Update</a> <span class=\"muted\">(sistema)</span>") {
		t.Error("com os componentes do sistema")
	}
	if b := pagina(h, "GET", "/painel/inventario/programas?q=ferr", sessaoAdmin, nil).Body.String(); !strings.Contains(b, ">Ferramenta</a>") || strings.Contains(b, ">LibreOffice</a>") {
		t.Error("busca")
	}
	if b := pagina(h, "GET", "/painel/inventario/programas?so=linux", sessaoAdmin, nil).Body.String(); !strings.Contains(b, "Nenhum programa no Linux") {
		t.Error("linux vazio")
	}
	// A página do programa: as duas versões e as duas máquinas.
	b = pagina(h, "GET", "/painel/inventario/programa?so=windows&nome=libreoffice&fab=The+Document+Foundation", sessaoAdmin, nil).Body.String()
	for _, quer := range []string{"<h1>LibreOffice</h1>", "<code>26.8.3.1</code></td><td class=\"num\">1</td>", ">PC-A</a>", ">PC-B</a>",
		"<code>LibreOffice 25.2.1.1</code>", "MSI (msiexec /x)"} {
		if !strings.Contains(b, quer) {
			t.Errorf("programa: faltou %q", quer)
		}
	}
	if r := pagina(h, "GET", "/painel/inventario/programa?so=windows&nome=nada&fab=", sessaoAdmin, nil); r.Code != http.StatusNotFound {
		t.Errorf("programa que não existe: %d", r.Code)
	}
	// O Inventário tem as duas abas.
	if b := pagina(h, "GET", "/painel/inventario", sessaoAdmin, nil).Body.String(); !strings.Contains(b, `href="/painel/inventario/programas">Programas instalados</a>`) {
		t.Error("aba no Inventário")
	}
}
