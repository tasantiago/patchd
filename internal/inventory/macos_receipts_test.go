package inventory

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Saída real do pkgutil --pkg-info-plist no Mac do laboratório (Aula 2.4).
const reciboReal = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>install-location</key>
	<string>/</string>
	<key>install-time</key>
	<integer>1737854274</integer>
	<key>pkg-version</key>
	<string>1.0.0.0.1645860813</string>
	<key>pkgid</key>
	<string>com.apple.pkg.MobileAssets</string>
	<key>receipt-plist-version</key>
	<real>1</real>
	<key>volume</key>
	<string>/</string>
</dict>
</plist>`

// Recibo sintético de terceiro, com o array "groups" (visto no recibo real do XProtect).
const reciboTerceiro = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
	<key>groups</key>
	<array><string>com.empresa.grupo</string></array>
	<key>install-time</key>
	<integer>1788000000</integer>
	<key>pkg-version</key>
	<string>8.2.1</string>
	<key>pkgid</key>
	<string>com.empresa.agente</string>
	<key>relocatable</key>
	<false/>
</dict>
</plist>`

func TestParsePlistDict(t *testing.T) {
	d, err := parsePlistDict([]byte(reciboTerceiro))
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if d["pkgid"] != "com.empresa.agente" || d["pkg-version"] != "8.2.1" || d["relocatable"] != "false" {
		t.Errorf("valores simples errados: %v", d)
	}
	if _, ok := d["groups"]; ok {
		t.Errorf("arrays deveriam ser ignorados: %v", d)
	}
	if _, err := parsePlistDict([]byte("<plist></plist>")); err == nil {
		t.Error("esperado erro sem <dict>")
	}
	if _, err := parsePlistDict([]byte("isto não é xml <<<")); err == nil {
		t.Error("esperado erro com XML inválido")
	}
}

func TestReceiptToSoftwareReal(t *testing.T) {
	s, err := receiptToSoftware([]byte(reciboReal))
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if s.Name != "com.apple.pkg.MobileAssets" || s.Version != "1.0.0.0.1645860813" || s.InstallDate != "2025-01-26" {
		t.Errorf("recibo real errado: %+v", s)
	}
	if !s.SystemComponent || s.Scope != "machine" || s.Source != receiptsSource {
		t.Errorf("marcações erradas: %+v", s)
	}
}

func TestCollectDarwinReceiptsParcial(t *testing.T) {
	run := runnerFalso{
		"/usr/sbin/pkgutil --pkgs":                                      {saida: "com.apple.pkg.MobileAssets\ncom.empresa.agente\ncom.empresa.quebrado\n"},
		"/usr/sbin/pkgutil --pkg-info-plist com.apple.pkg.MobileAssets": {saida: reciboReal},
		"/usr/sbin/pkgutil --pkg-info-plist com.empresa.agente":         {saida: reciboTerceiro},
		"/usr/sbin/pkgutil --pkg-info-plist com.empresa.quebrado":       {err: errors.New("No receipt for 'com.empresa.quebrado' found")},
	}
	list, err := collectDarwinReceipts(context.Background(), run)
	if len(list) != 2 {
		t.Fatalf("esperados 2 recibos lidos: %+v", list)
	}
	if list[1].Name != "com.empresa.agente" || list[1].SystemComponent {
		t.Errorf("recibo de terceiro não é componente de sistema: %+v", list[1])
	}
	if err == nil || !strings.Contains(err.Error(), "com.empresa.quebrado") {
		t.Errorf("o recibo ilegível deveria virar erro sem interromper os demais: %v", err)
	}
}
