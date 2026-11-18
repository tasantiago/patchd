package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/protocol"
)

// Remover um programa (Inventário › Programas instalados e a aba Software da máquina): o job
// desinstalar, só no Windows, para instalações de máquina com desinstalação silenciosa
// registrada (msi, quiet ou inno). O formulário diz só o nome e a chave; a entrada (método,
// ProductCode) sai do inventário atual da máquina, no servidor, e o agente confere o nome de
// novo antes de executar.

// uninstallTTL: o prazo do job pedido pelo painel.
const uninstallTTL = 24 * time.Hour

// removableEntry: a entrada pode sair pelo job desinstalar (nil) ou por que não.
func removableEntry(s protocol.Software) error {
	_, err := uninstallParamsFor(s)
	return err
}

// uninstallParamsFor: os parâmetros do job para a entrada do inventário.
func uninstallParamsFor(s protocol.Software) (jobs.UninstallParams, error) {
	return jobs.UninstallParamsFor(s)
}

// canRemove: o painel mostra o botão Remover (admin já conferido): a chave de jobs e o agente
// que remove. note: por que não, para a tela.
func (a *api) canRemove(ctx context.Context, id string) (bool, string, error) {
	if a.jobKey == nil {
		return false, "", nil
	}
	caps, err := a.store.MachineCapabilities(ctx, id)
	if err != nil {
		return false, "", err
	}
	if !slices.Contains(caps, protocol.CapabilityUninstall) {
		return false, "Para remover programas por aqui, atualize o agente desta máquina (a remoção veio com a versão nova).", nil
	}
	return true, "", nil
}

// removeBack: para onde voltar. A página do programa (o campo voltar) ou a da máquina.
func removeBack(id, back string) string {
	if strings.HasPrefix(back, "/painel/inventario/programa?") && !strings.ContainsAny(back, "\r\n") {
		return back
	}
	return machinePath(id) + "?aba=tarefas"
}

func withQuery(u, q string) string {
	if strings.Contains(u, "?") {
		return u + "&" + q
	}
	return u + "?" + q
}

// removeSoftwareAction: POST /painel/maquinas/{id}/software/remover (nome, chave, voltar).
func (a *api) removeSoftwareAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	id := r.PathValue("id")
	if !machineIDPattern.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxLoginBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulário inválido", http.StatusBadRequest)
		return
	}
	name, key := strings.TrimSpace(r.PostFormValue("nome")), r.PostFormValue("chave")
	back := removeBack(id, r.PostFormValue("voltar"))
	fail := back
	if !strings.HasPrefix(back, "/painel/inventario/") {
		fail = machinePath(id) + "?aba=software"
	}
	refuse := func(code, why string) {
		a.logger.Warn("remoção de programa pelo painel recusada: "+why, "machine_id", id, "programa", name, "username", s.Username,
			"request_id", RequestID(r.Context()))
		http.Redirect(w, r, withQuery(fail, "erro="+code+"&"+url.Values{"motivo": {shortError(why, 200)}}.Encode()), http.StatusSeeOther)
	}
	if a.jobKey == nil {
		refuse("sem-chave", "o servidor está sem a chave de jobs")
		return
	}
	ok, note, err := a.canRemove(r.Context(), id)
	if err != nil {
		a.internalError(w, r, "conferir o agente", err)
		return
	}
	if !ok {
		refuse("remover", note)
		return
	}
	inv, found, err := a.store.LatestInventory(r.Context(), id)
	if err != nil {
		a.internalError(w, r, "ler o inventário", err)
		return
	}
	var entry *protocol.Software
	if found && inv.OS.Family == "windows" {
		for i := range inv.Software {
			if strings.TrimSpace(inv.Software[i].Name) == name && inv.Software[i].Source == key {
				entry = &inv.Software[i]
			}
		}
	}
	if entry == nil {
		refuse("remover", "o programa não está no inventário atual desta máquina (ele pode já ter saído)")
		return
	}
	p, err := uninstallParamsFor(*entry)
	if err != nil {
		refuse("remover", err.Error())
		return
	}
	raw, err := json.Marshal(p)
	if err != nil {
		a.internalError(w, r, "montar o job", err)
		return
	}
	rec, err := jobs.Issue(r.Context(), a.store, a.jobKey,
		jobs.Request{MachineID: id, Type: jobs.TypeUninstall, TTL: uninstallTTL, By: s.Username, Params: raw}, a.now())
	if errors.Is(err, jobs.ErrMachineUnavailable) {
		refuse("remover", "a máquina está aposentada")
		return
	}
	if err != nil {
		a.internalError(w, r, "criar job", err)
		return
	}
	a.logger.Info("job criado pelo painel", "job_id", rec.ID, "machine_id", id, "type", rec.Type, "programa", p.Name, "versao", p.Version,
		"metodo", p.Method, "username", s.Username, "request_id", RequestID(r.Context()))
	a.audit(r, s, "programa.remover", id, fmt.Sprintf("job %d · %s %s · %s", rec.ID, p.Name, p.Version, p.Method))
	http.Redirect(w, r, withQuery(back, fmt.Sprintf("criado=1&job=%d", rec.ID)), http.StatusSeeOther)
}
