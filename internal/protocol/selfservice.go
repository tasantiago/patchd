package protocol

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// Autoatendimento (Aula 17.2): o agente pergunta ao servidor pela loja do usuário que chamou
// o app (17.3) e manda o pedido dele. Quem é o usuário e de quais grupos ele é vem do token
// do Windows de quem chamou o agente (decisão da 17.2: os grupos do token, inclusive os
// aninhados, resolvidos pelo controlador de domínio no logon; nada de consulta ao AD).
const (
	SelfServiceCatalogPath = "/api/v1/agent/selfservice/catalog"
	SelfServiceRequestPath = "/api/v1/agent/selfservice/request"
	// SelfServiceLogoPath (Aula 19.1b): o logotipo do painel, para a Central de software.
	SelfServiceLogoPath  = "/api/v1/agent/selfservice/logo"
	MaxSelfServiceGroups = 1024
)

// Ações que o usuário pede.
const (
	SelfServiceUpdate  = "atualizar"
	SelfServiceInstall = "instalar"
	SelfServiceReboot  = "reiniciar" // Aula 17.6: reiniciar agora ou na hora escolhida
)

// Respostas do pedido.
const (
	SelfServiceCreated = "criado"   // o job foi criado
	SelfServiceRefused = "recusado" // não está disponível para este usuário nesta máquina (agora)
	SelfServiceWait    = "aguarde"  // já há uma instalação em andamento na máquina
	// SelfServiceApproval (Aula 18.3): o pedido foi guardado e espera a TI aprovar.
	SelfServiceApproval = "aprovacao"
)

// SelfServiceUser: quem pediu, como o token do Windows diz.
type SelfServiceUser struct {
	Account string   `json:"account"` // DOMINIO\usuario
	Groups  []string `json:"groups"`  // DOMINIO\grupo
	// SID: o do token (Windows), para a loja achar as instalações por usuário dele e o job
	// por usuário achar a sessão dele. Agentes anteriores não mandam.
	SID string `json:"sid,omitempty"`
}

var selfServiceSID = regexp.MustCompile(`^S-1-[0-9]+(-[0-9]+){1,14}$`)

var selfServiceAccount = regexp.MustCompile(`^(?:[^\\\x00-\x1f]{1,64}\\)?[^\\\x00-\x1f]{1,104}$`)

// Check confere o formato (o conteúdo é do agente, que é confiável pela credencial).
func (u SelfServiceUser) Check() error {
	switch {
	case !selfServiceAccount.MatchString(u.Account):
		return errors.New("account fora do formato DOMINIO\\usuario")
	case len(u.Groups) > MaxSelfServiceGroups:
		return errors.New("grupos demais")
	case u.SID != "" && !selfServiceSID.MatchString(u.SID):
		return errors.New("sid fora do formato")
	}
	for _, g := range u.Groups {
		if g == "" || len(g) > 256 || strings.IndexFunc(g, unicode.IsControl) >= 0 {
			return errors.New("grupo fora do formato")
		}
	}
	return nil
}

// SelfServiceItem é um programa da loja.
type SelfServiceItem struct {
	App       string `json:"app"`
	Name      string `json:"name"`
	Installed string `json:"installed,omitempty"`
	Target    string `json:"target,omitempty"`
	Action    string `json:"action"` // atualizar, instalar, em_dia, instalado, indisponivel
	Reason    string `json:"reason,omitempty"`
	// Aula 18.3: o pedido passa pela TI (Approval); Pending é o pedido deste usuário que já
	// espera a TI para este programa (0: nenhum).
	Approval bool  `json:"approval,omitempty"`
	Pending  int64 `json:"pending,omitempty"`
	// Replaces: a atualização troca a instalação (a regularização): a atual sai e volta na
	// versão nova. Servidores anteriores não mandam.
	Replaces bool `json:"replaces,omitempty"`
	// Category: a categoria na loja (StoreCategories). Servidores anteriores não mandam: a
	// Central mostra a lista sem as categorias.
	Category string `json:"category,omitempty"`
	// PerUser: o programa instala só no perfil de quem pede (o patchd roda o instalador como
	// ele). Servidores anteriores não mandam.
	PerUser bool `json:"per_user,omitempty"`
}

// SelfServiceCatalog é a loja do usuário na máquina.
type SelfServiceCatalog struct {
	Items       []SelfServiceItem `json:"items"`
	GeneratedAt time.Time         `json:"generated_at"`
	OpenJob     int64             `json:"open_job,omitempty"` // instalação em andamento na máquina
	// Requests (Aula 17.5): os pedidos recentes deste usuário nesta máquina, o mais novo
	// primeiro. Agentes até a v0.17.1 não repassam o campo ao app.
	Requests []SelfServiceJob `json:"requests,omitempty"`
	// Reboot (Aula 17.6): o reinício pendente e o já pedido. Agentes até a v0.17.2 não
	// repassam o campo.
	Reboot *SelfServiceRebootState `json:"reboot,omitempty"`
	// Aula 19.1b: o nome do painel (PATCHD_PANEL_TITLE) e o SHA-256 do logotipo enviado pelo
	// admin (vazio: nenhum), para a Central ter a mesma cara. O agente baixa o logotipo por
	// SelfServiceLogoPath quando o SHA-256 muda. Servidores até a 19.1 não mandam.
	Brand string `json:"brand,omitempty"`
	Logo  string `json:"logo,omitempty"`
	// AppName (Aula 20.10): o nome da Central (parâmetro PATCHD_APP_NAME do painel), no título
	// dos avisos do Windows, no ícone e no topo da página. Servidores até a 20.9 não mandam.
	AppName string `json:"app_name,omitempty"`
}

// DefaultAppName e MaxAppName: o nome da Central sem parâmetro, e o limite (Aula 20.10).
const (
	DefaultAppName = "Central de software"
	MaxAppName     = 40
)

// CheckAppName confere o nome da Central: de 1 a MaxAppName caracteres, sem controle nem
// caractere inválido. Vale para o parâmetro do painel e para o que o agente recebe.
func CheckAppName(s string) error {
	n := len([]rune(s))
	if n == 0 || n > MaxAppName || strings.TrimSpace(s) != s {
		return fmt.Errorf("use de 1 a %d caracteres, sem espaço no começo ou no fim", MaxAppName)
	}
	for _, r := range s {
		if unicode.IsControl(r) || r == unicode.ReplacementChar || unicode.Is(unicode.Cf, r) {
			return errors.New("o nome tem caractere de controle ou inválido")
		}
	}
	return nil
}

// SelfServiceJob é um pedido do usuário, como o app mostra em "Meus pedidos".
type SelfServiceJob struct {
	ID         int64      `json:"id"`
	Request    int64      `json:"request,omitempty"` // Aula 18.3: o pedido que esperou (ou espera) a TI; ID 0 se não virou job
	App        string     `json:"app"`
	Name       string     `json:"name"`
	Version    string     `json:"version"`
	State      string     `json:"state"`            // o estado do job (pendente, entregue, concluido, falhou, adiado...)
	Detail     string     `json:"detail,omitempty"` // o que o agente informou (curto)
	CreatedAt  time.Time  `json:"created_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	// CloseAction: o pedido ficou adiado porque o programa estava aberto, e a Central pode
	// oferecer "Fechar o programa e instalar": a ação a pedir de novo, com Close (atualizar ou
	// instalar). Vazio: sem o botão. Servidores anteriores não mandam.
	CloseAction string `json:"close_action,omitempty"`
}

// SelfServiceRequest é o pedido do usuário.
type SelfServiceRequest struct {
	SelfServiceUser
	App    string `json:"app"`
	Action string `json:"action"`
	// RebootAt (Aula 17.6): no "reiniciar", a hora escolhida; vazio: agora.
	RebootAt *time.Time `json:"reboot_at,omitempty"`
	// Close: o usuário pediu para o patchd fechar o programa aberto (na sessão dele) e instalar
	// (o botão "Fechar o programa e instalar" da Central). Servidores anteriores ignoram.
	Close bool `json:"close,omitempty"`
}

// SelfServiceRebootState é o reinício da máquina como o app mostra (Aula 17.6).
type SelfServiceRebootState struct {
	Pending *bool      `json:"pending,omitempty"` // da última busca; nil: não se sabe
	Allowed bool       `json:"allowed"`           // false: servidor (o reinício é com a TI)
	JobID   int64      `json:"job_id,omitempty"`  // reinício já pedido (por qualquer um), em aberto
	At      *time.Time `json:"at,omitempty"`      // a hora desse reinício, se conhecida
	// O porquê e o quê, para a aba Reinício da Central: os motivos (RebootReasons), as
	// atualizações instaladas desde a última inicialização, quem pediu o reinício já marcado
	// e a última inicialização. Servidores e agentes anteriores não mandam.
	Reasons  []string                  `json:"reasons,omitempty"`
	Updates  []SelfServiceRebootUpdate `json:"updates,omitempty"`
	By       string                    `json:"by,omitempty"`
	LastBoot *time.Time                `json:"last_boot,omitempty"`
}

// SelfServiceRebootUpdate é uma atualização instalada que espera o reinício.
type SelfServiceRebootUpdate struct {
	Name   string     `json:"name"`
	Date   *time.Time `json:"date,omitempty"`
	Source string     `json:"source"` // "Windows Update" ou "patchd"
}

// MaxRebootUpdates: quantas atualizações a aba Reinício mostra.
const MaxRebootUpdates = 20

// RebootReasons traduz os sinais de reinício pendente para quem está na máquina, sem
// repetir. Só os sinais que dizem "pendente" contam (o indício fraco também: ele diz o que
// um programa deixou para a próxima inicialização).
func RebootReasons(st RebootStatus) []string {
	var out []string
	add := func(s string) {
		for _, x := range out {
			if x == s {
				return
			}
		}
		out = append(out, s)
	}
	for _, sig := range st.Signals {
		if !sig.Pending {
			continue
		}
		src := strings.ToLower(sig.Source)
		switch {
		case strings.Contains(src, "rebootrequired") || strings.Contains(src, "windowsupdate"):
			add("O Windows Update instalou atualizações que só terminam de valer depois de reiniciar.")
		case strings.Contains(src, "component based servicing"):
			add("Componentes do Windows (a atualização mensal, o .NET ou um recurso do sistema) esperam o reinício para concluir a instalação.")
		case strings.Contains(src, "pendingfilerenameoperations"):
			add("Um programa instalado ou atualizado deixou arquivos para trocar na próxima inicialização.")
		case strings.Contains(src, "reboot-required"):
			add("Pacotes atualizados pedem o reinício para valer.")
		case strings.Contains(src, "kernel"):
			add("Um núcleo (kernel) novo foi instalado e só passa a valer depois de reiniciar.")
		default:
			add("O sistema indica que há uma instalação esperando o reinício.")
		}
	}
	return out
}

// NextClock é a próxima vez que o relógio marca hh:mm, a partir de now (no fuso de now):
// hoje, se ainda não passou; senão, amanhã.
func NextClock(hhmm string, now time.Time) (time.Time, error) {
	t, err := time.Parse("15:04", hhmm)
	if err != nil {
		return time.Time{}, errors.New("use a hora no formato HH:MM, ex.: 18:00")
	}
	at := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location())
	if !at.After(now) {
		at = at.AddDate(0, 0, 1)
	}
	return at, nil
}

// SelfServiceResponse é a resposta ao pedido.
type SelfServiceResponse struct {
	Status    string `json:"status"`
	JobID     int64  `json:"job_id,omitempty"`
	RequestID int64  `json:"request_id,omitempty"` // Aula 18.3: o pedido que espera a TI
	Message   string `json:"message"`
}

// StoreCategory é uma categoria da loja da Central de software: o usuário vê "Todos" e cada
// categoria com programas, e escolhe uma para achar o programa mais rápido.
type StoreCategory struct {
	ID, Label string
}

// StoreCategories: as categorias, na ordem da loja. A última (StoreOther) recebe o que não tem
// categoria.
var StoreCategories = []StoreCategory{
	{"navegadores", "Navegadores"},
	{"escritorio", "Escritório e PDF"},
	{"comunicacao", "Comunicação"},
	{"multimidia", "Imagem, áudio e vídeo"},
	{"desenvolvimento", "Desenvolvimento"},
	{"utilitarios", "Utilitários"},
	{"sistemas", "Sistemas da instituição"},
	{StoreOther, "Outros"},
}

// StoreOther: a categoria de quem não tem outra.
const StoreOther = "outros"

// ValidStoreCategory: o ID é de uma categoria da lista.
func ValidStoreCategory(id string) bool {
	for _, c := range StoreCategories {
		if c.ID == id {
			return true
		}
	}
	return false
}

// StoreCategoryLabel: o nome da categoria ("" para um ID desconhecido).
func StoreCategoryLabel(id string) string {
	for _, c := range StoreCategories {
		if c.ID == id {
			return c.Label
		}
	}
	return ""
}
