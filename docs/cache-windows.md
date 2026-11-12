# Cache do Windows Update no servidor (RF-33)

O pedido (12/11/2026): o WSUS vai ser descontinuado; como fazer o cache das atualizações no
servidor do patchd para as máquinas da rede interna.

## 1. A situação do WSUS

A Microsoft declarou o WSUS **obsoleto** (deprecated) em setembro de 2024: não recebe
novidades, mas continua no Windows Server 2025, com correções enquanto a versão do servidor
tiver suporte, e **não há data de remoção** anunciada. Ou seja: o WSUS atual pode seguir
funcionando enquanto o patchd assume o papel dele, sem corrida.

O WSUS fazia duas coisas para o parque: **dizia o que falta** (a busca) e **entregava os
arquivos** pela rede interna. O patchd já fazia a primeira sem o WSUS (o catálogo offline
`wsusscn2.cab`, que o servidor baixa e distribui desde a Aula 6.6) e já decidia o que instalar
(aprovação em Patch, campanhas por grupo de implantação). Faltava a segunda: é o que este
documento descreve.

## 2. Como funciona

```
Microsoft ──(1 vez por pacote)──> servidor do patchd ──(rede interna)──> máquinas
```

1. A campanha (ou o botão Instalar da máquina) manda o job `windows_update` com o campo
   `cache`, só para as máquinas que o parâmetro inclui e cujo agente sabe usar o cache.
2. O agente procura a atualização pelo **catálogo offline** que o servidor já distribui (sem
   ele, na Microsoft). O Windows Update da máquina diz quais arquivos a atualização precisa e
   de onde eles vêm (`download.windowsupdate.com`).
3. O agente pede esses arquivos ao servidor. O servidor baixa da Microsoft **uma vez** (a
   primeira máquina espera; o job dela fica adiado se passar de 45 minutos) e guarda.
4. O agente baixa do servidor, com retomada, confere o SHA-256 e entrega os arquivos ao
   Windows Update (`CopyToCache`), que confere cada um contra a metadado assinada da
   atualização. Depois instala, como antes. O job nunca reinicia.

**O que garante que o arquivo é o certo:**

- o servidor só baixa de endereços de download da Microsoft (`download.windowsupdate.com` e
  seus subdomínios, `download.microsoft.com`, `*.delivery.mp.microsoft.com`) e só pacotes de
  atualização (`.cab`, `.msu`, `.psf`, `.wim`, `.esd`, `.exe`): não vira proxy para nada;
- quando o nome do arquivo traz o SHA-1 (o padrão do Windows Update: `..._<sha1>.cab`), o
  servidor confere e apaga o que não bate;
- o agente confere o SHA-256 que o servidor anunciou;
- o Windows Update recusa, no `CopyToCache`, o arquivo que não é o da atualização.

## 3. Ligar

1. **Servidor**: precisa de `PATCHD_CONTENT_DIR` (no Podman, já vem configurado). Os pacotes
   ficam em `<PATCHD_CONTENT_DIR>/windows-update`, no volume que já existe.
2. **Saída do servidor** para `download.windowsupdate.com` e subdomínios (HTTPS, com HTTP de
   reserva). Se a rede tem proxy, o servidor usa o `HTTPS_PROXY` de sempre.
3. **Agentes desta versão** em diante, no Windows (anunciam `wucache-v1`). Os outros seguem
   como antes.
4. **Configuração › Parâmetros › Agentes › Cache do Windows Update no servidor**: comece em
   **só o grupo de implantação piloto**.
5. Acompanhe em **Configuração › Sistema › Cache do Windows Update**: o que está baixando, o
   que foi usado por último e o tamanho.

**Espaço em disco:** uma atualização cumulativa tem de centenas de MB a alguns GB por versão e
arquitetura do Windows. Com três versões no parque, conte uns 5 a 10 GB por mês. O pacote que
nenhuma máquina pede em 60 dias sai sozinho (o prazo muda em Parâmetros, de 7 a 365 dias).

## 4. O piloto (o que conferir na frente da máquina)

Este caminho não foi testado num Windows de verdade nesta versão (o ambiente de
desenvolvimento não tem Windows). No piloto, com 1 máquina de cada versão do Windows:

- o job termina com "pelo cache do servidor: ..." e o KB aparece instalado no histórico do
  Windows Update (Configurações › Windows Update › Histórico);
- o cartão do cache mostra o pacote baixado uma vez, e a segunda máquina não baixa de novo;
- a atualização cumulativa do Windows 11 24H2 em diante (a que vem em mais de um arquivo, com
  a "checkpoint" antes) instala inteira. Se o Windows Update pedir um endereço que o cache
  recusa (os de entrega com parâmetros, de alguns pacotes), o job falha com o endereço no
  detalhe: anote e me passe.

## 5. Desligar o WSUS (depois do piloto)

Com o piloto bom e o parâmetro em **todas as máquinas**:

1. Rode as campanhas do mês pelo patchd com o cache ligado e confira a conformidade.
2. Na GPO, desfaça "Especificar o local do serviço de atualização da Microsoft na intranet"
   (o endereço do WSUS). Sem ela, a busca da política vai à Microsoft; o patchd continua
   usando o catálogo offline para o cache.
3. Na mesma GPO, deixe "Configurar Atualizações Automáticas" em **2 – Notificar para baixar e
   instalar** (ou desabilitada): quem decide o que instalar e quando é o patchd. Se o Windows
   ficar livre para baixar sozinho, cada máquina volta a buscar da internet o que o cache já
   tem.
4. Desligue o WSUS só depois de um ciclo mensal inteiro sem ele.

## 6. O que fica de fora

- **Atualização de versão** (feature update, ex.: 23H2 → 24H2): fora do escopo da v1, como
  antes.
- **Drivers e definições do Defender**: seguem como hoje (o Defender se atualiza sozinho).
- **Downloads expressos/diferenciais**: o cache guarda os pacotes completos que o catálogo
  offline indica (como o WSUS fazia sem o "express").

## 7. Alternativas da Microsoft (sem código)

- **Otimização de Entrega (Delivery Optimization) entre máquinas**: grátis, já no Windows. Com
  o modo de download em "LAN" ou "Grupo" na GPO, as máquinas da mesma rede trocam pedaços entre
  si e baixam menos da internet. Combina com o cache do patchd (não conflita).
- **Microsoft Connected Cache para Empresas e Educação**: o substituto oficial do papel de
  cache do WSUS, disponível desde julho de 2025, grátis para quem tem Windows Enterprise E3/E5
  ou Education A3/A5. Roda numa máquina própria (Windows 11, Windows Server 2022 ou mais novo,
  Ubuntu 24.04 ou RHEL 8/9), com 8 GB de memória ou mais, e é gerenciado pelo portal do Azure.
  Não cabe no servidor do patchd (que tem 3,6 GB de memória). Fica como opção se a licença
  existir; o cache do patchd não depende dele.
