package api_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/descoberta"
	"github.com/tasantiago/patchd/internal/machinebox"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/release"
	"github.com/tasantiago/patchd/internal/store"
)

func TestDiscoveryAutomaticInstall(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "serviço verificado", true: "permissão recusada"}[fail], func(t *testing.T) {
			ctx := context.Background()
			key, err := machinebox.Generate()
			if err != nil {
				t.Fatal(err)
			}
			rel, exe := versaoPublicada(t)
			calls := make(chan descoberta.WindowsInstall, 4)
			h, st, log := ambientePaginas(t, api.WithDiscoveryKey(key), api.WithPublicURL("https://patchd.exemplo"),
				api.WithReleases(release.Dir(rel), "v0.14.4"),
				api.WithNetDial(func(_ context.Context, ip netip.Addr, port int) bool {
					if ip.As4()[3] == 12 {
						return port == 22
					}
					return port == 5986
				}), api.WithNetLookup(func(context.Context, string) string { return "" }),
				api.WithWindowsInstaller(func(_ context.Context, in descoberta.WindowsInstall) error {
					calls <- in
					if fail {
						return errors.New("erro remoto com segredo " + in.Password + " " + in.Token)
					}
					return nil
				}))
			form := url.Values{"usuario": {"LAB\\deploy"}, "senha": {"senha-super-secreta"}, "automatico": {"on"}}
			path := "/painel/configuracao/descoberta"
			if r := pagina(h, "POST", path, sessaoTeste, form); r.Code != http.StatusForbidden {
				t.Fatalf("perfil leitura: %d", r.Code)
			}
			if r := pagina(h, "POST", path, sessaoAdmin, form); r.Code != http.StatusSeeOther {
				t.Fatalf("salvar: %d %s", r.Code, r.Body)
			}
			c, err := st.DiscoveryCredential(ctx)
			if err != nil || !c.Enabled || bytes.Contains(c.Password, []byte("senha-super-secreta")) {
				t.Fatalf("credencial não cifrada ou não habilitada: %v", err)
			}
			plain, err := machinebox.Open(key, "descoberta-credencial", c.Password)
			if err != nil || string(plain) != "senha-super-secreta" {
				t.Fatal("senha não recuperável")
			}
			page := pagina(h, "GET", "/painel/configuracao?aba=descoberta", sessaoAdmin, nil)
			if page.Code != 200 || strings.Contains(page.Body.String(), "senha-super-secreta") || !strings.Contains(page.Body.String(), "Senha cadastrada") {
				t.Fatal("página de configuração inválida")
			}
			// Empty password preserves ciphertext, changing the username requires a password.
			form.Set("senha", "")
			if r := pagina(h, "POST", path, sessaoAdmin, form); r.Code != 303 {
				t.Fatal("não manteve a senha")
			}
			form.Set("usuario", "outro")
			if r := pagina(h, "POST", path, sessaoAdmin, form); r.Code != 422 {
				t.Fatal("trocou usuário sem senha")
			}
			if _, err := st.AddDiscoveryRange(ctx, "192.0.2.10-12", "", "admin"); err != nil {
				t.Fatal(err)
			}
			if _, err := st.SaveInventory(ctx, "known", protocol.InventoryReport{SchemaVersion: 1, Hash: "known", CollectedAt: time.Now().UTC(), OS: protocol.OSInfo{Family: "windows", Hostname: "known"}}); err != nil {
				t.Fatal(err)
			}
			if err := st.SaveHostState(ctx, "known", &protocol.MachineState{CollectedAt: time.Now().UTC(),
				Interfaces: []protocol.NetInterface{{Name: "eth0", Up: true, IPv4: []string{"192.0.2.11/24"}}}}, "192.0.2.11"); err != nil {
				t.Fatal(err)
			}
			if _, err := st.AddDiscoveryRange(ctx, "192.0.2.20", "", "admin"); err != nil {
				t.Fatal(err)
			}
			ranges, _ := st.DiscoveryRanges(ctx)
			for _, r := range ranges {
				if r.Spec == "192.0.2.20" {
					st.SetDiscoveryRangeEnabled(ctx, r.ID, false)
				}
			}
			if r := pagina(h, "POST", "/painel/descoberta/varrer", sessaoAdmin, url.Values{}); r.Code != 303 {
				t.Fatal(r.Code)
			}
			for range 1 {
				select {
				case in := <-calls:
					if in.IP != "192.0.2.10" {
						t.Fatalf("instalou fora dos alvos: %s", in.IP)
					}
					if !bytes.Equal(in.Binary, exe) || in.Username != "LAB\\deploy" || in.Password != "senha-super-secreta" || in.Token == "" {
						t.Fatal("payload incorreto")
					}
				case <-time.After(5 * time.Second):
					t.Fatal("instalador não foi chamado")
				}
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				list, _ := st.DiscoveryInstalls(ctx)
				finished := len(list) == 1
				for _, d := range list {
					if d.State == "instalando" {
						finished = false
					}
				}
				if finished {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("instalação não terminou")
				}
				time.Sleep(10 * time.Millisecond)
			}
			tokens, _ := st.EnrollmentTokens(ctx)
			if len(tokens) != 1 {
				t.Fatalf("tokens: %d", len(tokens))
			}
			for _, tok := range tokens {
				if tok.MaxUses != 1 || tok.Status(time.Now()) == "válido" {
					t.Fatal("token temporário não revogado")
				}
			}
			events, _ := st.AuditEvents(ctx, store.AuditFilter{Action: "descoberta.instalar"})
			if len(events) != 1 {
				t.Fatalf("auditoria: %d", len(events))
			}
			body := pagina(h, "GET", "/painel/descoberta", sessaoAdmin, nil).Body.String()
			want := "aguardando registro"
			if fail {
				want = "falha: verifique WinRM"
			}
			if !strings.Contains(body, want) || strings.Contains(body, "senha-super-secreta") || strings.Contains(log.String(), "senha-super-secreta") {
				t.Fatal("resultado ou sigilo incorreto")
			}
			// A subsequent scan must not immediately retry either successes or failures.
			for strings.Contains(pagina(h, "GET", "/painel/descoberta", sessaoAdmin, nil).Body.String(), "Varrendo…") {
				if time.Now().After(deadline) {
					t.Fatal("varredura não terminou")
				}
				time.Sleep(time.Millisecond)
			}
			pagina(h, "POST", "/painel/descoberta/varrer", sessaoAdmin, url.Values{})
			time.Sleep(50 * time.Millisecond)
			select {
			case <-calls:
				t.Fatal("tentativa duplicada")
			default:
			}
		})
	}
}

func TestDiscoveryCredentialPrerequisites(t *testing.T) {
	key, _ := machinebox.Generate()
	for _, withKey := range []bool{false, true} {
		var opts []api.Option
		if withKey {
			opts = append(opts, api.WithDiscoveryKey(key))
		}
		h, st, _ := ambientePaginas(t, opts...)
		r := pagina(h, "POST", "/painel/configuracao/descoberta", sessaoAdmin, url.Values{"usuario": {"LAB\\deploy"}, "senha": {"segredo"}, "automatico": {"on"}})
		want := 503
		if withKey {
			want = 422
		}
		if r.Code != want {
			t.Fatalf("sem requisitos: %d", r.Code)
		}
		c, _ := st.DiscoveryCredential(context.Background())
		if c.Enabled {
			t.Fatal("habilitou sem requisitos")
		}
	}
}
