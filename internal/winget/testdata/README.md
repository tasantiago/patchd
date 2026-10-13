# Manifestos reais do winget-pkgs

Cópias de manifestos de instalador do repositório github.com/microsoft/winget-pkgs
(licença MIT), commit 6e3b1a6194, usadas nos testes do pacote winget (Aula 14.1). As
quebras de linha foram convertidas para LF; CRLF e BOM são testados em yaml_test.go.

- 7zip.7zip: exe e wix do mesmo programa; escopo na raiz.
- Google.Chrome: tipo e escopo na raiz; endereço sem versão.
- Mozilla.Firefox.pt-BR: Silent na raiz, herdado por todos.
- Microsoft.VisualStudioCode: escopo user e machine; Custom na raiz.
- Python.Python.3.14: burn, com Custom por escopo.
- Python.Python.3.8: usa âncora (&o0), recusada pelo leitor.
- Notepad++.Notepad++: nullsoft, wix e zip; ExpectedReturnCodes por instalador.
- Adobe.Acrobat.Reader.64-bit: exe com Silent e ExpectedReturnCodes na raiz.
- Microsoft.PowerShell: msix (descartado) e wix.
