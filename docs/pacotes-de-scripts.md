# Pacotes de scripts

Em **Automação › Scripts**, o administrador pode cadastrar um ZIP com todos os arquivos
da instalação ou enviar um script avulso. O cadastro guarda **Run**, parâmetros e timeout
para reutilização. Cadastrar ou salvar a configuração não executa o pacote.

## Cadastro

- **Nome e descrição:** identificam o pacote.
- **Arquivo:** ZIP de até 256 MiB, ou script avulso de até 16 MiB.
- **Run:** caminho relativo do arquivo de entrada dentro do ZIP, por exemplo
  `instalar.cmd` ou `scripts/instalar.ps1`. Em script avulso e pacote com uma única entrada,
  o preenchimento é automático. Um ZIP com várias entradas pode ser guardado sem Run,
  mas a entrada precisa ser escolhida antes de executar.
- **Interpretador:** automático pela extensão, ou selecionado para conferir a combinação:
  PowerShell (`.ps1`), Batch (`.bat`/`.cmd`), VBScript (`.vbs`), Bash (`.sh`), ou executável
  Windows (`.exe`, dentro de um ZIP). VBScript depende de Windows Script Host habilitado
  no endpoint; o recurso pode não estar instalado em versões recentes do Windows.
- **Parâmetros:** opcionais, separados por espaço. Aspas simples ou duplas agrupam valores
  com espaços. As barras invertidas são literais, inclusive no fim de um caminho.
- **Timeout:** de 0,5 a 60 minutos; padrão de 10 minutos.

Exemplo: envie `aplicativo.zip` contendo `instalar.cmd`, `setup.exe` e `config.ini`, escolha
Run `instalar.cmd`, parâmetros `/quiet MODO=1` e timeout de 60 minutos. O script começa na
raiz extraída do pacote; caminhos de arquivos auxiliares são relativos a essa raiz,
inclusive quando o próprio script fica numa subpasta. Scripts devem aguardar o término
dos instaladores que iniciam.

Para PowerShell, `-Destino "C:\Program Files\Empresa"` chega ao script como dois argumentos.
Não há expansão automática de `%VAR%`, `$VAR` ou substituição de comandos no formulário.
No Batch, aspas usadas para agrupar são removidas antes da validação, mas aspas literais,
`&`, `|`, `<`, `>`, `^`, `%` e `!` são recusados para evitar interpretação pelo `cmd.exe`.
Valores que dependam de expansão devem ser resolvidos pelo próprio script cadastrado.
São aceitos até 32 argumentos, 1.024 bytes por argumento e 8.192 bytes no total, sujeitos
também ao teto do payload JSON assinado. Batch limita o total dos parâmetros a 6.144 bytes
para reservar espaço ao caminho do script na linha de até 8.191 caracteres do cmd.exe.

## Configurar e executar

Abra **Configurar / Executar**, confira os valores e use **Salvar configuração padrão**
para reutilizá-los. Para executar, marque as máquinas e clique em **Executar nas máquinas
marcadas**. Alterações feitas somente no disparo valem para aqueles jobs; não modificam
o cadastro. Salvar novos padrões não altera jobs que já foram assinados.

O agente confere a assinatura do job, o tamanho e o SHA-256 do pacote antes de extrair.
Scripts avulsos são normalizados em ZIP pelo servidor, preservando os bytes do script e o
mesmo contrato de download dos outros pacotes. O arquivo de entrada precisa pertencer ao
pacote; o campo Run não aceita um comando de shell nem um caminho absoluto do endpoint.

Os scripts usam a conta do serviço do agente, normalmente SYSTEM ou root. O timeout
encerra a árvore controlada: grupo de processos em Linux/macOS e Job Object no Windows.
Isso não é uma sandbox contra um script administrativo malicioso; um daemon Unix que
deliberadamente cria outra sessão pode sair do grupo. Filhos remanescentes também são
encerrados ao finalizar o job. A saída combinada mantém somente o fim do log, com memória
limitada, e informa truncamento. Código zero continua indicando término do script;
a confirmação da instalação deve usar o inventário e as evidências apropriadas.

## Exclusão, armazenamento e atualização

Excluir remove o cadastro e impede novos disparos por ele, mas **preserva o ZIP por hash**
para jobs já emitidos. A exclusão não cancela jobs. Não há coleta automática desses
arquivos nesta versão: planeje espaço em `PATCHD_CONTENT_DIR/bundles`, inclua a pasta nos
backups e não a limpe enquanto houver jobs pendentes ou entregues que dependam dela.
Arquivos publicados antes de uma falha de gravação de metadados também podem permanecer
nessa pasta; o servidor nunca publica um cadastro antes de disponibilizar seu conteúdo.

A migration `0052_scripts_execucao.sql` acrescenta a configuração sem editar migrations
existentes. Cadastros antigos mantêm o padrão de 600 segundos e permitem escolher o Run
ao executar. Atualize e homologue os agentes **antes** de usar VBScript ou parâmetros com
espaços/caminhos: agentes antigos podem recusar essas combinações. Scripts que iniciam
processos em segundo plano precisam ser ajustados para aguardar seu término.

## Validação

O workflow `scripts` verifica os pacotes afetados em Linux, Windows e macOS, com testes
de race, vet, build dos comandos e um job PostgreSQL 17 para persistência e migrations.
Fixtures usam scripts sintéticos; não instalam programas nem alteram endpoints reais.
Para executar localmente:

```sh
go test -race -timeout 10m ./internal/bundle ./internal/jobs ./internal/scriptexec ./internal/store ./internal/api
go vet ./internal/bundle ./internal/jobs ./internal/scriptexec ./internal/store ./internal/api
go build ./cmd/...
```

Defina `PATCHD_TEST_DATABASE_URL` com um banco de laboratório para incluir os testes
PostgreSQL; sem essa variável eles são pulados. Homologue também um pacote real de cada
interpretador usado pela organização antes da substituição do ITOM.
