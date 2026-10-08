package main

import (
	"strings"
)

// systemdQuote prepara um argumento para o ExecStart do systemd: "%" vira "%%" e "$" vira
// "$$" (senão seriam especificadores e variáveis), e argumentos com espaço, aspas ou
// barra invertida vão entre aspas duplas, com as aspas e barras escapadas.
func systemdQuote(s string) string {
	s = strings.ReplaceAll(s, "%", "%%")
	s = strings.ReplaceAll(s, "$", "$$")
	if s != "" && !strings.ContainsAny(s, " \t\"'\\") {
		return s
	}
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// systemdUnit monta a unidade do serviço. Sem sufixo de SO: testável em qualquer lugar.
func systemdUnit(bin string, args []string) string {
	cmd := []string{systemdQuote(bin), "service", "run"}
	for _, a := range args {
		cmd = append(cmd, systemdQuote(a))
	}
	return `# Gerado por "patchd-agent service install". Para mudar as opções, desinstale e instale de novo.
[Unit]
Description=patchd agent (inventário e atualizações faltantes)
Wants=network-online.target
After=network-online.target
# Sem limite de tentativas de início: o RestartSec abaixo já espaça os reinícios.
StartLimitIntervalSec=0

[Service]
Type=simple
Environment=PATCHD_SERVICE_MANAGER=systemd
ExecStart=` + strings.Join(cmd, " ") + `
# Falha (saída com erro ou queda): novo início em 60 s.
Restart=on-failure
RestartSec=60
# Parada: SIGTERM; depois de 30 s, SIGKILL.
TimeoutStopSec=30
# Proteções: /usr, /boot e /etc só leitura; /home só leitura; /tmp próprio; sem ganho de
# privilégio por setuid. O agente só escreve na pasta de dados, nos caches do apt e do dnf
# (/var) e na configuração do cache de repositórios (Aula 6.6): o "-" ignora a pasta que
# não existir (o apt numa Fedora, o dnf numa Ubuntu).
NoNewPrivileges=yes
ProtectSystem=full
ProtectHome=read-only
PrivateTmp=yes
ReadWritePaths=-/etc/apt/apt.conf.d -/etc/dnf

[Install]
WantedBy=multi-user.target
`
}
