package api_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestExportacaoCSV(t *testing.T) {
	h, _, log := ambientePaginas(t)

	r := pagina(h, "GET", "/painel/frota.csv", sessaoTeste, nil)
	if r.Code != http.StatusOK || r.Header().Get("Content-Type") != "text/csv; charset=utf-8" ||
		!strings.HasPrefix(r.Header().Get("Content-Disposition"), `attachment; filename="patchd-frota-`) || r.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("frota: %d %v", r.Code, r.Header())
	}
	b := r.Body.String()
	if !strings.HasPrefix(b, "\xef\xbb\xbfmaquina;nome;sistema;estado") || strings.Count(b, "\r\n") != 4 ||
		!strings.Contains(b, "60ef97e4-0001;vm-ubuntu;Ubuntu 26.04;faltando;1;pacotes;1;") {
		t.Errorf("conteúdo:\n%s", b)
	}
	// O nome hostil sai como texto, sem fórmula nem HTML de verdade (é um CSV).
	if !strings.Contains(b, "<script>alert(1)</script>") {
		t.Error("o CSV leva o valor como texto")
	}

	// Com o filtro da tela.
	r = pagina(h, "GET", "/painel/frota.csv?estado=faltando", sessaoTeste, nil)
	if b := r.Body.String(); strings.Count(b, "\r\n") != 2 || strings.Contains(b, "92fc3b93") ||
		!strings.Contains(r.Header().Get("Content-Disposition"), "patchd-frota-faltando-") {
		t.Errorf("filtro:\n%s\n%v", b, r.Header())
	}

	// Pendências de uma máquina.
	r = pagina(h, "GET", "/painel/maquinas/60ef97e4-0001/pendencias.csv", sessaoTeste, nil)
	if b := r.Body.String(); r.Code != http.StatusOK || !strings.Contains(b, "60ef97e4-0001;vm-ubuntu;curl;sim;high;8.18.0-1ubuntu2.7;8.18.0-1ubuntu2.10;USN-9002-1") ||
		!strings.Contains(r.Header().Get("Content-Disposition"), "patchd-pendencias-60ef97e4-") {
		t.Errorf("pendências: %d\n%s", r.Code, b)
	}
	if r := pagina(h, "GET", "/painel/maquinas/nao-existe/pendencias.csv", sessaoTeste, nil); r.Code != http.StatusNotFound {
		t.Errorf("desconhecida: %d", r.Code)
	}

	// Sem sessão: volta ao login. A exportação vai para o log com quem exportou.
	if r := pagina(h, "GET", "/painel/frota.csv", "", nil); r.Code != http.StatusSeeOther {
		t.Errorf("sem sessão: %d", r.Code)
	}
	if !strings.Contains(log.String(), "exportação CSV") || !strings.Contains(log.String(), "username=consulta") {
		t.Errorf("log:\n%s", log.String())
	}

	// Os links nas telas.
	if b := pagina(h, "GET", "/painel/?estado=faltando", sessaoTeste, nil).Body.String(); !strings.Contains(b, `href="/painel/frota.csv?estado=faltando"`) {
		t.Error("link do CSV com o filtro")
	}
	if b := pagina(h, "GET", "/painel/maquinas/60ef97e4-0001", sessaoTeste, nil).Body.String(); !strings.Contains(b, `href="/painel/maquinas/60ef97e4-0001/pendencias.csv"`) {
		t.Error("link das pendências")
	}
}
