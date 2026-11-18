# Ícone da Central de software

A grade de aplicativos com o "+" no lugar do quarto app: "programas para instalar". O
`scripts/build.sh` grava `central-de-software.ico` no `patchd-app.exe` (`cmd/winres -icon`);
o Explorer, o atalho na área de trabalho e o ícone perto do relógio usam esse recurso.

- `central-de-software.svg`: o desenho de 48 px para cima (disco branco e leve inclinação).
- `central-de-software-16.svg`, `-24.svg`, `-32.svg`: os tamanhos pequenos, sem disco e sem
  inclinação, alinhados ao pixel (bandeja e barra de tarefas).
- `central-de-software.ico`: 16, 24, 32, 48, 64 e 128 px em DIB de 32 bits e 256 px em PNG,
  cada tamanho desenhado a partir do SVG dele.

Outro ícone (o da organização): `PATCHD_APP_ICON=/caminho/icone.ico scripts/build.sh`.
