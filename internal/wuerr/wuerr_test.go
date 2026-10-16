package wuerr

import "testing"

func TestAnnotate(t *testing.T) {
	for in, want := range map[string]string{
		"busca no WUA: Search: HRESULT 0x80244007":       "busca no WUA: Search: HRESULT 0x80244007 (WU_E_PT_SOAPCLIENT_SOAPFAULT: o servidor (WSUS) recusou a sincronização; o motivo está no WindowsUpdate.log (ErrorCode do SOAP))",
		"Search: HRESULT 0x8024001E":                     "Search: HRESULT 0x8024001E (WU_E_SERVICE_STOP)",
		"busca: 0x80244007 WU_E_PT_SOAPCLIENT_SOAPFAULT": "busca: 0x80244007 WU_E_PT_SOAPCLIENT_SOAPFAULT",
		"Search: HRESULT 0x80001234":                     "Search: HRESULT 0x80001234",
		"sem código":                                     "sem código",
	} {
		if got := Annotate(in); got != want {
			t.Errorf("%q:\n got %q\nwant %q", in, got, want)
		}
	}
	if Text(0x80244010) != "0x80244010 WU_E_PT_EXCEEDED_MAX_SERVER_TRIPS" || Text(1) != "0x00000001" {
		t.Error("Text")
	}
}
