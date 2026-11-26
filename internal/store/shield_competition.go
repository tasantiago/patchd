package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Tipos de grupo de competição do Shield.
const (
	CompetitionBuildings = "predios" // prédios disputam entre si
	CompetitionFloors    = "andares" // um prédio, os andares dele disputam
)

// Limites de um grupo de competição.
const (
	CompetitionNameMax      = 80
	CompetitionMaxBuildings = 200
)

// ShieldCompetition é um grupo de competição: a disputa que a Central mostra primeiro.
type ShieldCompetition struct {
	ID        int64
	Name      string
	Kind      string
	Buildings []CompetitionBuilding // em ordem de nome
	UpdatedBy string
	UpdatedAt time.Time
}

// CompetitionBuilding é um prédio do grupo (o nome atual do patrimônio).
type CompetitionBuilding struct {
	ID      int64
	Name    string
	Present bool
}

// ErrShieldCompetition: grupo fora das regras (a mensagem diz qual).
type ErrShieldCompetition struct{ Msg string }

func (e *ErrShieldCompetition) Error() string { return "grupo de competição: " + e.Msg }

func competitionErr(format string, a ...any) error {
	return &ErrShieldCompetition{Msg: fmt.Sprintf(format, a...)}
}

// IsShieldCompetitionError diz se err é recusa das regras do grupo.
func IsShieldCompetitionError(err error) bool {
	var e *ErrShieldCompetition
	return errors.As(err, &e)
}

// checkCompetition confere o nome, o tipo e a quantidade de prédios (sem banco).
func checkCompetition(c *ShieldCompetition, ids []int64) error {
	c.Name = strings.Join(strings.Fields(c.Name), " ")
	switch {
	case c.Name == "" || len([]rune(c.Name)) > CompetitionNameMax:
		return competitionErr("o nome tem de 1 a %d caracteres", CompetitionNameMax)
	case strings.ContainsFunc(c.Name, unicode.IsControl):
		return competitionErr("o nome tem caracteres de controle")
	}
	switch c.Kind {
	case CompetitionBuildings:
		if len(ids) < 2 || len(ids) > CompetitionMaxBuildings {
			return competitionErr("um grupo de prédios tem de 2 a %d prédios", CompetitionMaxBuildings)
		}
	case CompetitionFloors:
		if len(ids) != 1 {
			return competitionErr("um grupo por andar tem um prédio só")
		}
	default:
		return competitionErr("tipo desconhecido")
	}
	return nil
}

// ShieldCompetitions: todos os grupos, com os prédios, em ordem de nome.
func (p *Postgres) ShieldCompetitions(ctx context.Context) ([]ShieldCompetition, error) {
	rows, err := p.pool.Query(ctx, `SELECT c.id, c.name, c.kind, c.updated_by, c.updated_at, b.id, b.name, b.present
		FROM shield_competitions c
		LEFT JOIN shield_competition_buildings cb ON cb.competition_id = c.id
		LEFT JOIN buildings b ON b.id = cb.building_id
		ORDER BY c.name, c.id, b.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ShieldCompetition
	for rows.Next() {
		var c ShieldCompetition
		var bid *int64
		var bname *string
		var present *bool
		if err := rows.Scan(&c.ID, &c.Name, &c.Kind, &c.UpdatedBy, &c.UpdatedAt, &bid, &bname, &present); err != nil {
			return nil, err
		}
		if n := len(out); n == 0 || out[n-1].ID != c.ID {
			c.UpdatedAt = c.UpdatedAt.UTC()
			out = append(out, c)
		}
		if bid != nil {
			last := &out[len(out)-1]
			last.Buildings = append(last.Buildings, CompetitionBuilding{ID: *bid, Name: *bname, Present: *present})
		}
	}
	return out, rows.Err()
}

// SaveShieldCompetition cria (ID 0) ou troca um grupo e os prédios dele, numa transação. O
// prédio que já está em outro grupo é recusado com o nome dos dois. Devolve o ID.
func (p *Postgres) SaveShieldCompetition(ctx context.Context, c ShieldCompetition, ids []int64, by string) (int64, error) {
	ids = slices.Clone(ids)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if err := checkCompetition(&c, ids); err != nil {
		return 0, err
	}
	if strings.TrimSpace(by) == "" {
		return 0, competitionErr("sem autor")
	}
	id := c.ID
	err := pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		var known []int64
		rows, err := tx.Query(ctx, `SELECT id FROM buildings WHERE id = ANY($1)`, ids)
		if err != nil {
			return err
		}
		if known, err = pgx.CollectRows(rows, pgx.RowTo[int64]); err != nil {
			return err
		}
		if len(known) != len(ids) {
			return competitionErr("prédio desconhecido")
		}
		if id == 0 {
			err = tx.QueryRow(ctx, `INSERT INTO shield_competitions (name, kind, created_by, updated_by) VALUES ($1, $2, $3, $3) RETURNING id`,
				c.Name, c.Kind, by).Scan(&id)
		} else {
			var tag pgconn.CommandTag
			tag, err = tx.Exec(ctx, `UPDATE shield_competitions SET name = $2, kind = $3, updated_by = $4, updated_at = now() WHERE id = $1`,
				id, c.Name, c.Kind, by)
			if err == nil && tag.RowsAffected() == 0 {
				return ErrShieldNotFound
			}
		}
		if err != nil {
			var pe *pgconn.PgError
			if errors.As(err, &pe) && pe.Code == "23505" {
				return competitionErr("já existe um grupo com o nome %q", c.Name)
			}
			return err
		}
		// Os prédios que estão em outro grupo: recusa, com os nomes.
		var clash []string
		rows, err = tx.Query(ctx, `SELECT b.name || ' (no grupo ' || c.name || ')' FROM shield_competition_buildings cb
			JOIN buildings b ON b.id = cb.building_id JOIN shield_competitions c ON c.id = cb.competition_id
			WHERE cb.building_id = ANY($1) AND cb.competition_id <> $2 ORDER BY b.name`, ids, id)
		if err != nil {
			return err
		}
		if clash, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
			return err
		}
		if len(clash) > 0 {
			return competitionErr("um prédio fica em um grupo só: %s", strings.Join(clash, ", "))
		}
		if _, err := tx.Exec(ctx, `DELETE FROM shield_competition_buildings WHERE competition_id = $1`, id); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO shield_competition_buildings (building_id, competition_id) SELECT unnest($1::bigint[]), $2`, ids, id)
		return err
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// DeleteShieldCompetition apaga o grupo (os prédios voltam a ficar sem grupo).
func (p *Postgres) DeleteShieldCompetition(ctx context.Context, id int64) (string, bool, error) {
	var name string
	err := p.pool.QueryRow(ctx, `DELETE FROM shield_competitions WHERE id = $1 RETURNING name`, id).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return name, err == nil, err
}
