package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/tasantiago/patchd/internal/httpx"
	"github.com/tasantiago/patchd/internal/installomator"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
	"github.com/tasantiago/patchd/internal/tpinstall"
	"github.com/tasantiago/patchd/internal/version"
)

// WithThirdPartyManifest dá ao servidor as regras de terceiros (Aula 14.4): é com elas que
// ele reconhece, no inventário novo, o programa que o job thirdparty atualizou.
func WithThirdPartyManifest(m *thirdparty.Live) Option {
	return func(a *api) { a.tpManifest = m }
}

// WithInstallomator: a versão do Installomator que vai aos Macs (internal/installomator).
func WithInstallomator(s installomator.Script) Option {
	return func(a *api) { a.installomator = &s }
}

// packageName: o nome do instalador no cache, <sha256>.msi ou <sha256>.exe (Aula 14.2), ou o
// Installomator dos Macs, <sha256>.sh (internal/installomator).
var packageName = regexp.MustCompile(`^[0-9a-f]{64}\.(msi|exe|sh)$`)

// packageFile entrega um instalador do cache às máquinas autenticadas. O nome é o SHA-256:
// o agente confere o conteúdo contra o job assinado antes de executar.
func (a *api) packageFile(w http.ResponseWriter, r *http.Request, id string) {
	name := r.PathValue("file")
	if a.contentDir == "" || !packageName.MatchString(name) {
		writeError(w, http.StatusNotFound, "content_not_found", "instalador não encontrado")
		return
	}
	f, err := os.Open(filepath.Join(a.contentDir, "pacotes", name))
	if errors.Is(err, fs.ErrNotExist) {
		writeError(w, http.StatusNotFound, "content_not_found", "instalador não encontrado no cache do servidor")
		return
	}
	if err != nil {
		a.internalError(w, r, "abrir instalador", err)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, "content_not_found", "instalador não encontrado")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("ETag", `"`+name[:64]+`"`)
	a.logger.Info("download de instalador", "machine_id", id, "file", name[:16], "request_id", RequestID(r.Context()))
	done, ok := a.beginDownload(w, dlPackage, id) // o limite de downloads e a aba Saúde (downloads.go)
	if !ok {
		return
	}
	defer done()
	http.ServeContent(httpx.ExtendWrites(w, downloadIdle), r, "", fi.ModTime(), f)
}

// judgeThirdParty confere o job thirdparty pelo inventário (RF-08): o agente manda um
// inventário novo logo depois de instalar, antes do resultado. Só vira "concluido" se o
// inventário mudou depois da entrega, mostra uma única instalação visível do programa e
// ela está pelo menos na versão do job.
func (a *api) judgeThirdParty(r *http.Request, job store.JobRow, res protocol.JobResult) (string, string) {
	var payload jobs.Job
	if err := json.Unmarshal([]byte(job.Payload), &payload); err != nil {
		return jobs.StateFailed, "o servidor não conseguiu ler o job"
	}
	p, err := jobs.ParseThirdPartyParams(payload.Params)
	if err != nil {
		return jobs.StateFailed, "parâmetros do job ilegíveis: " + err.Error()
	}
	if a.tpManifest == nil {
		return jobs.StateFailed, "o servidor está sem as regras de terceiros e não consegue conferir o inventário"
	}
	ms, err := a.store.Machines(r.Context())
	if err != nil {
		return jobs.StateFailed, "o servidor não conseguiu ler a máquina"
	}
	var changed *protocol.MachineSummary
	for i := range ms {
		if ms[i].ID == job.MachineID {
			changed = &ms[i]
		}
	}
	agent := strings.TrimSpace(res.Detail)
	if changed == nil || changed.InventoryChangedAt == nil || job.DeliveredAt == nil || !changed.InventoryChangedAt.After(*job.DeliveredAt) {
		return jobs.StateFailed, "o agente informou sucesso, mas o inventário não mudou depois da entrega; agente: " + agent
	}
	inv, found, err := a.store.LatestInventory(r.Context(), job.MachineID)
	if err != nil || !found {
		return jobs.StateFailed, "o servidor não conseguiu ler o inventário novo"
	}
	when := changed.InventoryChangedAt.UTC().Format("2006-01-02 15:04:05")
	machine, user := tpinstall.Entries(inv.OS.ID, inv.Software, a.tpManifest.Get(), p.App)
	switch len(machine) {
	case 0:
		return jobs.StateFailed, fmt.Sprintf("o inventário de %s UTC não mostra o programa %s instalado para a máquina toda: %s; agente: %s",
			when, p.App, missingWhy(inv, a.tpManifest.Get(), p.App, user), agent)
	case 1:
	default:
		var parts []string
		for _, e := range machine {
			parts = append(parts, e.Kind+" "+e.Software.Version)
		}
		return jobs.StateFailed, fmt.Sprintf("o inventário de %s UTC mostra %d instalações visíveis de %s (%s): a instalação deixou uma duplicata; agente: %s",
			when, len(machine), p.App, strings.Join(parts, ", "), agent)
	}
	e := machine[0]
	target, ok := thirdparty.Normalize(p.Version)
	if !ok || e.Installed == "" {
		return jobs.StateFailed, fmt.Sprintf("versão que não se compara (instalada %q, do job %q); agente: %s", e.Software.Version, p.Version, agent)
	}
	if c, err := version.CompareDotted(e.Installed, target); err != nil || c < 0 {
		return jobs.StateFailed, fmt.Sprintf("o inventário de %s UTC mostra %s %s, abaixo de %s; agente: %s", when, p.App, e.Software.Version, p.Version, agent)
	}
	return jobs.StateDone, fmt.Sprintf("o inventário de %s UTC mostra %s (%s); agente: %s", when, nameVersion(e.Software.Name, e.Software.Version), e.Kind, agent)
}

// missingWhy: por que o inventário não mostra o programa depois de o instalador terminar bem.
// A instalação só para um usuário (o instalador do Electron, sem /allusers, instala no perfil
// de quem roda: o SYSTEM, que o inventário não lê), ou a entrada com o nome da regra e outro
// fabricante (o fabricante no programa não é o do Programas e Recursos).
func missingWhy(inv protocol.InventoryReport, m thirdparty.Manifest, app string, user []tpinstall.Entry) string {
	if len(user) > 0 {
		return fmt.Sprintf("há %d instalação(ões) só para um usuário (%s %s): o instalador instalou por usuário; "+
			"use os argumentos que instalam para todos os usuários (ex.: /allusers)", len(user), user[0].Software.Name, user[0].Software.Version)
	}
	var rule *thirdparty.App
	for _, r := range m.For(inv.OS.ID) {
		if r.ID == app {
			rule = &r
			break
		}
	}
	if rule != nil {
		for _, s := range inv.Software {
			if rule.MatchesName(s.Name) {
				return fmt.Sprintf("o inventário tem %q com o fabricante %q, que não é o do programa no patchd: "+
					"corrija o fabricante no inventário (ou deixe em branco)", s.Name, s.Publisher)
			}
		}
	}
	return "nenhuma entrada com o nome do programa em Programas e Recursos da máquina; se o instalador instala por usuário, " +
		"ele foi para o perfil do SYSTEM (o agente), que o inventário não lê: use os argumentos que instalam para todos os usuários (ex.: /allusers)"
}

// nameVersion: o nome com a versão, sem repetir quando o nome já a traz ("LibreOffice
// 26.8.1.1" mostrava "LibreOffice 26.8.1.1 26.8.1.1" no job 35).
func nameVersion(name, version string) string {
	if version == "" || strings.Contains(name, version) {
		return name
	}
	return name + " " + version
}

// judgeUninstall confere o job desinstalar pelo inventário: o agente manda um inventário
// novo logo depois de remover, antes do resultado. Só vira "concluido" se o inventário atual
// não mostra mais a entrada (a mesma chave com o mesmo nome).
func (a *api) judgeUninstall(r *http.Request, job store.JobRow, res protocol.JobResult) (string, string) {
	var payload jobs.Job
	if err := json.Unmarshal([]byte(job.Payload), &payload); err != nil {
		return jobs.StateFailed, "o servidor não conseguiu ler o job"
	}
	p, err := jobs.ParseUninstallParams(payload.Params)
	if err != nil {
		return jobs.StateFailed, "parâmetros do job ilegíveis: " + err.Error()
	}
	agent := strings.TrimSpace(res.Detail)
	inv, found, err := a.store.LatestInventory(r.Context(), job.MachineID)
	if err != nil || !found {
		return jobs.StateFailed, "o servidor não conseguiu ler o inventário novo; agente: " + agent
	}
	when := inv.CollectedAt.UTC().Format("2006-01-02 15:04:05")
	for _, s := range inv.Software {
		if strings.EqualFold(s.Source, p.Key) && strings.EqualFold(strings.TrimSpace(s.Name), p.Name) {
			return jobs.StateFailed, fmt.Sprintf("o inventário de %s UTC ainda mostra %s; agente: %s", when, nameVersion(s.Name, s.Version), agent)
		}
	}
	return jobs.StateDone, fmt.Sprintf("o inventário de %s UTC não mostra mais %s; agente: %s", when, nameVersion(p.Name, p.Version), agent)
}
