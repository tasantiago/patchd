package descoberta

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/netip"
	"net/url"
	"time"

	"github.com/masterzen/winrm"
)

// WindowsInstall is a typed operation, never an administrator-supplied command.
// Secrets and the executable travel on encrypted WinRM stdin, never process arguments.
type WindowsInstall struct {
	IP, Username, Password, Server, Token, SHA256 string
	Binary, CA                                    []byte
}

type WindowsInstaller func(context.Context, WindowsInstall) error

func InstallWindows(ctx context.Context, in WindowsInstall) error {
	ip, err := netip.ParseAddr(in.IP)
	u, urlErr := url.Parse(in.Server)
	sum := sha256.Sum256(in.Binary)
	if err != nil || !ip.Is4() || urlErr != nil || u.Scheme != "https" || u.Host == "" || u.User != nil ||
		in.Username == "" || in.Password == "" || in.Token == "" || len(in.Binary) == 0 || hex.EncodeToString(sum[:]) != in.SHA256 {
		return errors.New("configuração de instalação inválida")
	}
	endpoint := winrm.NewEndpoint(ip.String(), 5986, true, false, nil, nil, nil, 20*time.Second)
	params := *winrm.DefaultParameters
	params.TransportDecorator = func() winrm.Transporter { return &winrm.ClientNTLM{} }
	client, err := winrm.NewClientWithParameters(endpoint, in.Username, in.Password, &params)
	if err != nil {
		return errors.New("não foi possível preparar WinRM")
	}
	payload, err := json.Marshal(struct{ Binary, CA, Token, Server, SHA256 string }{
		base64.StdEncoding.EncodeToString(in.Binary), base64.StdEncoding.EncodeToString(in.CA), in.Token, in.Server, in.SHA256})
	if err != nil {
		return errors.New("não foi possível preparar o instalador")
	}
	code, err := client.RunWithContextWithInput(ctx, winrm.Powershell(windowsInstallScript), io.Discard, io.Discard, bytes.NewReader(payload))
	// Remote errors may echo tokens or command contents. Persist only a fixed diagnostic.
	if err != nil {
		return errors.New("WinRM indisponível, certificado não confiável ou credencial sem permissão")
	}
	if code != 0 {
		return errors.New("a instalação ou a verificação do serviço falhou")
	}
	return nil
}

const windowsInstallScript = `
$ErrorActionPreference = 'Stop'
$dir = $null
try {
 $p = [Console]::In.ReadToEnd() | ConvertFrom-Json
 $dir = Join-Path $env:ProgramData ('patchd-deploy-' + [guid]::NewGuid().ToString('N'))
 New-Item -ItemType Directory -Path $dir | Out-Null
 $acl = [System.Security.AccessControl.DirectorySecurity]::new()
 $acl.SetAccessRuleProtection($true, $false)
 foreach ($sid in @('S-1-5-18','S-1-5-32-544')) {
  $rule = [System.Security.AccessControl.FileSystemAccessRule]::new(
   [System.Security.Principal.SecurityIdentifier]::new($sid), 'FullControl',
   'ContainerInherit,ObjectInherit', 'None', 'Allow')
  $acl.AddAccessRule($rule)
 }
 Set-Acl -LiteralPath $dir -AclObject $acl
 $exe = Join-Path $dir 'patchd-agent.exe'
 $token = Join-Path $dir 'token.txt'
 [IO.File]::WriteAllBytes($exe, [Convert]::FromBase64String($p.Binary))
 if ((Get-FileHash -LiteralPath $exe -Algorithm SHA256).Hash -ne $p.SHA256) { throw 'hash' }
 [IO.File]::WriteAllText($token, $p.Token)
 $arguments = @('install', '-server', $p.Server, '-enroll-token-file', $token)
 if ($p.CA) {
  $ca = Join-Path $dir 'ca.crt'
  [IO.File]::WriteAllBytes($ca, [Convert]::FromBase64String($p.CA))
  $arguments += @('-ca-file', $ca)
 }
 & $exe @arguments | Out-Null
 if ($LASTEXITCODE -ne 0) { throw 'install' }
 $service = Get-Service -Name 'patchd-agent' -ErrorAction Stop
 if ($service.Status -ne 'Running') { throw 'service' }
 exit 0
} catch { exit 1 }
finally { if ($dir) { Remove-Item -LiteralPath $dir -Recurse -Force -ErrorAction SilentlyContinue } }
`
