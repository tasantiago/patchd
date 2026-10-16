package api_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
)

// Aula 17.1: Configuração › Autoatendimento grava as ofertas (auditadas) e mostra a prévia
// da loja de uma máquina para uma lista de grupos.
func TestAbaAutoatendimento(t *testing.T) {
	m, _ := thirdparty.Default()
	h, st, _ := ambientePaginas(t, api.WithThirdPartyManifest(&m))
	ctx := context.Background()
	const pc = "c0ffee00-0009"
	_, _ = st.SaveInventory(ctx, pc, protocol.InventoryReport{SchemaVersion: 1, Hash: "w",
		OS:       protocol.OSInfo{ID: "windows", Name: "Windows 11 Pro", Arch: "amd64", Hostname: "pc-loja"},
		Software: []protocol.Software{{Name: "7-Zip 26.01 (x64)", Version: "26.01", Publisher: "Igor Pavlov", Scope: "machine"}}})
	for _, p := range []store.ThirdPartyPackage{
		{AppID: "7zip", Version: "26.04", Type: "exe", Architecture: "x64", SHA256: strings.Repeat("a", 64), Size: 1},
		{AppID: "notepad-plus-plus", Version: "8.9.8.1", Type: "nullsoft", Architecture: "x64", SHA256: strings.Repeat("b", 64), Size: 1},
	} {
		if _, err := st.SaveThirdPartyPackage(ctx, p); err != nil {
			t.Fatal(err)
		}
		_, _ = st.ApproveThirdParty(ctx, store.ThirdPartyApproval{AppID: p.AppID, Version: p.Version, ApprovedBy: "thyago", ApprovedAt: time.Now().UTC()})
	}
	aba := "/painel/configuracao?aba=autoatendimento"
	if r := pagina(h, "GET", aba, sessaoTeste, nil); r.Code != http.StatusForbidden {
		t.Errorf("leitura: %d", r.Code)
	}
	b := pagina(h, "GET", aba, sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, `>Autoatendimento</a>`) || !strings.Contains(b, `7-Zip <span class="muted"><code>7zip</code></span></td><td><code>26.04</code></td>`) ||
		!strings.Contains(b, "fora da loja") || strings.Contains(b, "adobe-acrobat") {
		t.Errorf("aba:\n%s", b)
	}
	salvar := func(f url.Values) string {
		return local(pagina(h, "POST", "/painel/configuracao/autoatendimento", sessaoAdmin, f))
	}
	if loc := salvar(url.Values{"app": {"adobe-acrobat"}, "atualizar": {"1"}}); !strings.HasSuffix(loc, "erro=programa") {
		t.Errorf("sem install: %s", loc)
	}
	if loc := salvar(url.Values{"app": {"7zip"}, "atualizar": {"1"}, "grupos": {"GG-TI, grupo*)(cn=*"}}); !strings.Contains(loc, "erro=grupos") {
		t.Errorf("grupo ruim: %s", loc)
	}
	if loc := salvar(url.Values{"app": {"7zip"}, "atualizar": {"1"}}); !strings.HasSuffix(loc, "salvo=7zip") {
		t.Errorf("7-Zip: %s", loc)
	}
	if loc := salvar(url.Values{"app": {"notepad-plus-plus"}, "instalar": {"1"}, "grupos": {"GG-TI, gg-ti"}}); !strings.HasSuffix(loc, "salvo=notepad-plus-plus") {
		t.Errorf("Notepad++: %s", loc)
	}
	if l, _ := st.SelfServiceOffers(ctx); len(l) != 2 || l[1].AppID != "notepad-plus-plus" || strings.Join(l[1].Groups, ",") != "GG-TI,gg-ti" || l[0].UpdatedBy != "thyago" {
		t.Errorf("ofertas: %+v", l)
	}
	if ev, _ := st.AuditEvents(ctx, store.AuditFilter{Action: "autoatendimento."}); len(ev) != 2 || ev[0].Detail != "atualizar não · instalar sim · 2 grupo(s)" {
		t.Errorf("auditoria: %+v", ev)
	}

	// Prévia: sem o grupo, só a atualização do 7-Zip; com o grupo, também instalar o Notepad++.
	b = pagina(h, "GET", aba+"&maquina="+pc, sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, `<option value="`+pc+`" selected>pc-loja</option>`) ||
		!strings.Contains(b, "<td>7-Zip</td><td><code>26.01</code></td><td>Atualizar</td><td><code>26.04</code></td>") || strings.Contains(b, "<td>Notepad&#43;&#43;</td>") {
		t.Errorf("prévia sem grupo:\n%s", b)
	}
	b = pagina(h, "GET", aba+"&maquina="+pc+"&grupos=gg-ti", sessaoAdmin, nil).Body.String()
	if !strings.Contains(b, "<td>Notepad&#43;&#43;</td><td>—</td><td>Instalar</td><td><code>8.9.8.1</code></td>") {
		t.Errorf("prévia com grupo:\n%s", b)
	}
	// Desmarcar os dois tira da loja.
	if loc := salvar(url.Values{"app": {"7zip"}}); !strings.HasSuffix(loc, "removido=7zip") {
		t.Errorf("remover: %s", loc)
	}
	if l, _ := st.SelfServiceOffers(ctx); len(l) != 1 {
		t.Errorf("depois de remover: %+v", l)
	}
}
