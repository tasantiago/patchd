package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/store"
)

// Limpeza de temporários pelos alertas de disco (jobs.TypeCleanup): o admin marca os alertas
// de disco (ou usa o botão da linha), escolhe os itens da lista fechada na conferência e cada
// máquina recebe um job "limpeza". O resultado (o espaço liberado) fica no job; com o disco de
// volta abaixo do limite, o alerta fecha sozinho pela regra de sempre.

const cleanupTTL = 2 * time.Hour

type cleanupItemOption struct {
	Value, Label, Hint string
	Checked            bool
}

var cleanupHints = map[string]string{
	jobs.CleanupSystemTemp:  `Windows: %SystemRoot%\Temp; Linux: /tmp e /var/tmp (systemd-tmpfiles)`,
	jobs.CleanupUserTemp:    `Windows: AppData\Local\Temp de cada perfil`,
	jobs.CleanupPatchd:      "instaladores e pastas temporárias antigas dos jobs do agente",
	jobs.CleanupUpdateCache: `Windows: SoftwareDistribution\Download (o serviço Windows Update é parado e religado); Linux: o cache do apt ou do dnf`,
	jobs.CleanupComponents:  "Windows: DISM /StartComponentCleanup (pode levar dezenas de minutos e usa bastante disco e CPU); Linux: o journal acima de 200 MB",
}

type cleanupMachine struct {
	ID, Name, Volumes string
}

type cleanupView struct {
	pageBase
	Which, Metric string
	IDs           []int64
	Digest        string
	Items         []cleanupItemOption
	Machines      []cleanupMachine
	Changed       bool
	NoKey         bool
}

// cleanupFromAlerts: a conferência (itens e máquinas) e, confirmada com a mesma lista, os jobs.
func (a *api) cleanupFromAlerts(w http.ResponseWriter, r *http.Request, s panel.Session, list []store.PerfAlert, which, metric string, back url.Values) {
	ids, keys := alertIDs(list)
	digest := batchDigest(keys)
	confirmed := r.PostFormValue("confirmar") == "1"
	chosen := r.PostForm["item"]
	if !confirmed {
		chosen = jobs.CleanupDefault
	}
	// As máquinas, uma vez cada, com os volumes em alerta.
	names, err := a.machineNames(r.Context())
	if err != nil {
		a.internalError(w, r, "listar máquinas", err)
		return
	}
	vols := map[string][]string{}
	var order []string
	for _, al := range list {
		if _, ok := vols[al.MachineID]; !ok {
			order = append(order, al.MachineID)
		}
		vols[al.MachineID] = append(vols[al.MachineID], al.Target)
	}
	p := jobs.CleanupParams{Items: chosen}
	if !confirmed || r.PostFormValue("lista") != digest || p.Validate() != nil || a.jobKey == nil {
		v := cleanupView{pageBase: a.baseFor(r.Context(), s, "Limpar temporários", "alertas"), Which: which, Metric: metric,
			Digest: digest, Changed: confirmed && r.PostFormValue("lista") != digest, NoKey: a.jobKey == nil}
		if which == "marcados" {
			v.IDs = ids
		}
		if confirmed && r.PostFormValue("lista") == digest && p.Validate() != nil {
			v.Error = "Marque ao menos um item da limpeza."
		}
		for _, it := range jobs.CleanupItems {
			v.Items = append(v.Items, cleanupItemOption{Value: it, Label: jobs.CleanupLabel(it), Hint: cleanupHints[it],
				Checked: slices.Contains(chosen, it)})
		}
		for _, id := range order {
			slices.Sort(vols[id])
			v.Machines = append(v.Machines, cleanupMachine{ID: id, Name: dash(names[id]), Volumes: strings.Join(slices.Compact(vols[id]), ", ")})
		}
		a.render(w, r, http.StatusOK, "alertas-limpeza", v)
		return
	}
	raw, err := json.Marshal(p)
	if err != nil {
		a.internalError(w, r, "montar a limpeza", err)
		return
	}
	created := 0
	var jobIDs []string
	for _, id := range order {
		rec, err := jobs.Issue(r.Context(), a.store, a.jobKey, jobs.Request{MachineID: id, Type: jobs.TypeCleanup, TTL: cleanupTTL,
			By: s.Username, Params: raw}, a.now())
		switch {
		case errors.Is(err, jobs.ErrMachineUnavailable):
			continue
		case err != nil:
			a.internalError(w, r, "criar os jobs de limpeza", err)
			return
		}
		created++
		jobIDs = append(jobIDs, strconv.FormatInt(rec.ID, 10))
	}
	labels := make([]string, len(p.Items))
	for i, it := range p.Items {
		labels[i] = jobs.CleanupLabel(it)
	}
	a.audit(r, s, "limpeza.criar", fmt.Sprintf("%d máquina(s)", created),
		fmt.Sprintf("itens: %s; jobs: %s", strings.Join(labels, ", "), strings.Join(jobIDs, ",")))
	back.Set("limpezas", strconv.Itoa(created))
	http.Redirect(w, r, "/painel/alertas?"+back.Encode(), http.StatusSeeOther)
}
