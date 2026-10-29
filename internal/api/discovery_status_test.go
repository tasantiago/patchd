package api_test

import (
	"context"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
)

func TestDiscoveryLiveProgressAndEmptyResult(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once, closeOnce sync.Once
	t.Cleanup(func() { closeOnce.Do(func() { close(release) }) })
	h, st, _ := ambientePaginas(t, api.WithNetDial(func(ctx context.Context, _ netip.Addr, _ int) bool {
		once.Do(func() { close(started) })
		select {
		case <-release:
		case <-ctx.Done():
		}
		return false
	}), api.WithNetLookup(func(context.Context, string) string { return "" }))
	if _, err := st.AddDiscoveryRange(context.Background(), "192.0.2.10", "", "admin"); err != nil {
		t.Fatal(err)
	}
	pagina(h, "POST", "/painel/descoberta/varrer", sessaoAdmin, url.Values{})
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("scan did not start")
	}
	r := pagina(h, "GET", "/painel/descoberta", sessaoAdmin, nil)
	for _, want := range []string{"Sondando a rede", "descoberta-progresso", "0 de 1", "atualização a cada 3 segundos", "chave de cifragem"} {
		if !strings.Contains(r.Body.String(), want) {
			t.Errorf("running page missing %q", want)
		}
	}
	closeOnce.Do(func() { close(release) })
	deadline := time.Now().Add(5 * time.Second)
	for {
		body := pagina(h, "GET", "/painel/descoberta", sessaoAdmin, nil).Body.String()
		if !strings.Contains(body, "Varrendo…") {
			for _, want := range []string{"Concluída", "não recebeu respostas", "Atividade desta execução", "Diagnóstico", "Nenhuma resposta nas portas"} {
				if !strings.Contains(body, want) {
					t.Errorf("finished page missing %q", want)
				}
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("scan remained running")
		}
		time.Sleep(time.Millisecond)
	}
	runs, err := st.DiscoveryRuns(context.Background(), 1)
	if err != nil || len(runs) != 1 || runs[0].Scanned != 1 || !strings.Contains(runs[0].Note, "firewall") {
		t.Fatalf("missing persisted diagnostic: %+v %v", runs, err)
	}
}

func TestDiscoveryConfigurationBlockedLayout(t *testing.T) {
	h, _, _ := ambientePaginas(t)
	r := pagina(h, "GET", "/painel/configuracao?aba=descoberta", sessaoAdmin, nil)
	for _, want := range []string{"descoberta-campos", "for=\"descoberta-usuario\"", "for=\"descoberta-senha\"", "Como configurar a chave", "discovery keygen", "Instalação bloqueada", "disabled"} {
		if !strings.Contains(r.Body.String(), want) {
			t.Errorf("missing %q", want)
		}
	}
}
