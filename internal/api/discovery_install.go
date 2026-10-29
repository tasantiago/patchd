package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/descoberta"
	"github.com/tasantiago/patchd/internal/identity"
	"github.com/tasantiago/patchd/internal/machinebox"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/store"
)

const discoveryPasswordPurpose = "descoberta-credencial"

func WithDiscoveryKey(key []byte) Option {
	return func(a *api) { a.discoveryKey = append([]byte(nil), key...) }
}

func WithWindowsInstaller(f descoberta.WindowsInstaller) Option {
	return func(a *api) { a.windowsInstaller = f }
}

type discoveryConfigView struct {
	Blocker                        string
	Username                       string
	Enabled, HasPassword, KeyReady bool
}

func (a *api) discoveryConfig(ctx context.Context) (*discoveryConfigView, error) {
	c, err := a.store.DiscoveryCredential(ctx)
	v := &discoveryConfigView{Username: c.Username, Enabled: c.Enabled, HasPassword: len(c.Password) > 0, KeyReady: len(a.discoveryKey) > 0}
	switch {
	case !v.KeyReady:
		v.Blocker = "Instalação bloqueada: a chave de cifragem da credencial não foi configurada no servidor."
	case !v.HasPassword:
		v.Blocker = "Instalação bloqueada: cadastre a conta administrativa e a senha."
	case !v.Enabled:
		v.Blocker = "Instalação automática desligada. A varredura continua funcionando."
	default:
		if e := a.discoveryPrerequisites(); e != nil {
			v.Blocker = e.Error()
		} else {
			plain, e := machinebox.Open(a.discoveryKey, discoveryPasswordPurpose, c.Password)
			clear(plain)
			if e != nil {
				v.Blocker = "A credencial não abre com a chave atual. Cadastre a senha novamente."
			}
		}
	}
	return v, err
}

func (a *api) discoveryCredentialAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	w.Header().Set("Cache-Control", "no-store")
	if len(a.discoveryKey) == 0 {
		http.Error(w, "Configure PATCHD_DISCOVERY_KEY_FILE no servidor.", http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulário inválido", http.StatusBadRequest)
		return
	}
	c, err := a.store.DiscoveryCredential(r.Context())
	if err != nil {
		a.internalError(w, r, "ler credencial de descoberta", err)
		return
	}
	username := strings.TrimSpace(r.PostFormValue("usuario"))
	password := r.PostFormValue("senha")
	enabled := r.PostFormValue("automatico") == "on"
	if username == "" || len(username) > 256 || strings.ContainsAny(username, "\x00\r\n") || len(password) > 1024 ||
		(password == "" && len(c.Password) == 0) || (password == "" && username != c.Username && len(c.Password) > 0) {
		http.Error(w, "Informe usuário e senha ao trocar a conta.", http.StatusUnprocessableEntity)
		return
	}
	if password != "" {
		pub, err := machinebox.Public(a.discoveryKey)
		if err != nil {
			http.Error(w, "chave de descoberta inválida", http.StatusServiceUnavailable)
			return
		}
		sealed, err := machinebox.Seal(pub, discoveryPasswordPurpose, []byte(password))
		if err != nil {
			http.Error(w, "não foi possível cifrar a senha", http.StatusInternalServerError)
			return
		}
		c.Password = sealed
	}
	if enabled && len(c.Password) == 0 {
		http.Error(w, "Informe a senha da conta.", http.StatusUnprocessableEntity)
		return
	}
	if enabled {
		plain, err := machinebox.Open(a.discoveryKey, discoveryPasswordPurpose, c.Password)
		clear(plain)
		if err != nil {
			http.Error(w, "A senha não abre com a chave atual. Cadastre a senha novamente.", http.StatusUnprocessableEntity)
			return
		}
	}
	if enabled {
		if err := a.discoveryPrerequisites(); err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
	}
	c.Username, c.Enabled, c.UpdatedBy = username, enabled, s.Username
	if err := a.store.SetDiscoveryCredential(r.Context(), c); err != nil {
		a.internalError(w, r, "guardar credencial de descoberta", err)
		return
	}
	a.audit(r, s, "descoberta.credencial", "Windows", fmt.Sprintf("instalação automática: %t", enabled))
	http.Redirect(w, r, "/painel/configuracao?aba=descoberta", http.StatusSeeOther)
}

func (a *api) discoveryPrerequisites() error {
	u, err := url.Parse(a.publicURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || strings.ContainsAny(a.publicURL, "\"'\r\n\x00") {
		return fmt.Errorf("Configure PATCHD_PUBLIC_URL com a URL HTTPS alcançável pelas estações.")
	}
	version, err := a.publishedVersion()
	if err != nil || version == "" {
		return fmt.Errorf("Publique uma versão do agente em Configuração › Agente.")
	}
	m, err := a.releases.Manifest(version)
	if err != nil {
		return fmt.Errorf("O manifesto publicado não pôde ser lido.")
	}
	if _, ok := m.For("windows", "amd64"); !ok {
		return fmt.Errorf("A versão publicada precisa conter o agente Windows amd64.")
	}
	return nil
}

// Runs inside the scan lifecycle: a second scan cannot duplicate installations.
func (a *api) installDiscovered(ctx context.Context, hosts []store.DiscoveryHost, by string) {
	report := func(detail string) { a.discovery.report("Concluída", detail) }
	c, err := a.store.DiscoveryCredential(ctx)
	if err != nil {
		a.discovery.report("Falha", "Não foi possível ler a credencial de instalação.")
		return
	}
	if !c.Enabled || len(a.discoveryKey) == 0 {
		report("Varredura concluída. A instalação automática não está habilitada ou falta a chave de cifragem.")
		return
	}
	candidates := 0
	for _, h := range hosts {
		if h.MatchID == "" && h.OSGuess == "windows" {
			candidates++
		}
	}
	if candidates == 0 {
		report("Varredura concluída. Nenhuma estação Windows sem agente foi encontrada para instalação.")
		return
	}
	password, err := machinebox.Open(a.discoveryKey, discoveryPasswordPurpose, c.Password)
	if err != nil {
		a.logger.Error("descoberta: não foi possível abrir a credencial")
		report("Instalação bloqueada: não foi possível abrir a senha. Cadastre a credencial novamente.")
		return
	}
	defer clear(password)
	if err := a.discoveryPrerequisites(); err != nil {
		a.logger.Error("descoberta: instalação indisponível", "reason", err.Error())
		report("Instalação bloqueada: " + err.Error())
		return
	}
	version, _ := a.publishedVersion()
	m, err := a.releases.Manifest(version)
	if err != nil {
		report("Instalação bloqueada: não foi possível ler o manifesto do agente.")
		return
	}
	f, ok := m.For("windows", "amd64")
	if !ok || f.Size <= 0 || f.Size > 100<<20 {
		report("Instalação bloqueada: executável Windows ausente ou tamanho inválido.")
		return
	}
	file, err := a.releases.Open(version, f.Name)
	if err != nil {
		report("Instalação bloqueada: não foi possível abrir o executável Windows.")
		return
	}
	binary, err := io.ReadAll(io.LimitReader(file, (100<<20)+1))
	_ = file.Close()
	sum := sha256.Sum256(binary)
	if err != nil || int64(len(binary)) != f.Size || hex.EncodeToString(sum[:]) != f.SHA256 {
		a.logger.Error("descoberta: executável não confere com o manifesto")
		report("Instalação bloqueada: o executável não confere com o manifesto publicado.")
		return
	}
	install := a.windowsInstaller
	if install == nil {
		install = descoberta.InstallWindows
	}
	for _, h := range hosts {
		if ctx.Err() != nil {
			a.discovery.report("Interrompida", "Limite de tempo de instalação atingido. Alvos pendentes precisam de nova varredura.")
			return
		}
		if h.MatchID != "" || h.OSGuess != "windows" {
			continue
		}
		// Respect a newly disabled range or a credential disabled while scanning.
		current, err := a.store.DiscoveryCredential(ctx)
		if err != nil || !current.Enabled || current.Username != c.Username || string(current.Password) != string(c.Password) {
			report("Novas instalações interrompidas: credencial alterada, desligada ou indisponível.")
			return
		}
		ranges, err := a.store.DiscoveryRanges(ctx)
		if err != nil {
			report("Não foi possível conferir as faixas habilitadas. Novas instalações interrompidas.")
			return
		}
		allowed := false
		for _, r := range ranges {
			if r.Enabled {
				sp, e := descoberta.ParseSpec(r.Spec)
				if e == nil && sp.Contains(h.IP) {
					allowed = true
					break
				}
			}
		}
		if !allowed {
			a.discovery.report("Instalando agentes", h.IP+": ignorado porque a faixa foi desligada ou removida.")
			continue
		}
		// Refresh inventory immediately before dispatch, fail closed on errors.
		known, err := a.knownNetwork(ctx)
		if err != nil {
			report("Não foi possível conferir o inventário. Novas instalações interrompidas.")
			return
		}
		if _, ok := known.byIP[h.IP]; ok {
			continue
		}
		if h.Hostname != "" {
			if _, ok := known.byName[firstLabel(h.Hostname)]; ok {
				continue
			}
		}
		d := store.DiscoveryInstall{IP: h.IP, State: "instalando", At: a.now().UTC(), By: by}
		claimed, err := a.store.ClaimDiscoveryInstall(ctx, d)
		if err != nil {
			report("Não foi possível reservar a instalação no banco.")
			return
		}
		if !claimed {
			a.discovery.report("Instalando agentes", h.IP+": aguardando intervalo de nova tentativa ou instalação em outro servidor.")
			continue
		}
		a.discovery.report("Instalando agentes", h.IP+": conectando ao WinRM HTTPS e instalando o serviço (limite de 3 minutos).")
		plain, hash := identity.NewSecret(identity.EnrollmentPrefix)
		token, err := a.store.CreateEnrollmentToken(ctx, hash, "Descoberta "+h.IP, a.now().Add(10*time.Minute), 1)
		if err != nil {
			d.State = "falha ao criar token"
		} else {
			one, cancel := context.WithTimeout(ctx, 3*time.Minute)
			err = install(one, descoberta.WindowsInstall{IP: h.IP, Username: c.Username, Password: string(password), Server: a.publicURL, Token: plain, SHA256: f.SHA256, Binary: binary, CA: a.agentCA})
			cancel()
			cleanup, done := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			_, revokeErr := a.store.RevokeEnrollmentToken(cleanup, token)
			done()
			if revokeErr != nil {
				a.logger.Error("descoberta: revogar token temporário", "token_id", token)
			}
			d.State = "aguardando registro"
			if err != nil {
				d.State = "falha: verifique WinRM HTTPS, certificado, permissão e serviço"
			}
		}
		d.At = a.now().UTC()
		a.discovery.report("Instalando agentes", h.IP+": "+d.State)
		save, done := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		if err := a.store.SaveDiscoveryInstall(save, d); err != nil {
			a.logger.Error("descoberta: gravar instalação")
		}
		if err := a.store.RecordAudit(save, store.AuditEvent{At: d.At, Username: by, Action: "descoberta.instalar", Target: h.IP, Detail: d.State}); err != nil {
			a.logger.Error("descoberta: gravar auditoria de instalação")
		}
		done()
	}
	report("Processo concluído. Confira o resultado de cada estação em Instalações automáticas; falhas e intervalos de nova tentativa aparecem na atividade.")
}
