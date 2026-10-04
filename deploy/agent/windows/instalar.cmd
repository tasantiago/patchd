@echo off
rem patchd: instalacao do agente na frota Windows.
rem   GPO: Configuracao do Computador > Politicas > Configuracoes do Windows > Scripts >
rem        Inicializacao, com a URL do servidor no campo "Parametros do script".
rem   ITOM: pacote com estes arquivos e a linha de comando "instalar.cmd <URL>".
rem Ao lado deste script: patchd-agent-windows-amd64.exe, patchd-agent-windows-arm64.exe e,
rem durante a implantacao, enroll.token. Roda como SYSTEM. O install e repetivel: a cada
rem boot, so age se faltar registro ou servico, ou se o executavel ou as opcoes mudaram.
rem Nenhum endereco do ambiente real fica neste arquivo: a URL chega como parametro.
rem Somente ASCII aqui: o cmd le o arquivo na pagina de codigos do console.
setlocal
if "%~1"=="" (
  echo uso: %~nx0 URL-do-servidor [opcoes do agente] 1>&2
  exit /b 2
)

rem Um cmd de 32 bits em Windows de 64 bits ve "x86"; a arquitetura real vem em W6432.
set "ARQ=amd64"
if /i "%PROCESSOR_ARCHITECTURE%"=="ARM64" set "ARQ=arm64"
if /i "%PROCESSOR_ARCHITEW6432%"=="ARM64" set "ARQ=arm64"

rem Log fora da pasta de dados (que ainda nao existe na primeira vez); a Temp do Windows
rem nao deixa usuarios comuns lerem arquivos criados pelo SYSTEM.
set "LOG=%SystemRoot%\Temp\patchd-install.log"
echo ==== %DATE% %TIME% %COMPUTERNAME% >> "%LOG%"
"%~dp0patchd-agent-windows-%ARQ%.exe" install -server %* -enroll-token-file "%~dp0enroll.token" >> "%LOG%" 2>&1
set "CODIGO=%ERRORLEVEL%"
echo ==== codigo %CODIGO% >> "%LOG%"
exit /b %CODIGO%
