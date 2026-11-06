package store

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

// Perguntas ao parque (jobs.TypeQuery): a pergunta e os jobs dela, um por máquina.

// Question é uma pergunta feita no painel.
type Question struct {
	ID          int64
	Sensor, Arg string
	Name        string // registro: o nome do valor
	Target      string // "todas as máquinas", "grupo de implantação 2"
	Machines    int    // quantos jobs foram criados
	CreatedBy   string
	CreatedAt   time.Time
	Answered    int // jobs encerrados (com resposta, recusa ou prazo vencido)
}

// QuestionJob é a resposta (ou a espera) de uma máquina.
type QuestionJob struct {
	JobID     int64
	MachineID string
	State     string
	Detail    string
}

// CreateQuestion grava a pergunta e devolve o ID (os jobs vêm depois, com o QuestionID).
func (p *Postgres) CreateQuestion(ctx context.Context, q Question) (int64, error) {
	var id int64
	err := p.pool.QueryRow(ctx, `INSERT INTO questions (sensor, arg, name, target, machines, created_by)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`, q.Sensor, q.Arg, q.Name, q.Target, q.Machines, q.CreatedBy).Scan(&id)
	return id, err
}

// SetQuestionMachines corrige quantos jobs saíram (máquinas aposentadas no meio ficam fora).
func (p *Postgres) SetQuestionMachines(ctx context.Context, id int64, n int) error {
	_, err := p.pool.Exec(ctx, `UPDATE questions SET machines = $2 WHERE id = $1`, id, n)
	return err
}

const questionCols = `q.id, q.sensor, q.arg, q.name, q.target, q.machines, q.created_by, q.created_at,
	(SELECT count(*) FROM jobs j WHERE j.question_id = q.id AND j.state NOT IN ('pendente', 'entregue'))`

func scanQuestion(row pgx.Row) (Question, error) {
	var q Question
	err := row.Scan(&q.ID, &q.Sensor, &q.Arg, &q.Name, &q.Target, &q.Machines, &q.CreatedBy, &q.CreatedAt, &q.Answered)
	q.CreatedAt = q.CreatedAt.UTC()
	return q, err
}

// Questions: as perguntas, da mais nova para a mais antiga.
func (p *Postgres) Questions(ctx context.Context, limit int) ([]Question, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+questionCols+` FROM questions q ORDER BY q.id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Question, error) { return scanQuestion(r) })
}

// Question devolve a pergunta.
func (p *Postgres) Question(ctx context.Context, id int64) (Question, bool, error) {
	q, err := scanQuestion(p.pool.QueryRow(ctx, `SELECT `+questionCols+` FROM questions q WHERE q.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return q, false, nil
	}
	return q, err == nil, err
}

// QuestionJobs: os jobs da pergunta, em ordem de máquina.
func (p *Postgres) QuestionJobs(ctx context.Context, id int64) ([]QuestionJob, error) {
	rows, err := p.pool.Query(ctx, `SELECT id, machine_id, state, detail FROM jobs WHERE question_id = $1 ORDER BY machine_id, id`, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (QuestionJob, error) {
		var j QuestionJob
		err := r.Scan(&j.JobID, &j.MachineID, &j.State, &j.Detail)
		return j, err
	})
}

// --- Memória -------------------------------------------------------------------------

// CreateQuestion na memória.
func (m *Memory) CreateQuestion(ctx context.Context, q Question) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	q.ID = int64(len(m.questions) + 1)
	q.CreatedAt = m.now()
	m.questions = append(m.questions, q)
	return q.ID, nil
}

// SetQuestionMachines na memória.
func (m *Memory) SetQuestionMachines(ctx context.Context, id int64, n int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id >= 1 && id <= int64(len(m.questions)) {
		m.questions[id-1].Machines = n
	}
	return nil
}

func (m *Memory) questionAnswered(id int64) int {
	n := 0
	for _, j := range m.jobs {
		if j.questionID == id && j.State != "pendente" && j.State != "entregue" {
			n++
		}
	}
	return n
}

// Questions na memória.
func (m *Memory) Questions(ctx context.Context, limit int) ([]Question, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Question
	for i := len(m.questions) - 1; i >= 0 && len(out) < limit; i-- {
		q := m.questions[i]
		q.Answered = m.questionAnswered(q.ID)
		out = append(out, q)
	}
	return out, nil
}

// Question na memória.
func (m *Memory) Question(ctx context.Context, id int64) (Question, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if id < 1 || id > int64(len(m.questions)) {
		return Question{}, false, nil
	}
	q := m.questions[id-1]
	q.Answered = m.questionAnswered(id)
	return q, true, nil
}

// QuestionJobs na memória.
func (m *Memory) QuestionJobs(ctx context.Context, id int64) ([]QuestionJob, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []QuestionJob
	for _, j := range m.jobs {
		if j.questionID == id {
			out = append(out, QuestionJob{JobID: j.ID, MachineID: j.MachineID, State: j.State, Detail: j.Detail})
		}
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].MachineID != out[b].MachineID {
			return out[a].MachineID < out[b].MachineID
		}
		return out[a].JobID < out[b].JobID
	})
	return out, nil
}
