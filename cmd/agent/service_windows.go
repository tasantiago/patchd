package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/tasantiago/patchd/internal/config"
	"github.com/tasantiago/patchd/internal/credential"
)

// dataDirSDDL: controle total para SYSTEM e Administradores, herdado por tudo o que for
// criado dentro (OI = arquivos, CI = pastas), e DACL protegida (P): a pasta deixa de
// herdar de C:\ProgramData, onde o grupo Usuários tem leitura.
const dataDirSDDL = "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"

// stopGrace: quanto o serviço espera o laço terminar depois do pedido de parada. Uma busca
// do WUA em andamento não é interrompível; passado o prazo, o processo sai assim mesmo, a
// busca se perde e é refeita na próxima partida (a agenda só avança com a busca concluída).
const stopGrace = 25 * time.Second

func isServiceProcess() bool {
	ok, err := svc.IsWindowsService()
	return err == nil && ok
}

// protectDataDir cria a pasta de dados e aplica a DACL protegida. O SetNamedSecurityInfo
// propaga a herança para o que já existe dentro; a credential.json, que tem DACL
// protegida própria (Aula 4.3), não é afetada.
func protectDataDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString(dataDirSDDL)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

// installedBinary é onde o serviço roda: em Arquivos de Programas, onde só administradores
// escrevem. Um serviço que roda como SYSTEM a partir de uma pasta em que um usuário comum
// escreve é uma escalada de privilégio pronta: basta trocar o executável.
func installedBinary() string {
	pf := os.Getenv("ProgramFiles")
	if pf == "" {
		pf = `C:\Program Files`
	}
	return filepath.Join(pf, "patchd", "patchd-agent.exe")
}

// installedPath é o installedBinary(), com a mesma forma de função do Linux e do macOS.
func installedPath() string { return installedBinary() }

// copyBinary copia o executável atual para dest (temporário + renomeação).
func copyBinary(dest string) error {
	src, err := os.Executable()
	if err != nil {
		return err
	}
	if src, err = filepath.EvalSymlinks(src); err != nil {
		return err
	}
	if strings.EqualFold(filepath.Clean(src), filepath.Clean(dest)) {
		return nil // já está rodando do lugar instalado
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dest + ".novo"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dest)
}

// serviceInstall copia o agente para Arquivos de Programas, protege a pasta de dados,
// registra o serviço (início automático atrasado, reinício em caso de falha) e o inicia.
// As opções dadas aqui (-data-dir, -checkin-interval...) viram a linha de comando do serviço.
func serviceInstall(args []string, look config.Lookup, stdout, stderr io.Writer) int {
	cfg, err := loadAgentConfig(args, look, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-agent: configuração inválida: %v\n", err)
		return exitConfig
	}
	if _, err := credential.Load(cfg.DataDir); err != nil {
		fmt.Fprintf(stderr, "patchd-agent: registre a máquina antes de instalar o serviço (patchd-agent enroll -data-dir %s): %v\n", cfg.DataDir, err)
		return exitRuntime
	}

	m, err := mgr.Connect()
	if err != nil {
		fmt.Fprintf(stderr, "patchd-agent: gerenciador de serviços: %v (execute como administrador)\n", err)
		return exitRuntime
	}
	defer m.Disconnect()
	if s, err := m.OpenService(serviceName); err == nil {
		s.Close()
		fmt.Fprintf(stderr, "patchd-agent: o serviço %s já existe; desinstale antes (patchd-agent service uninstall)\n", serviceName)
		return exitRuntime
	}

	if err := protectDataDir(cfg.DataDir); err != nil {
		fmt.Fprintf(stderr, "patchd-agent: proteger %s: %v\n", cfg.DataDir, err)
		return exitRuntime
	}
	bin := installedBinary()
	if err := copyBinary(bin); err != nil {
		fmt.Fprintf(stderr, "patchd-agent: copiar o agente para %s: %v\n", bin, err)
		return exitRuntime
	}

	runArgs := append([]string{"service", "run"}, args...)
	s, err := m.CreateService(serviceName, bin, mgr.Config{
		DisplayName:      "patchd agent",
		Description:      "Coleta inventário e atualizações faltantes e envia ao patchd-server.",
		StartType:        mgr.StartAutomatic,
		DelayedAutoStart: true, // começa depois dos serviços essenciais: não pesa no boot
	}, runArgs...)
	if err != nil {
		fmt.Fprintf(stderr, "patchd-agent: criar o serviço: %v\n", err)
		return exitRuntime
	}
	defer s.Close()

	// Falha: reinicia em 1 min, depois em 5, depois a cada 15; o contador zera em 1 dia.
	// "NonCrashFailures": vale também quando o agente sai com erro, e não só quando cai.
	actions := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: time.Minute},
		{Type: mgr.ServiceRestart, Delay: 5 * time.Minute},
		{Type: mgr.ServiceRestart, Delay: 15 * time.Minute},
	}
	if err := s.SetRecoveryActions(actions, uint32((24 * time.Hour).Seconds())); err != nil {
		fmt.Fprintf(stderr, "patchd-agent: aviso: ações de recuperação: %v\n", err)
	}
	if err := s.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		fmt.Fprintf(stderr, "patchd-agent: aviso: recuperação em saída com erro: %v\n", err)
	}

	if err := s.Start(); err != nil {
		fmt.Fprintf(stderr, "patchd-agent: o serviço foi criado, mas não iniciou: %v\n", err)
		return exitRuntime
	}
	fmt.Fprintf(stdout, "serviço %s instalado e iniciado\n  executável: %s\n  dados e log: %s\n",
		serviceName, bin, filepath.Join(cfg.DataDir, "logs", "agent.log"))
	return exitOK
}

// serviceUninstall para e remove o serviço e o executável instalado. A pasta de dados (com
// a credencial) fica: reinstalar não deve criar outra máquina no servidor.
func serviceUninstall(stdout, stderr io.Writer) int {
	m, err := mgr.Connect()
	if err != nil {
		fmt.Fprintf(stderr, "patchd-agent: gerenciador de serviços: %v (execute como administrador)\n", err)
		return exitRuntime
	}
	defer m.Disconnect()
	s, err := m.OpenService(serviceName)
	if err != nil {
		fmt.Fprintf(stdout, "o serviço %s não está instalado\n", serviceName)
		return exitOK
	}

	if st, err := s.Query(); err == nil && st.State != svc.Stopped {
		if _, err := s.Control(svc.Stop); err != nil {
			fmt.Fprintf(stderr, "patchd-agent: pedir a parada: %v\n", err)
		}
		deadline := time.Now().Add(stopGrace + 15*time.Second)
		for time.Now().Before(deadline) {
			if st, err := s.Query(); err != nil || st.State == svc.Stopped {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
	}
	err = s.Delete()
	// O Delete só marca o serviço para remoção: ele sai do SCM quando o último handle
	// fecha. Por isso o handle fecha aqui, e não num defer, e a espera vem em seguida;
	// sem ela, a reinstalação do install acharia o serviço ainda lá.
	s.Close()
	if err != nil {
		fmt.Fprintf(stderr, "patchd-agent: remover o serviço: %v\n", err)
		return exitRuntime
	}
	waitServiceGone(m, 30*time.Second)

	// O processo pode levar um instante para soltar o executável.
	bin := installedBinary()
	for i := 0; i < 20; i++ {
		err = os.Remove(bin)
		if err == nil || errors.Is(err, os.ErrNotExist) {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(stderr, "patchd-agent: aviso: não foi possível apagar %s: %v\n", bin, err)
	}
	_ = os.Remove(filepath.Dir(bin)) // só sai se estiver vazia
	fmt.Fprintf(stdout, "serviço %s removido; a pasta de dados e a credencial foram mantidas\n", serviceName)
	return exitOK
}

// waitServiceGone espera o SCM terminar de remover o serviço (até limit).
func waitServiceGone(m *mgr.Mgr, limit time.Duration) {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		s, err := m.OpenService(serviceName)
		if err != nil {
			return
		}
		s.Close()
		time.Sleep(500 * time.Millisecond)
	}
}

// requireAdmin recusa instalar sem elevação. Sem ela, o install criaria a pasta de dados
// como usuário comum e aplicaria nela uma DACL que tira o acesso do próprio usuário.
func requireAdmin(stderr io.Writer) bool {
	if !windows.GetCurrentProcessToken().IsElevated() {
		fmt.Fprintln(stderr, "patchd-agent: instalar o agente exige um prompt elevado (Executar como administrador) ou a conta SYSTEM")
		return false
	}
	return true
}

// serviceCommandLine reproduz a linha de comando que o mgr.CreateService grava no SCM.
func serviceCommandLine(bin string, args []string) string {
	s := syscall.EscapeArg(bin)
	for _, a := range args {
		s += " " + syscall.EscapeArg(a)
	}
	return s
}

// installedState compara o serviço e o executável instalados com os que este install
// criaria: iguais, não há o que fazer; diferentes, o install reinstala.
func installedState(cfg agentConfig, args []string) (serviceState, error) {
	m, err := mgr.Connect()
	if err != nil {
		return 0, fmt.Errorf("gerenciador de serviços: %w", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(serviceName)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return serviceMissing, nil
	}
	if err != nil {
		return 0, err
	}
	defer s.Close()
	c, err := s.Config()
	if err != nil {
		return 0, err
	}
	bin := installedBinary()
	same, err := sameExecutable(bin)
	if err != nil {
		return 0, err
	}
	want := serviceCommandLine(bin, append([]string{"service", "run"}, args...))
	if same && c.BinaryPathName == want && c.StartType == mgr.StartAutomatic {
		return serviceCurrent, nil
	}
	return serviceOutdated, nil
}

// serviceStart inicia o serviço se estiver parado (depois de uma falha que esgotou a
// recuperação, por exemplo). Em execução ou iniciando, não faz nada.
func serviceStart() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(serviceName)
	if err != nil {
		return err
	}
	defer s.Close()
	st, err := s.Query()
	if err != nil {
		return err
	}
	if st.State == svc.Stopped {
		return s.Start()
	}
	return nil
}

// handler liga o laço de check-in ao gerenciador de serviços do Windows.
type handler struct {
	logger *slog.Logger
	loop   func(context.Context) int
}

func (h *handler) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	changes <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() { done <- h.loop(ctx) }()
	changes <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}

	for {
		select {
		case code := <-done:
			// O laço terminou sozinho: é erro (credencial ausente, por exemplo). O código
			// vai para o gerenciador, e as ações de recuperação reiniciam o serviço.
			changes <- svc.Status{State: svc.StopPending}
			return code != exitOK, uint32(code)
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				changes <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				h.logger.Info("parada pedida pelo gerenciador de serviços", "shutdown", c.Cmd == svc.Shutdown)
				changes <- svc.Status{State: svc.StopPending, WaitHint: uint32((stopGrace + 5*time.Second).Milliseconds())}
				cancel()
				select {
				case <-done:
				case <-time.After(stopGrace):
					h.logger.Warn("o laço não terminou a tempo (provavelmente uma busca do WUA); encerrando assim mesmo")
				}
				return false, 0
			}
		}
	}
}

// runAsService entrega o laço ao gerenciador de serviços e só volta quando o serviço para.
func runAsService(logger *slog.Logger, loop func(context.Context) int) int {
	if err := svc.Run(serviceName, &handler{logger: logger, loop: loop}); err != nil {
		logger.Error("gerenciador de serviços", "error", err)
		return exitRuntime
	}
	return exitOK
}

// serviceLog: sob o SCM não há terminal; o log vai para o arquivo rotativo da pasta de dados.
func serviceLog(dataDir string, asService bool, stderr io.Writer) (io.Writer, func(), error) {
	if !asService {
		return stderr, func() {}, nil
	}
	return rotatingLog(dataDir)
}
