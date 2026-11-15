package store

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tasantiago/patchd/internal/protocol"
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
	Skipped     int // máquinas do alvo que ficaram de fora (agente anterior à pergunta)
}

// QuestionItem é um achado de uma pergunta em lista (jobs.ListSensor), de uma máquina.
type QuestionItem struct {
	MachineID string
	protocol.AnswerItem
}

// QuestionItemGroup junta os itens de mesma chave no parque (uma extensão, pelo ID).
type QuestionItemGroup struct {
	Key, Name string   // o nome mais comum da chave
	Note      string   // a observação mais comum
	Versions  []string // as versões vistas, em ordem
	Sources   []string // os navegadores
	Machines  int
	Users     int // pares (máquina, usuário)
	Items     int
}

// QuestionItemQuery filtra e ordena os grupos.
type QuestionItemQuery struct {
	Text          string // pedaço do nome, da chave, do usuário ou da observação
	Sort          string // ItemSortCommon (padrão), ItemSortRare, ItemSortName
	Limit, Offset int
}

// Ordens dos grupos.
const (
	ItemSortCommon = "comuns" // em mais máquinas primeiro
	ItemSortRare   = "raras"  // em menos máquinas primeiro: o que só um ou dois têm
	ItemSortName   = "nome"
)

// QuestionItemTotals: o tamanho da resposta do parque.
type QuestionItemTotals struct {
	Items, Keys, Machines, Users int
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
	err := p.pool.QueryRow(ctx, `INSERT INTO questions (sensor, arg, name, target, machines, created_by, skipped)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`, q.Sensor, q.Arg, q.Name, q.Target, q.Machines, q.CreatedBy, q.Skipped).Scan(&id)
	return id, err
}

// SetQuestionMachines corrige quantos jobs saíram (máquinas aposentadas no meio ficam fora).
func (p *Postgres) SetQuestionMachines(ctx context.Context, id int64, n int) error {
	_, err := p.pool.Exec(ctx, `UPDATE questions SET machines = $2 WHERE id = $1`, id, n)
	return err
}

const questionCols = `q.id, q.sensor, q.arg, q.name, q.target, q.machines, q.created_by, q.created_at, q.skipped,
	(SELECT count(*) FROM jobs j WHERE j.question_id = q.id AND j.state NOT IN ('pendente', 'entregue'))`

func scanQuestion(row pgx.Row) (Question, error) {
	var q Question
	err := row.Scan(&q.ID, &q.Sensor, &q.Arg, &q.Name, &q.Target, &q.Machines, &q.CreatedBy, &q.CreatedAt, &q.Skipped, &q.Answered)
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

// MachinesWithCapability: as máquinas ativas cujo agente anunciou a capacidade.
func (p *Postgres) MachinesWithCapability(ctx context.Context, capability string) ([]string, error) {
	rows, err := p.pool.Query(ctx, `SELECT id FROM machines WHERE retired_at IS NULL AND $1 = ANY(capabilities) ORDER BY id COLLATE "C"`, capability)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// SaveQuestionItems guarda os itens que a máquina devolveu para o job de uma pergunta em
// lista (trocando os que houver: o resultado repetido não duplica). Um job que não é de
// pergunta, ou não é desta máquina, não guarda nada.
func (p *Postgres) SaveQuestionItems(ctx context.Context, jobID int64, machineID string, items []protocol.AnswerItem) error {
	var qid *int64
	err := p.pool.QueryRow(ctx, `SELECT question_id FROM jobs WHERE id = $1 AND machine_id = $2`, jobID, machineID).Scan(&qid)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && qid == nil {
		return nil
	}
	if err != nil {
		return err
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) // sem efeito depois do Commit
	if _, err := tx.Exec(ctx, `DELETE FROM question_items WHERE question_id = $1 AND machine_id = $2`, *qid, machineID); err != nil {
		return err
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"question_items"},
		[]string{"question_id", "machine_id", "key", "name", "version", "usuario", "origem", "lugar", "nota"},
		pgx.CopyFromSlice(len(items), func(i int) ([]any, error) {
			it := items[i]
			return []any{*qid, machineID, it.Key, it.Name, it.Version, it.User, it.Source, it.Where, it.Note}, nil
		})); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// likePattern: o texto como padrão do ILIKE ("contém"), com os curingas dele sem efeito.
func likePattern(text string) string {
	return "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(text) + "%"
}

const questionItemMatch = ` AND ($2 = '' OR key ILIKE $2 OR name ILIKE $2 OR usuario ILIKE $2 OR nota ILIKE $2 OR origem ILIKE $2)`

// QuestionItemGroups: os itens da pergunta juntados pela chave, e quantos grupos há ao todo
// (com o filtro). O filtro conta só os itens que casam: procurar por um usuário mostra o que
// é dele.
func (p *Postgres) QuestionItemGroups(ctx context.Context, id int64, q QuestionItemQuery) ([]QuestionItemGroup, int, error) {
	order := `3 DESC, 2, 1`
	switch q.Sort {
	case ItemSortRare:
		order = `3, 2, 1`
	case ItemSortName:
		order = `lower(mode() WITHIN GROUP (ORDER BY name)), 1`
	}
	pattern := ""
	if q.Text != "" {
		pattern = likePattern(q.Text)
	}
	rows, err := p.pool.Query(ctx, `
		SELECT key, mode() WITHIN GROUP (ORDER BY name), count(DISTINCT machine_id), count(DISTINCT (machine_id, usuario)), count(*),
		       coalesce(array_agg(DISTINCT version) FILTER (WHERE version <> ''), '{}'),
		       coalesce(array_agg(DISTINCT origem) FILTER (WHERE origem <> ''), '{}'),
		       mode() WITHIN GROUP (ORDER BY nota), count(*) OVER ()
		FROM question_items WHERE question_id = $1`+questionItemMatch+`
		GROUP BY key ORDER BY `+order+` LIMIT $3 OFFSET $4`, id, pattern, q.Limit, q.Offset)
	if err != nil {
		return nil, 0, err
	}
	total := 0
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (QuestionItemGroup, error) {
		var g QuestionItemGroup
		err := r.Scan(&g.Key, &g.Name, &g.Machines, &g.Users, &g.Items, &g.Versions, &g.Sources, &g.Note, &total)
		sortVersions(g.Versions)
		return g, err
	})
	return list, total, err
}

// QuestionItemTotals: quantos itens, chaves, máquinas e usuários a pergunta juntou.
func (p *Postgres) QuestionItemTotals(ctx context.Context, id int64) (QuestionItemTotals, error) {
	var t QuestionItemTotals
	err := p.pool.QueryRow(ctx, `SELECT count(*), count(DISTINCT key), count(DISTINCT machine_id), count(DISTINCT (machine_id, usuario))
		FROM question_items WHERE question_id = $1`, id).Scan(&t.Items, &t.Keys, &t.Machines, &t.Users)
	return t, err
}

// QuestionItems: os itens de uma chave (até limit, em ordem de máquina e usuário), e quantos há.
func (p *Postgres) QuestionItems(ctx context.Context, id int64, key string, limit int) ([]QuestionItem, int, error) {
	rows, err := p.pool.Query(ctx, `SELECT machine_id, key, name, version, usuario, origem, lugar, nota, count(*) OVER ()
		FROM question_items WHERE question_id = $1 AND key = $2
		ORDER BY machine_id COLLATE "C", lower(usuario), origem, lugar LIMIT $3`, id, key, limit)
	if err != nil {
		return nil, 0, err
	}
	total := 0
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (QuestionItem, error) {
		var it QuestionItem
		err := r.Scan(&it.MachineID, &it.Key, &it.Name, &it.Version, &it.User, &it.Source, &it.Where, &it.Note, &total)
		return it, err
	})
	return list, total, err
}

// EachQuestionItem passa por todos os itens da pergunta, em ordem de máquina (o CSV): um por
// vez, sem montar a lista inteira na memória.
func (p *Postgres) EachQuestionItem(ctx context.Context, id int64, fn func(QuestionItem) error) error {
	rows, err := p.pool.Query(ctx, `SELECT machine_id, key, name, version, usuario, origem, lugar, nota
		FROM question_items WHERE question_id = $1 ORDER BY machine_id COLLATE "C", lower(usuario), origem, lugar, lower(name)`, id)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var it QuestionItem
		if err := rows.Scan(&it.MachineID, &it.Key, &it.Name, &it.Version, &it.User, &it.Source, &it.Where, &it.Note); err != nil {
			return err
		}
		if err := fn(it); err != nil {
			return err
		}
	}
	return rows.Err()
}

// sortVersions põe as versões em ordem (1.9 antes de 1.10).
func sortVersions(v []string) {
	sort.SliceStable(v, func(a, b int) bool { return versionLess(v[a], v[b]) })
}

func versionLess(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		na, ea := strconv.Atoi(pa[i])
		nb, eb := strconv.Atoi(pb[i])
		switch {
		case ea == nil && eb == nil && na != nb:
			return na < nb
		case (ea != nil || eb != nil) && pa[i] != pb[i]:
			return pa[i] < pb[i]
		}
	}
	return len(pa) < len(pb)
}

// --- Memória -------------------------------------------------------------------------

// MachinesWithCapability na memória.
func (m *Memory) MachinesWithCapability(ctx context.Context, capability string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []string
	for id, mm := range m.machines {
		if mm.retiredAt.IsZero() && slices.Contains(mm.capabilities, capability) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out, nil
}

// SaveQuestionItems na memória.
func (m *Memory) SaveQuestionItems(ctx context.Context, jobID int64, machineID string, items []protocol.AnswerItem) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var qid int64
	for _, j := range m.jobs {
		if j.ID == jobID && j.MachineID == machineID {
			qid = j.questionID
		}
	}
	if qid == 0 {
		return nil
	}
	if m.questionItems == nil {
		m.questionItems = map[int64][]QuestionItem{}
	}
	kept := slices.DeleteFunc(slices.Clone(m.questionItems[qid]), func(it QuestionItem) bool { return it.MachineID == machineID })
	for _, it := range items {
		kept = append(kept, QuestionItem{MachineID: machineID, AnswerItem: it})
	}
	m.questionItems[qid] = kept
	return nil
}

func (m *Memory) sortedItems(id int64) []QuestionItem {
	list := slices.Clone(m.questionItems[id])
	sort.SliceStable(list, func(a, b int) bool {
		x, y := list[a], list[b]
		switch {
		case x.MachineID != y.MachineID:
			return x.MachineID < y.MachineID
		case !strings.EqualFold(x.User, y.User):
			return strings.ToLower(x.User) < strings.ToLower(y.User)
		case x.Source != y.Source:
			return x.Source < y.Source
		}
		return x.Where < y.Where
	})
	return list
}

// mostCommon: o texto mais frequente (no empate, o menor).
func mostCommon(count map[string]int) string {
	best, n, first := "", 0, true
	for s, c := range count {
		if first || c > n || c == n && s < best {
			best, n, first = s, c, false
		}
	}
	return best
}

// QuestionItemGroups na memória.
func (m *Memory) QuestionItemGroups(ctx context.Context, id int64, q QuestionItemQuery) ([]QuestionItemGroup, int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	type acc struct {
		g               QuestionItemGroup
		names, notes    map[string]int
		machines, users map[string]bool
	}
	needle := strings.ToLower(q.Text)
	groups := map[string]*acc{}
	for _, it := range m.questionItems[id] {
		if needle != "" && !strings.Contains(strings.ToLower(it.Key+"\x00"+it.Name+"\x00"+it.User+"\x00"+it.Note+"\x00"+it.Source), needle) {
			continue
		}
		a := groups[it.Key]
		if a == nil {
			a = &acc{g: QuestionItemGroup{Key: it.Key}, names: map[string]int{}, notes: map[string]int{}, machines: map[string]bool{}, users: map[string]bool{}}
			groups[it.Key] = a
		}
		a.g.Items++
		a.names[it.Name]++
		a.notes[it.Note]++
		a.machines[it.MachineID] = true
		a.users[it.MachineID+"\x00"+it.User] = true
		if it.Version != "" && !slices.Contains(a.g.Versions, it.Version) {
			a.g.Versions = append(a.g.Versions, it.Version)
		}
		if it.Source != "" && !slices.Contains(a.g.Sources, it.Source) {
			a.g.Sources = append(a.g.Sources, it.Source)
		}
	}
	out := make([]QuestionItemGroup, 0, len(groups))
	for _, a := range groups {
		a.g.Name, a.g.Note, a.g.Machines, a.g.Users = mostCommon(a.names), mostCommon(a.notes), len(a.machines), len(a.users)
		sort.Strings(a.g.Sources)
		sort.Strings(a.g.Versions)
		sortVersions(a.g.Versions)
		out = append(out, a.g)
	}
	sort.Slice(out, func(a, b int) bool {
		x, y := out[a], out[b]
		switch {
		case q.Sort == ItemSortName && !strings.EqualFold(x.Name, y.Name):
			return strings.ToLower(x.Name) < strings.ToLower(y.Name)
		case q.Sort == ItemSortRare && x.Machines != y.Machines:
			return x.Machines < y.Machines
		case q.Sort != ItemSortName && q.Sort != ItemSortRare && x.Machines != y.Machines:
			return x.Machines > y.Machines
		case q.Sort != ItemSortName && x.Name != y.Name:
			return x.Name < y.Name
		}
		return x.Key < y.Key
	})
	total := len(out)
	out = out[min(q.Offset, total):]
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, total, nil
}

// QuestionItemTotals na memória.
func (m *Memory) QuestionItemTotals(ctx context.Context, id int64) (QuestionItemTotals, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	keys, machines, users := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, it := range m.questionItems[id] {
		keys[it.Key], machines[it.MachineID], users[it.MachineID+"\x00"+it.User] = true, true, true
	}
	return QuestionItemTotals{Items: len(m.questionItems[id]), Keys: len(keys), Machines: len(machines), Users: len(users)}, nil
}

// QuestionItems na memória.
func (m *Memory) QuestionItems(ctx context.Context, id int64, key string, limit int) ([]QuestionItem, int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []QuestionItem
	for _, it := range m.sortedItems(id) {
		if it.Key == key {
			out = append(out, it)
		}
	}
	total := len(out)
	if limit > 0 && total > limit {
		out = out[:limit]
	}
	return out, total, nil
}

// EachQuestionItem na memória.
func (m *Memory) EachQuestionItem(ctx context.Context, id int64, fn func(QuestionItem) error) error {
	m.mu.RLock()
	list := m.sortedItems(id)
	m.mu.RUnlock()
	for _, it := range list {
		if err := fn(it); err != nil {
			return err
		}
	}
	return nil
}

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
