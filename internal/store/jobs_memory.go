package store

import (
	"context"
	"sort"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
)

// Jobs na memória: o mesmo comportamento do PostgreSQL, para os testes da API.

type memJob struct {
	JobRow
	signed protocol.SignedJob
}

// NextJobID reserva o próximo ID.
func (m *Memory) NextJobID(ctx context.Context) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.jobSeq++
	return m.jobSeq, nil
}

// CreateJob grava o job pendente.
func (m *Memory) CreateJob(ctx context.Context, j NewJob) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if mm := m.machines[j.MachineID]; mm == nil || !mm.retiredAt.IsZero() {
		return jobs.ErrMachineUnavailable
	}
	m.jobs = append(m.jobs, &memJob{JobRow: JobRow{ID: j.ID, MachineID: j.MachineID, Type: j.Type, State: "pendente",
		CreatedBy: j.CreatedBy, CreatedAt: m.now(), NotBefore: nullTime(j.NotBefore), NotAfter: j.NotAfter}, signed: j.Signed})
	return nil
}

// ExpireJobs marca como expirados os jobs em aberto que passaram do prazo.
func (m *Memory) ExpireJobs(ctx context.Context, now time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for _, j := range m.jobs {
		if (j.State == "pendente" || j.State == "entregue") && !j.NotAfter.After(now) {
			j.State, j.Detail = "expirado", "prazo vencido sem resultado do agente"
			t := now
			j.FinishedAt = &t
			n++
		}
	}
	return n, nil
}

// DeliverJobs entrega os pendentes da máquina, no máximo uma vez.
func (m *Memory) DeliverJobs(ctx context.Context, machineID string, now time.Time) ([]protocol.SignedJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sort.Slice(m.jobs, func(i, k int) bool { return m.jobs[i].ID < m.jobs[k].ID })
	var out []protocol.SignedJob
	for _, j := range m.jobs {
		if len(out) == maxJobsPerCheckin {
			break
		}
		if j.MachineID == machineID && j.State == "pendente" && j.NotAfter.After(now) && (j.NotBefore == nil || !j.NotBefore.After(now)) {
			j.State = "entregue"
			t := now
			j.DeliveredAt = &t
			out = append(out, j.signed)
		}
	}
	return out, nil
}

// JobForMachine devolve o job da máquina.
func (m *Memory) JobForMachine(ctx context.Context, machineID string, id int64) (JobRow, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, j := range m.jobs {
		if j.ID == id && j.MachineID == machineID {
			return j.JobRow, true, nil
		}
	}
	return JobRow{}, false, nil
}

// FinishJob grava o estado final de um job em aberto.
func (m *Memory) FinishJob(ctx context.Context, id int64, state, detail string, at time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, j := range m.jobs {
		if j.ID == id && (j.State == "pendente" || j.State == "entregue") {
			j.State, j.Detail = state, detail
			t := at
			j.FinishedAt = &t
			return true, nil
		}
	}
	return false, nil
}

// ScanReceivedAt devolve quando a busca atual da máquina chegou.
func (m *Memory) ScanReceivedAt(ctx context.Context, machineID string) (*time.Time, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	mm := m.machines[machineID]
	if mm == nil || mm.scan == nil {
		return nil, nil
	}
	t := mm.scanReceivedAt
	return &t, nil
}

// Jobs lista os jobs, do mais novo para o mais antigo.
func (m *Memory) Jobs(ctx context.Context, machineID string, limit int) ([]JobRow, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []JobRow
	for i := len(m.jobs) - 1; i >= 0 && len(out) < limit; i-- {
		if machineID == "" || m.jobs[i].MachineID == machineID {
			out = append(out, m.jobs[i].JobRow)
		}
	}
	sort.Slice(out, func(i, k int) bool { return out[i].ID > out[k].ID })
	return out, nil
}

// CancelJob cancela um job pendente.
func (m *Memory) CancelJob(ctx context.Context, id int64, by string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, j := range m.jobs {
		if j.ID == id && j.State == "pendente" {
			j.State, j.Detail = "cancelado", "cancelado por "+by
			t := m.now()
			j.FinishedAt = &t
			return true, nil
		}
	}
	return false, nil
}
