package protocol

import (
	"errors"
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
	MaxSelfServiceGroups   = 1024
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
)

// SelfServiceUser: quem pediu, como o token do Windows diz.
type SelfServiceUser struct {
	Account string   `json:"account"` // DOMINIO\usuario
	Groups  []string `json:"groups"`  // DOMINIO\grupo
}

var selfServiceAccount = regexp.MustCompile(`^(?:[^\\\x00-\x1f]{1,64}\\)?[^\\\x00-\x1f]{1,104}$`)

// Check confere o formato (o conteúdo é do agente, que é confiável pela credencial).
func (u SelfServiceUser) Check() error {
	switch {
	case !selfServiceAccount.MatchString(u.Account):
		return errors.New("account fora do formato DOMINIO\\usuario")
	case len(u.Groups) > MaxSelfServiceGroups:
		return errors.New("grupos demais")
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
}

// SelfServiceJob é um pedido do usuário, como o app mostra em "Meus pedidos".
type SelfServiceJob struct {
	ID         int64      `json:"id"`
	App        string     `json:"app"`
	Name       string     `json:"name"`
	Version    string     `json:"version"`
	State      string     `json:"state"`            // o estado do job (pendente, entregue, concluido, falhou, adiado...)
	Detail     string     `json:"detail,omitempty"` // o que o agente informou (curto)
	CreatedAt  time.Time  `json:"created_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// SelfServiceRequest é o pedido do usuário.
type SelfServiceRequest struct {
	SelfServiceUser
	App    string `json:"app"`
	Action string `json:"action"`
	// RebootAt (Aula 17.6): no "reiniciar", a hora escolhida; vazio: agora.
	RebootAt *time.Time `json:"reboot_at,omitempty"`
}

// SelfServiceRebootState é o reinício da máquina como o app mostra (Aula 17.6).
type SelfServiceRebootState struct {
	Pending *bool      `json:"pending,omitempty"` // da última busca; nil: não se sabe
	Allowed bool       `json:"allowed"`           // false: servidor (o reinício é com a TI)
	JobID   int64      `json:"job_id,omitempty"`  // reinício já pedido (por qualquer um), em aberto
	At      *time.Time `json:"at,omitempty"`      // a hora desse reinício, se conhecida
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
	Status  string `json:"status"`
	JobID   int64  `json:"job_id,omitempty"`
	Message string `json:"message"`
}
