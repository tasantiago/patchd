package api

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/tasantiago/patchd/internal/biospw"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/store"
)

// A senha padrão de cada fabricante (docs/bios-senha.md, seção 10): Configuração › BIOS. É a
// senha que as máquinas têm antes da primeira troca pela guarda; com ela cadastrada, o admin
// não digita a senha atual máquina por máquina. Fica fechada para a chave de guarda (como a
// guarda), nunca aparece de volta no painel e só sai do servidor fechada para a caixa de uma
// máquina, no job de troca.

type biosFleetView struct {
	On                   bool   // a guarda está ligada
	KeyID                string // a impressão da chave de guarda
	Rows                 []biosFleetRow
	MinReason, MaxReason int
	MaxPassword          int
}

type biosFleetRow struct {
	Vendor, Label string
	Set           bool
	SetAt, SetBy  string
	OtherKey      string // fechada com outra chave de guarda: não serve mais
}

// fleetBack: a aba BIOS da configuração, com o aviso.
func fleetBack(query string) string { return "/painel/configuracao?aba=bios&" + query }

func (a *api) fleetTab(ctx context.Context) (*biosFleetView, error) {
	v := &biosFleetView{On: a.escrow != nil, MinReason: minReasonLength, MaxReason: maxReasonLength, MaxPassword: biospw.MaxFleet}
	if a.escrow != nil {
		v.KeyID = a.escrow.id
	}
	list, err := a.store.BIOSFleetPasswords(ctx)
	if err != nil {
		return nil, err
	}
	for _, vendor := range jobs.BIOSVendors {
		row := biosFleetRow{Vendor: vendor, Label: vendorLabel(vendor)}
		if i := slices.IndexFunc(list, func(b store.BIOSFleetPassword) bool { return b.Vendor == vendor }); i >= 0 {
			b := list[i]
			row.Set, row.SetAt, row.SetBy = true, a.stamp(b.SetAt), b.SetBy
			if a.escrow == nil || b.EscrowKeyID != a.escrow.id {
				row.OtherKey = b.EscrowKeyID
			}
		}
		v.Rows = append(v.Rows, row)
	}
	return v, nil
}

// vendorLabel: o nome do fabricante na tela.
func vendorLabel(vendor string) string {
	switch vendor {
	case "hp":
		return "HP"
	case "dell":
		return "Dell"
	case "lenovo":
		return "Lenovo"
	}
	return vendor
}

// fleetForm: o fabricante e o motivo do formulário, conferidos ("" no erro, com o aviso).
func fleetForm(r *http.Request) (vendor, reason, problem string) {
	vendor = r.PostFormValue("fabricante")
	reason = strings.TrimSpace(r.PostFormValue("motivo"))
	switch n := utf8.RuneCountInString(reason); {
	case !slices.Contains(jobs.BIOSVendors, vendor):
		return "", "", "fabricante"
	case n < minReasonLength || n > maxReasonLength:
		return "", "", "motivo"
	}
	return vendor, reason, ""
}

// setFleetPasswordAction grava (ou troca) a senha padrão do fabricante.
func (a *api) setFleetPasswordAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	w.Header().Set("Cache-Control", "no-store")
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if a.escrow == nil {
		http.Redirect(w, r, fleetBack("bios-padrao-erro=sem-chave"), http.StatusSeeOther)
		return
	}
	vendor, reason, problem := fleetForm(r)
	if problem != "" {
		http.Redirect(w, r, fleetBack("bios-padrao-erro="+problem), http.StatusSeeOther)
		return
	}
	pw, again := r.PostFormValue("senha"), r.PostFormValue("confirmacao")
	switch {
	case biospw.CheckFleet(vendor, pw) != nil:
		http.Redirect(w, r, fleetBack("bios-padrao-erro=senha&fabricante="+vendor), http.StatusSeeOther)
		return
	case pw != again:
		http.Redirect(w, r, fleetBack("bios-padrao-erro=confirmacao"), http.StatusSeeOther)
		return
	}
	env, err := biospw.SealFleet(a.escrow.pub, vendor, pw)
	if err != nil {
		a.internalError(w, r, "fechar a senha padrão", err)
		return
	}
	if err := a.store.SetBIOSFleetPassword(r.Context(), store.BIOSFleetPassword{Vendor: vendor, Escrow: env, EscrowKeyID: a.escrow.id,
		SetAt: a.now(), SetBy: s.Username}); err != nil {
		a.internalError(w, r, "gravar a senha padrão", err)
		return
	}
	a.logger.Warn("senha padrão da BIOS gravada", "fabricante", vendor, "username", s.Username, "request_id", RequestID(r.Context()))
	a.audit(r, s, "bios.senha.padrao", vendor, "motivo: "+reason)
	http.Redirect(w, r, fleetBack("bios-padrao=gravada&fabricante="+vendor), http.StatusSeeOther)
}

// deleteFleetPasswordAction apaga a senha padrão do fabricante.
func (a *api) deleteFleetPasswordAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	vendor, reason, problem := fleetForm(r)
	if problem != "" {
		http.Redirect(w, r, fleetBack("bios-padrao-erro="+problem), http.StatusSeeOther)
		return
	}
	found, err := a.store.DeleteBIOSFleetPassword(r.Context(), vendor)
	if err != nil {
		a.internalError(w, r, "apagar a senha padrão", err)
		return
	}
	if found {
		a.audit(r, s, "bios.senha.padrao.remover", vendor, "motivo: "+reason)
	}
	http.Redirect(w, r, fleetBack("bios-padrao=removida&fabricante="+vendor), http.StatusSeeOther)
}

// fleetPassword abre a senha padrão do fabricante, se houver uma que a chave de guarda atual
// abra. Sem: "", nil.
func (a *api) fleetPassword(ctx context.Context, vendor string) (string, *store.BIOSFleetPassword, error) {
	if a.escrow == nil {
		return "", nil, nil
	}
	list, err := a.store.BIOSFleetPasswords(ctx)
	if err != nil {
		return "", nil, err
	}
	for i := range list {
		if b := &list[i]; b.Vendor == vendor && b.EscrowKeyID == a.escrow.id {
			pw, err := biospw.OpenFleet(a.escrow.priv, vendor, b.Escrow)
			if err != nil {
				return "", nil, fmt.Errorf("a senha padrão de %s não abre com a chave de guarda: %w", vendorLabel(vendor), err)
			}
			return pw, b, nil
		}
	}
	return "", nil, nil
}

// fleetFlash: os avisos da aba BIOS da configuração.
func fleetFlash(q map[string][]string, v *settingsView) {
	get := func(k string) string {
		if l := q[k]; len(l) > 0 {
			return l[0]
		}
		return ""
	}
	label := vendorLabel(get("fabricante"))
	switch get("bios-padrao") {
	case "gravada":
		v.Flash = fmt.Sprintf("Senha padrão da %s gravada. A primeira troca pela guarda das máquinas %s que já têm senha passa a usá-la sozinha.", label, label)
		return
	case "removida":
		v.Flash = fmt.Sprintf("Senha padrão da %s removida: na primeira troca, a senha atual volta a ser digitada.", label)
		return
	}
	msg, ok := map[string]string{
		"sem-chave":   "A guarda está desligada: configure PATCHD_BIOS_ESCROW_KEY_FILE (crie com patchd-server bios keygen).",
		"fabricante":  "Escolha o fabricante.",
		"motivo":      fmt.Sprintf("Informe o motivo (de %d a %d caracteres).", minReasonLength, maxReasonLength),
		"confirmacao": "A confirmação não bate com a senha: nada foi gravado.",
		"senha":       "Senha padrão inválida para " + label + ": de 1 a 64 caracteres, sem caractere de controle; na Lenovo, só ASCII sem espaço, vírgula ou ponto e vírgula.",
	}[get("bios-padrao-erro")]
	if ok {
		v.Error = msg
	}
}
