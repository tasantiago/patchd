package descoberta

import "strings"

// Hint é uma forma de instalar o agente numa máquina descoberta: o rótulo do método, o
// comando pronto (ou o passo a passo, no caso da GPO) e uma observação curta.
type Hint struct {
	Method  string // "psexec" | "powershell" | "ssh" | "gpo"
	Label   string
	Command string
	Note    string
}

// InstallHints monta os comandos de instalação para um host, pelo palpite de SO. O IP vai
// concreto; o usuário administrador e o token ficam a preencher — o patchd não guarda
// credencial. O token sai de Configuração › Agente, salvo em patchd-token.txt.
//
// osGuess: "windows" → PowerShell remoto, PsExec e GPO; "ssh" → SSH; vazio → os dois lados.
func InstallHints(ip, server, osGuess string, ca bool) []Hint {
	if server == "" {
		server = "https://<endereço-do-servidor>"
	}
	win := osGuess == "windows" || osGuess == ""
	nix := osGuess == "ssh" || osGuess == ""
	var out []Hint
	if win {
		out = append(out, winHints(ip, server, ca)...)
	}
	if nix {
		out = append(out, sshHint(ip, server, ca))
	}
	return out
}

func winHints(ip, server string, ca bool) []Hint {
	caFiles, caCopy, caFlag := "", "", ""
	if ca {
		caFiles = ", ca.crt"
		caCopy = ", ca.crt"
		caFlag = " -ca-file C:\\Windows\\Temp\\ca.crt"
	}
	// PowerShell remoto (WinRM): copia os arquivos pela sessão e roda o install.
	ps := strings.Join([]string{
		"# PowerShell; crie um token em Configuração › Agente e salve patchd-token.txt na pasta atual",
		"$ip = '" + ip + "'",
		"$s = New-PSSession -ComputerName $ip -Credential (Get-Credential)   # usuário com admin local na máquina",
		"Copy-Item patchd-agent-windows-amd64.exe, patchd-token.txt" + caCopy + " -Destination C:\\Windows\\Temp\\ -ToSession $s",
		"Invoke-Command -Session $s { C:\\Windows\\Temp\\patchd-agent-windows-amd64.exe install -server " + server + " -enroll-token-file C:\\Windows\\Temp\\patchd-token.txt" + caFlag + " }",
		"Invoke-Command -Session $s { Remove-Item C:\\Windows\\Temp\\patchd-token.txt }; Remove-PSSession $s",
	}, "\n")
	// PsExec: estaciona os arquivos no C$ e executa.
	pe := strings.Join([]string{
		"# PsExec (Sysinternals) no PATH; patchd-agent-windows-amd64.exe, patchd-token.txt" + caFiles + " na pasta atual",
		"$ip = '" + ip + "'",
		"Copy-Item patchd-agent-windows-amd64.exe, patchd-token.txt" + caCopy + " -Destination \\\\$ip\\C$\\Windows\\Temp\\",
		"PsExec.exe \\\\$ip -u DOMINIO\\SEU_USUARIO -h C:\\Windows\\Temp\\patchd-agent-windows-amd64.exe install -server " + server + " -enroll-token-file C:\\Windows\\Temp\\patchd-token.txt" + caFlag,
	}, "\n")
	caGPO := ""
	if ca {
		caGPO = " O ca.crt entra numa GPO de certificados confiáveis (ou -ca-file apontando para o compartilhamento)."
	}
	gpo := strings.Join([]string{
		"1. Copie patchd-agent-windows-amd64.exe" + caFiles + " para um compartilhamento que as máquinas leiam (ex.: \\\\servidor\\patchd$).",
		"2. Em Configuração › Agente, crie um token de validade curta e vários usos; salve patchd-token.txt no mesmo compartilhamento.",
		"3. Numa GPO de Script de Inicialização (Computador) na OU das máquinas, chame:",
		"   \\\\servidor\\patchd$\\patchd-agent-windows-amd64.exe install -server " + server + " -enroll-token-file \\\\servidor\\patchd$\\patchd-token.txt" + caFlag,
		"4. Tire a GPO e troque o token depois que as máquinas se registrarem." + caGPO,
	}, "\n")
	return []Hint{
		{Method: "powershell", Label: "PowerShell remoto (WinRM)", Command: ps, Note: "Pede WinRM ligado na máquina (Enable-PSRemoting). O token some ao final; nenhuma senha fica no servidor."},
		{Method: "psexec", Label: "PsExec", Command: pe, Note: "Pede o compartilhamento C$ e admin local. O token fica em arquivo, não na linha de comando."},
		{Method: "gpo", Label: "GPO (em massa)", Command: gpo, Note: "Para muitas máquinas de uma OU de uma vez, sem tocar em cada uma."},
	}
}

func sshHint(ip, server string, ca bool) Hint {
	caScp, caInstall, caFlag := "", "", ""
	if ca {
		caScp = " ca.crt"
		caInstall = "sudo install -d -m 0755 /etc/patchd && sudo install -m 0644 /tmp/ca.crt /etc/patchd/ca.crt && "
		caFlag = " -ca-file /etc/patchd/ca.crt"
	}
	cmd := strings.Join([]string{
		"# crie um token em Configuração › Agente e salve patchd-token.txt na pasta atual",
		"ip=" + ip,
		"scp patchd-agent-linux-amd64 patchd-token.txt" + caScp + " SEU_USUARIO@$ip:/tmp/",
		"ssh SEU_USUARIO@$ip 'chmod +x /tmp/patchd-agent-linux-amd64 && " + caInstall + "sudo /tmp/patchd-agent-linux-amd64 install -server " + server + " -enroll-token-file /tmp/patchd-token.txt" + caFlag + " && rm -f /tmp/patchd-token.txt'",
	}, "\n")
	return Hint{Method: "ssh", Label: "SSH", Command: cmd, Note: "Para Linux ou macOS. Pede um usuário com sudo. O token é apagado ao final."}
}
