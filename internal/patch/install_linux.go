package patch

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/tasantiago/patchd/internal/inventory"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/platform"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/version"
)

const systemdRunPath = "/usr/bin/systemd-run"

// linuxInstaller instala pelo apt ou pelo dnf, que conferem a assinatura dos repositórios
// configurados na máquina (o patchd não acrescenta nenhum).
type linuxInstaller struct {
	run      platform.Runner
	readFile func(string) ([]byte, error)
}

// NewInstaller devolve o instalador do SO atual. run precisa de prazo longo: uma
// atualização grande leva minutos.
func NewInstaller(run platform.Runner) Installer {
	return linuxInstaller{run: run, readFile: os.ReadFile}
}

// Install confere cada pacote contra uma busca feita agora e só então instala, numa
// unidade transitória do systemd: o serviço do agente roda com ProtectSystem=full (/usr e
// /etc só leitura), e uma instalação parada no meio pelo stop do serviço deixaria o dpkg
// pela metade. A unidade continua mesmo se o agente parar.
func (l linuxInstaller) Install(ctx context.Context, jobID int64, pkgs []jobs.Package) (string, error) {
	manager, id, err := inventory.DetectPackageManager(l.readFile)
	if err != nil {
		return "", err
	}
	var (
		pending []protocol.MissingUpdate
		compare func(a, b string) (int, error)
		install []string
		tool    string
	)
	names := make([]string, len(pkgs))
	for i, p := range pkgs {
		names[i] = p.Name
	}
	switch manager {
	case "dpkg":
		out, err := l.run.Run(ctx, aptGetPath, "-s", "dist-upgrade")
		if err != nil {
			return "", fmt.Errorf("conferir pendências: %w", err)
		}
		if pending, err = parseAptSimulation(out); err != nil {
			return "", fmt.Errorf("conferir pendências: %w", err)
		}
		compare, tool = version.CompareDpkg, "apt-get"
		// DPkg::Lock::Timeout: espera até 5 min se outro apt (unattended-upgrades) estiver
		// rodando. confdef/confold: arquivo de configuração alterado localmente fica como está.
		install = append([]string{"--setenv=DEBIAN_FRONTEND=noninteractive", "--", aptGetPath, "install", "-y", "--only-upgrade",
			"-o", "DPkg::Lock::Timeout=300", "-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold"}, names...)
	case "rpm":
		out, err := l.run.Run(ctx, dnfPath, "advisory", "list", "--security", "--json")
		if err != nil {
			return "", fmt.Errorf("conferir pendências: %w", err)
		}
		if pending, err = parseDnfAdvisories(out); err != nil {
			return "", fmt.Errorf("conferir pendências: %w", err)
		}
		compare, tool = version.CompareRPM, "dnf"
		install = append([]string{"--", dnfPath, "upgrade", "-y"}, names...)
	default:
		return "", fmt.Errorf("%w: a distribuição %q não tem instalação de pacotes no patchd", jobs.ErrNotPending, id)
	}

	if err := checkPending(pkgs, pending, compare); err != nil {
		return "", err
	}

	args := append([]string{"--unit=patchd-job-" + fmt.Sprint(jobID), "--wait", "--pipe", "--collect", "--quiet",
		"--setenv=LC_ALL=C"}, install...)
	out, err := l.run.Run(ctx, systemdRunPath, args...)
	if err != nil {
		return lastLines(out, 2), fmt.Errorf("%s: %w", tool, err)
	}
	return fmt.Sprintf("%s: %d pacote(s) pedidos (%s); %s", tool, len(pkgs), strings.Join(names, ", "), summaryLine(out)), nil
}

// summaryLine acha o resumo do apt ("2 upgraded, 0 newly installed, ...") ou, sem ele,
// fica com a última linha (o dnf termina com "Complete!" ou "Nothing to do.").
func summaryLine(out []byte) string {
	for _, l := range strings.Split(string(out), "\n") {
		if strings.Contains(l, " upgraded, ") && strings.Contains(l, " newly installed") {
			return strings.TrimSpace(l)
		}
	}
	return lastLines(out, 1)
}

// checkPending exige que cada pacote apareça na busca com candidata pelo menos na versão
// pedida. A busca do dnf lista um item por advisory: vale a maior correção do pacote.
func checkPending(pkgs []jobs.Package, pending []protocol.MissingUpdate, compare func(a, b string) (int, error)) error {
	best := map[string]string{}
	for _, u := range pending {
		cur, ok := best[u.Package]
		if !ok {
			best[u.Package] = u.FixedVersion
			continue
		}
		if c, err := compare(u.FixedVersion, cur); err == nil && c > 0 {
			best[u.Package] = u.FixedVersion
		}
	}
	var problems []string
	for _, p := range pkgs {
		cand, ok := best[p.Name]
		if !ok {
			problems = append(problems, p.Name+" não está pendente")
			continue
		}
		c, err := compare(cand, p.MinVersion)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: versão incomparável (%v)", p.Name, err))
			continue
		}
		if c < 0 {
			problems = append(problems, fmt.Sprintf("%s: a candidata %s é mais velha que a pedida %s (listas do repositório desatualizadas?)", p.Name, cand, p.MinVersion))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", jobs.ErrNotPending, strings.Join(problems, "; "))
	}
	return nil
}
