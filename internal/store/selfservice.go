package store

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"
)

// SelfServiceOffer (Aula 17.1): o que o admin liberou no autoatendimento para um programa.
type SelfServiceOffer struct {
	AppID      string
	CanUpdate  bool     // o usuário atualiza o que já está instalado
	CanInstall bool     // o usuário instala do zero
	Groups     []string // grupos do AD (sAMAccountName, com ou sem DOMINIO\); vazio: qualquer usuário
	UpdatedBy  string
	UpdatedAt  time.Time
}

// MaxOfferGroups: grupos por oferta.
const MaxOfferGroups = 10

var groupName = regexp.MustCompile(`^(?:[A-Za-z0-9][A-Za-z0-9.-]{0,14}\\)?[A-Za-z0-9][A-Za-z0-9 ._$-]{0,63}$`)

// ValidGroup: o sAMAccountName do grupo, com ou sem o domínio NetBIOS (DOMINIO\GG-X). Desde
// a Aula 17.2 o DN não é aceito: os grupos vêm do token do Windows, que traz DOMINIO\grupo.
func ValidGroup(g string) bool { return groupName.MatchString(g) }

// Validate confere a oferta.
func (o SelfServiceOffer) Validate() error {
	switch {
	case o.AppID == "" || len(o.AppID) > 64:
		return errors.New("programa inválido")
	case !o.CanUpdate && !o.CanInstall:
		return errors.New("a oferta libera atualizar, instalar, ou os dois")
	case len(o.Groups) > MaxOfferGroups:
		return errors.New("grupos demais")
	}
	for _, g := range o.Groups {
		if !ValidGroup(g) {
			return errors.New("grupo fora do formato (sAMAccountName do grupo, com ou sem DOMINIO\\)")
		}
	}
	return nil
}

// SelfServiceOffers lista as ofertas, por programa.
func (p *Postgres) SelfServiceOffers(ctx context.Context) ([]SelfServiceOffer, error) {
	rows, err := p.pool.Query(ctx, `SELECT app_id, can_update, can_install, groups, updated_by, updated_at
		FROM selfservice_offers ORDER BY app_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SelfServiceOffer{}
	for rows.Next() {
		var o SelfServiceOffer
		if err := rows.Scan(&o.AppID, &o.CanUpdate, &o.CanInstall, &o.Groups, &o.UpdatedBy, &o.UpdatedAt); err != nil {
			return nil, err
		}
		o.UpdatedAt = o.UpdatedAt.UTC()
		if o.Groups == nil {
			o.Groups = []string{}
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// SetSelfServiceOffer grava (ou troca) a oferta de um programa.
func (p *Postgres) SetSelfServiceOffer(ctx context.Context, o SelfServiceOffer) error {
	if err := o.Validate(); err != nil {
		return err
	}
	if o.Groups == nil {
		o.Groups = []string{}
	}
	_, err := p.pool.Exec(ctx, `
		INSERT INTO selfservice_offers (app_id, can_update, can_install, groups, updated_by, updated_at)
		VALUES ($1, $2, $3, $4, $5, now())
		ON CONFLICT (app_id) DO UPDATE SET can_update = $2, can_install = $3, groups = $4, updated_by = $5, updated_at = now()`,
		o.AppID, o.CanUpdate, o.CanInstall, o.Groups, o.UpdatedBy)
	return err
}

// DeleteSelfServiceOffer tira o programa do autoatendimento; false: não estava.
func (p *Postgres) DeleteSelfServiceOffer(ctx context.Context, app string) (bool, error) {
	tag, err := p.pool.Exec(ctx, `DELETE FROM selfservice_offers WHERE app_id = $1`, app)
	return tag.RowsAffected() > 0, err
}

// SelfServiceOffers na memória.
func (m *Memory) SelfServiceOffers(ctx context.Context) ([]SelfServiceOffer, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []SelfServiceOffer{}
	for _, o := range m.offers {
		o.Groups = slices.Clone(o.Groups)
		out = append(out, o)
	}
	slices.SortFunc(out, func(a, b SelfServiceOffer) int { return strings.Compare(a.AppID, b.AppID) })
	return out, nil
}

// SetSelfServiceOffer na memória.
func (m *Memory) SetSelfServiceOffer(ctx context.Context, o SelfServiceOffer) error {
	if err := o.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.offers == nil {
		m.offers = map[string]SelfServiceOffer{}
	}
	o.Groups = slices.Clone(o.Groups)
	if o.Groups == nil {
		o.Groups = []string{}
	}
	o.UpdatedAt = m.now().UTC()
	m.offers[o.AppID] = o
	return nil
}

// DeleteSelfServiceOffer na memória.
func (m *Memory) DeleteSelfServiceOffer(ctx context.Context, app string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.offers[app]
	delete(m.offers, app)
	return ok, nil
}
