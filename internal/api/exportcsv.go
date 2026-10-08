package api

import (
	"bytes"
	"context"
	"fmt"
	"net/http"

	"github.com/tasantiago/patchd/internal/compliance"
	"github.com/tasantiago/patchd/internal/export"
)

// sendCSV responde o arquivo como download. O conteúdo é montado inteiro antes: um erro
// no meio vira 500, e não um arquivo cortado que parece completo.
func (a *api) sendCSV(w http.ResponseWriter, r *http.Request, name string, rows int, write func(*bytes.Buffer) error) {
	var b bytes.Buffer
	if err := write(&b); err != nil {
		a.internalError(w, r, "montar CSV", err)
		return
	}
	s, _ := SessionFrom(r.Context())
	a.logger.Info("exportação CSV", "username", s.Username, "path", r.URL.Path, "rows", rows, "request_id", RequestID(r.Context()))
	h := w.Header()
	h.Set("Content-Type", "text/csv; charset=utf-8")
	h.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(b.Bytes())
}

// fleetCSV exporta a frota (com o mesmo filtro ?estado= da tela).
func (a *api) fleetCSV(w http.ResponseWriter, r *http.Request) {
	if a.complianceOff(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), complianceTimeout)
	defer cancel()
	fleet, err := a.compliance.Fleet(ctx)
	if a.canceled(r, err) {
		return
	}
	if err != nil {
		a.internalError(w, r, "avaliar a frota", err)
		return
	}
	suffix := ""
	if f := compliance.State(r.URL.Query().Get("estado")); f.Rank() < len(compliance.States) {
		kept := fleet.Machines[:0:0]
		for _, m := range fleet.Machines {
			if m.State == string(f) {
				kept = append(kept, m)
			}
		}
		fleet.Machines, suffix = kept, "-"+string(f)
	}
	name := fmt.Sprintf("patchd-frota%s-%s.csv", suffix, fleet.EvaluatedAt.UTC().Format("20060102-1504"))
	a.sendCSV(w, r, name, len(fleet.Machines), func(b *bytes.Buffer) error { return export.Fleet(b, fleet) })
}

// itemsCSV exporta as pendências de uma máquina.
func (a *api) itemsCSV(w http.ResponseWriter, r *http.Request) {
	id, ok := a.machineID(w, r)
	if !ok || a.complianceOff(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), complianceTimeout)
	defer cancel()
	d, found, err := a.compliance.Detail(ctx, id)
	if a.canceled(r, err) {
		return
	}
	if err != nil {
		a.internalError(w, r, "avaliar a máquina", err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "máquina desconhecida")
		return
	}
	name := fmt.Sprintf("patchd-pendencias-%s-%s.csv", short(id), d.EvaluatedAt.UTC().Format("20060102-1504"))
	a.sendCSV(w, r, name, len(d.Items), func(b *bytes.Buffer) error { return export.Items(b, d) })
}
