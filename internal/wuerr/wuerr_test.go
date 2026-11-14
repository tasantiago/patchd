package wuerr

import "testing"

func TestAnnotate(t *testing.T) {
	for in, want := range map[string]string{
		"busca no WUA: Search: HRESULT 0x80244007": "busca no WUA: Search: HRESULT 0x80244007 (WU_E_PT_SOAPCLIENT_SOAPFAULT: o servidor (WSUS) recusou a sincronização; o motivo está no WindowsUpdate.log (ErrorCode do SOAP))",
		"Search: HRESULT 0x80244017":               "Search: HRESULT 0x80244017 (WU_E_PT_HTTP_STATUS_DENIED)",
		// O agente já pôs o nome: entra só a dica, uma vez.
		"busca: 0x80244017 WU_E_PT_HTTP_STATUS_DENIED":                          "busca: 0x80244017 WU_E_PT_HTTP_STATUS_DENIED",
		"tentar depois: busca: 0x8024001E WU_E_SERVICE_STOP":                    "tentar depois: busca: 0x8024001E WU_E_SERVICE_STOP (o serviço do Windows Update parou no meio da operação: a máquina desligando ou reiniciando, ou o serviço reiniciado; costuma passar sozinho)",
		"registrar catálogo offline: AddScanPackageService: HRESULT 0x80070070": "registrar catálogo offline: AddScanPackageService: HRESULT 0x80070070 (ERROR_DISK_FULL: sem espaço em disco na máquina: libere espaço (Alertas › Limpar temporários) e mande o job de novo)",
		"Search: HRESULT 0x80001234":                                            "Search: HRESULT 0x80001234",
		"sem código":                                                            "sem código",
	} {
		if got := Annotate(in); got != want {
			t.Errorf("%q:\n got %q\nwant %q", in, got, want)
		}
	}
	// Anotar duas vezes não repete nada.
	for _, in := range []string{"busca: 0x8024001E WU_E_SERVICE_STOP", "AddScanPackageService: HRESULT 0x80070070", "Search: HRESULT 0x80244007"} {
		if once := Annotate(in); Annotate(once) != once {
			t.Errorf("%q anotado duas vezes muda: %q", in, Annotate(once))
		}
	}
	if Text(0x80244010) != "0x80244010 WU_E_PT_EXCEEDED_MAX_SERVER_TRIPS" || Text(1) != "0x00000001" {
		t.Error("Text")
	}
}
