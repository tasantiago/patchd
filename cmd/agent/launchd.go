package main

import (
	"bytes"
	"encoding/xml"
	"path/filepath"
)

// launchdLabel: identificador do serviço no launchd (nome reverso de domínio). O
// repositório não carrega nomes do ambiente real, por isso o do próprio projeto.
const launchdLabel = "io.github.tasantiago.patchd.agent"

// launchdPlist monta o plist do LaunchDaemon. Sem sufixo de SO: testável em qualquer lugar.
//   - RunAtLoad: inicia no boot e ao ser carregado;
//   - KeepAlive/SuccessfulExit=false: reinicia se sair com erro ou cair, não se parar bem;
//   - ThrottleInterval 60: no mínimo 60 s entre reinícios;
//   - ExitTimeOut 30: depois do SIGTERM, 30 s até o SIGKILL;
//   - StandardErrorPath: só o que acontece antes de o log rotativo abrir.
func launchdPlist(bin string, args []string, dataDir string) string {
	var b bytes.Buffer
	str := func(s string) {
		b.WriteString("\t\t<string>")
		_ = xml.EscapeText(&b, []byte(s))
		b.WriteString("</string>\n")
	}
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + launchdLabel + `</string>
	<key>ProgramArguments</key>
	<array>
`)
	for _, a := range append([]string{bin, "service", "run"}, args...) {
		str(a)
	}
	b.WriteString(`	</array>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PATCHD_SERVICE_MANAGER</key>
		<string>launchd</string>
	</dict>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>ThrottleInterval</key>
	<integer>60</integer>
	<key>ExitTimeOut</key>
	<integer>30</integer>
	<key>StandardErrorPath</key>
	<string>`)
	_ = xml.EscapeText(&b, []byte(filepath.Join(dataDir, "logs", "stderr.log")))
	b.WriteString(`</string>
</dict>
</plist>
`)
	return b.String()
}
