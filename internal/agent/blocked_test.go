package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
)

// Tentar assim que fechar: o job adiado por programa aberto fica vigiado; quando o programa
// fecha, o check-in seguinte leva o job em Unblocked e o servidor o marca. O adiado por outro
// motivo (outro instalador rodando) não é vigiado.
func TestTentarAssimQueFechar(t *testing.T) {
	srv, id, cred := novoServidor(t)
	pub, priv, _ := jobs.GenerateKey()
	var buscas atomic.Int32
	a := agenteDeTeste(t, srv, cred, &buscas, &bytes.Buffer{})
	a.MachineID, a.JobKey = id, pub
	ctx := context.Background()
	a.CheckIn(ctx)

	aberto := atomic.Bool{}
	aberto.Store(true)
	var perguntas atomic.Int32
	a.ProgramOpen = func(names []string) (bool, error) {
		perguntas.Add(1)
		if len(names) != 1 || names[0] != "firefox.exe" {
			return false, fmt.Errorf("nomes %v", names)
		}
		return aberto.Load(), nil
	}
	msiOcupado := false
	a.ThirdParty = func(context.Context, int64, jobs.ThirdPartyParams) (string, error) {
		if msiOcupado {
			return "", fmt.Errorf("%w: %w: outra instalação do Windows Installer em andamento", jobs.ErrNotStarted, jobs.ErrRetry)
		}
		return "", fmt.Errorf("%w: %w: %w (20 firefox.exe na sessão 4)", jobs.ErrNotStarted, jobs.ErrRetry, jobs.ErrProgramOpen)
	}
	raw, _ := json.Marshal(jobs.ThirdPartyParams{App: "firefox", Version: "145.0", SHA256: strings.Repeat("ab", 32), Size: 10,
		Installer: "nullsoft", Args: []string{"/S"}, Processes: []string{"firefox.exe"}})
	job := criaJob(t, srv, priv, id, func(j *jobs.Job) { j.Type, j.Params = jobs.TypeThirdParty, raw })
	a.CheckIn(ctx) // recebe, adia, manda o resultado
	if row, _, _ := srv.st.JobForMachine(ctx, id, job); row.State != jobs.StatePostponed {
		t.Fatalf("job: %s", row.State)
	}
	if a.checkBlocked() || len(a.takeUnblocked()) != 0 {
		t.Fatal("ainda aberto: nada a avisar")
	}
	aberto.Store(false)
	if !a.checkBlocked() || fmt.Sprint(a.takeUnblocked()) != fmt.Sprint([]int64{job}) {
		t.Fatalf("fechou: %v", a.takeUnblocked())
	}
	a.CheckIn(ctx) // leva o Unblocked
	if len(a.takeUnblocked()) != 0 {
		t.Error("o check-in aceito tira da lista")
	}
	if n, _ := srv.st.MarkJobsUnblocked(ctx, id, []int64{job}, time.Now()); n != 0 {
		t.Error("o servidor não marcou o job como liberado")
	}

	// Outro instalador rodando: adiado, sem vigiar.
	msiOcupado = true
	job2 := criaJob(t, srv, priv, id, func(j *jobs.Job) { j.Type, j.Params = jobs.TypeThirdParty, raw })
	a.CheckIn(ctx)
	if row, _, _ := srv.st.JobForMachine(ctx, id, job2); row.State != jobs.StatePostponed {
		t.Fatalf("job 2: %s", row.State)
	}
	if len(a.blockedInit().watch) != 0 {
		t.Error("vigiando o adiado por outro instalador")
	}
}
