// Package userapp é o app do usuário (Aula 17.4): roda na sessão do usuário, mostra um ícone
// perto do relógio e abre uma página local no navegador com o que o patchd oferece a ele.
// Tudo passa pelo canal local do agente (17.3); o app não fala com o servidor e não guarda
// credencial nenhuma. Quem pede é sempre o dono do processo, pelo token do Windows.
//
// A página é servida em 127.0.0.1, numa porta escolhida pelo sistema, sob um caminho com um
// segredo gerado a cada sessão (/s/SEGREDO/). Sem o segredo, nada responde: outro usuário da
// mesma máquina (servidor de terminal) ou uma página da internet aberta no navegador não
// conseguem pedir nada em nome de quem está logado.
package userapp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/tasantiago/patchd/internal/localapi"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/ui"
)

// Caller é o canal local (localapi.Call); os testes o substituem.
type Caller func(ctx context.Context, req localapi.Request) (localapi.Response, error)

// Title é o nome que o usuário vê, na página e no ícone.
const Title = "Central de software"

// App é a página local do usuário.
type App struct {
	Call  Caller
	Token string // o segredo do caminho
	Addr  string // 127.0.0.1:porta, para conferir Host e Origin
	Now   func() time.Time
	Loc   *time.Location // fuso das datas da página; nil: o do computador
	// Version é a versão do agente, no selo da barra superior (Aula 19.1b); vazio: sem selo.
	Version string

	mu    sync.Mutex
	flash *flash
	// Fila do "Atualizar tudo" (Aula 19.1b, queue.go).
	life         context.Context // o do Serve: a fila para quando a Central fecha
	queue        []string
	queueCurrent string
	queueFailed  []string
	queueRunning bool
	queueCheck   time.Duration // QueueCheck; os testes encurtam
	// O logotipo do painel (Aula 19.1b), como o agente entregou e a página conferiu.
	logoSHA, logoType string
	logoData          []byte

	kickOnce      sync.Once
	kick          chan struct{}
	fast          time.Duration // FastCheck; os testes encurtam
	approvalEvery time.Duration // ApprovalCheck; os testes encurtam
}

type flash struct {
	Kind string // sucesso, atencao, erro
	Text string
}

// New monta o app com um segredo novo.
func New(call Caller) *App {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return &App{Call: call, Token: base64.RawURLEncoding.EncodeToString(b), Now: time.Now}
}

// Listen abre a porta local (só 127.0.0.1; a porta é escolhida pelo sistema).
func (a *App) Listen() (net.Listener, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	a.Addr = ln.Addr().String()
	return ln, nil
}

// URL é o endereço da página, com o segredo.
func (a *App) URL() string { return "http://" + a.Addr + a.base() }

func (a *App) base() string { return "/s/" + a.Token + "/" }

// TabURL é o endereço de uma aba ("": a padrão).
func (a *App) TabURL(tab string) string {
	if tab == "" {
		return a.URL()
	}
	return a.URL() + "?aba=" + tab
}

// Serve atende até o contexto acabar.
func (a *App) Serve(ctx context.Context, ln net.Listener) error {
	a.mu.Lock()
	a.life = ctx
	a.mu.Unlock()
	srv := &http.Server{Handler: a.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 2 * time.Minute, MaxHeaderBytes: 16 << 10}
	// #nosec G118 -- o ctx já acabou; o desligamento precisa de um prazo próprio
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	err := srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Handler confere o Host, o segredo e a origem dos formulários antes de qualquer coisa.
func (a *App) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src 'self' data:; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		// O segredo está no caminho: same-origin só manda o Referer para a própria página. Aula
		// 17.5b: com no-referrer, o navegador manda "Origin: null" nos formulários (é a regra do
		// Fetch para essa política), e todo pedido feito pela página era recusado como de fora.
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Cache-Control", "no-store")
		// Host: só o endereço em que o app escuta (contra DNS rebinding: um site que aponta
		// o próprio nome para 127.0.0.1 manda outro Host).
		if a.Addr != "" && r.Host != a.Addr {
			http.Error(w, "não encontrado", http.StatusNotFound)
			return
		}
		rest, ok := strings.CutPrefix(r.URL.Path, "/s/")
		tok, sub, _ := strings.Cut(rest, "/")
		if !ok || subtle.ConstantTimeCompare([]byte(tok), []byte(a.Token)) != 1 {
			http.Error(w, "não encontrado", http.StatusNotFound)
			return
		}
		switch {
		case sub == "" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
			a.page(w, r)
		case sub == "logo" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
			a.logo(w, r)
		case sub == "pedir" && r.Method == http.MethodPost:
			if !a.sameOrigin(r) {
				http.Error(w, "origem recusada", http.StatusForbidden)
				return
			}
			a.request(w, r)
		case (sub == "atualizar-tudo" || sub == "cancelar-fila") && r.Method == http.MethodPost: // Aula 19.1b
			if !a.sameOrigin(r) {
				http.Error(w, "origem recusada", http.StatusForbidden)
				return
			}
			if sub == "atualizar-tudo" {
				a.updateAll(w, r)
			} else {
				a.cancelAll(w, r)
			}
		default:
			http.Error(w, "não encontrado", http.StatusNotFound)
		}
	})
}

// sameOrigin: o formulário tem de vir da própria página. O navegador manda Origin num POST;
// sem ela, vale o Referer (só se for desta página).
func (a *App) sameOrigin(r *http.Request) bool {
	want := "http://" + a.Addr
	if o := r.Header.Get("Origin"); o != "" {
		return o == want
	}
	ref, err := url.Parse(r.Referer())
	return err == nil && ref.Scheme+"://"+ref.Host == want
}

// Abas da página: Atualizações (17.4), Loja e Meus pedidos (17.5).
const (
	tabUpdates  = "atualizacoes"
	tabStore    = "loja"
	tabRequests = "pedidos"
	tabReboot   = "reinicio" // Aula 17.6
)

type tab struct {
	ID, Label string // o ícone sai do ID (template "icone"; Aula 20.2: sem template.HTML)
	Href      string
	Count     int  // um número ao lado do rótulo (atualizações disponíveis); 0: nada
	Alert     bool // um "!" ao lado do rótulo (reinício pendente sem hora escolhida)
	Active    bool
}

// rebootView é a aba Reinício (Aula 17.6).
type rebootView struct {
	Allowed bool
	Known   bool // a máquina sabe dizer se há reinício pendente
	Pending bool
	JobID   int64
	At      string // "18:00"; vazio: em breve (hora ainda não conhecida)
	Options []rebootOption
}

type rebootOption struct {
	Value, Label string
	First        bool
}

// rebootOptions são as horas que o usuário pode escolher: agora, daqui a 1, 2 e 4 horas, e o
// almoço e o fim do expediente de hoje, se ainda não passaram (até 12 h à frente).
func rebootOptions(now time.Time) []rebootOption {
	type cand struct {
		at    time.Time
		label string
	}
	var cs []cand
	for _, h := range []int{1, 2, 4} {
		at := now.Add(time.Duration(h) * time.Hour)
		label := fmt.Sprintf("Daqui a %d hora", h)
		if h > 1 {
			label += "s"
		}
		cs = append(cs, cand{at, label + " (às " + at.Format("15:04") + ")"})
	}
	for _, c := range []struct {
		h     int
		label string
	}{{12, "Na hora do almoço (12:00)"}, {18, "No fim do expediente (18:00)"}} {
		at := time.Date(now.Year(), now.Month(), now.Day(), c.h, 0, 0, 0, now.Location())
		if d := at.Sub(now); d >= 15*time.Minute && d <= 12*time.Hour {
			cs = append(cs, cand{at, c.label})
		}
	}
	slices.SortFunc(cs, func(a, b cand) int { return a.at.Compare(b.at) })
	out := []rebootOption{{Value: "agora", Label: "Agora (com 2 minutos para salvar o trabalho)", First: true}}
	for _, c := range cs {
		out = append(out, rebootOption{Value: fmt.Sprint(c.at.Unix()), Label: c.label})
	}
	return out
}

// Item é um programa como a página mostra.
type Item struct {
	App, Name, Installed, Target, Reason string
	Approval                             bool  // Aula 18.4: o pedido passa pela TI
	Pending                              int64 // o pedido deste usuário que já espera a TI
	// Aula 19.1b: na fila do "Atualizar tudo" (Queued) ou pedido por ela e instalando (Running).
	Queued, Running bool
}

// reqView é um pedido em "Meus pedidos".
type reqView struct {
	ID                   int64
	Request              int64 // Aula 18.4: o pedido à TI (ID 0: não virou job)
	Name, Version, When  string
	Label, Class, Detail string
}

type pageData struct {
	Title, Base, Account, Error, Checked, Tab string
	Tabs                                      []tab
	// Aula 19.1b: o layout do painel. Tabs são as do grupo Software; Computer, a do Reinício.
	Computer               []tab
	Brand, Logo, Version   string
	User, Domain, Initials string
	TabLabel               string
	// Aula 19.1b: o "Atualizar tudo" (com 2 ou mais atualizações que ainda dá para pedir) e a fila.
	UpdateAll                       int
	Queue                           queueState
	Flash                           *flash
	Updates, Current                []Item
	Install, Installed, Unavailable []Item
	Confirm                         *Item
	Requests                        []reqView
	Reboot                          *rebootView
	Busy                            int64
}

func (a *App) page(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	d := pageData{Title: Title, Base: a.base(), Checked: a.now().Format("02/01 15:04"), Tab: r.URL.Query().Get("aba")}
	if d.Tab != tabStore && d.Tab != tabRequests && d.Tab != tabReboot {
		d.Tab = tabUpdates
	}
	a.mu.Lock()
	d.Flash, a.flash = a.flash, nil
	a.mu.Unlock()

	resp, err := a.Call(ctx, localapi.Request{Op: localapi.OpCatalog})
	refresh := false
	switch {
	case err != nil:
		d.Error = "Não foi possível falar com o patchd neste computador: " + err.Error()
	case !resp.OK:
		d.Error = userError(resp)
	default:
		d.Account = resp.Account
		d.User, d.Domain, d.Initials = splitAccount(resp.Account)
		d.Brand = resp.Catalog.Brand
		a.syncLogo(ctx, resp.Catalog.Logo)
		d.Busy = resp.Catalog.OpenJob
		refresh = d.Busy > 0
		for _, it := range resp.Catalog.Items {
			x := Item{App: it.App, Name: it.Name, Installed: it.Installed, Target: it.Target, Reason: it.Reason,
				Approval: it.Approval, Pending: it.Pending}
			switch it.Action {
			case protocol.SelfServiceUpdate:
				d.Updates = append(d.Updates, x)
			case "em_dia":
				d.Current = append(d.Current, x)
			case protocol.SelfServiceInstall:
				d.Install = append(d.Install, x)
				if r.URL.Query().Get("confirmar") == it.App {
					d.Confirm = &x
				}
			case "instalado":
				d.Installed = append(d.Installed, x)
			case "indisponivel":
				d.Unavailable = append(d.Unavailable, x)
			}
		}
		for _, j := range resp.Catalog.Requests {
			v := reqView{ID: j.ID, Request: j.Request, Name: j.Name, Version: j.Version, When: j.CreatedAt.In(a.loc()).Format("02/01 15:04")}
			v.Label, v.Class = stateLabel(j.State)
			switch j.State {
			case "concluido", "pendente", "entregue", "aguardando aprovação":
			case "adiado": // o programa estava aberto: o pedido não fica na fila sozinho
				v.Detail = "Feche o programa e peça de novo. (" + j.Detail + ")"
			default:
				v.Detail = j.Detail
			}
			if j.State == "pendente" || j.State == "entregue" {
				refresh = true
			}
			d.Requests = append(d.Requests, v)
		}
		if rb := resp.Catalog.Reboot; rb != nil {
			v := &rebootView{Allowed: rb.Allowed, Known: rb.Pending != nil, Pending: rb.Pending != nil && *rb.Pending, JobID: rb.JobID}
			if rb.At != nil {
				v.At = rb.At.In(a.loc()).Format("15:04")
			}
			if v.Allowed && v.Pending && v.JobID == 0 {
				v.Options = rebootOptions(a.now().In(a.loc()))
			}
			d.Reboot = v
		}
	}
	d.Tabs = []tab{
		{ID: tabUpdates, Label: "Atualizações", Count: len(d.Updates)},
		{ID: tabStore, Label: "Loja"},
		{ID: tabRequests, Label: "Meus pedidos"},
	}
	if d.Reboot != nil { // agentes até a v0.17.2 não trazem o reinício
		d.Computer = []tab{{ID: tabReboot, Label: "Reinício", Alert: d.Reboot.Options != nil}}
	} else if d.Tab == tabReboot && d.Error == "" {
		d.Tab = tabUpdates
	}
	for _, l := range [][]tab{d.Tabs, d.Computer} {
		for i := range l {
			l[i].Href = a.base() + "?aba=" + l[i].ID
			if l[i].Active = l[i].ID == d.Tab; l[i].Active {
				d.TabLabel = l[i].Label
			}
		}
	}
	d.Version = a.Version
	d.Queue = a.queueView()
	for i := range d.Updates {
		it := &d.Updates[i]
		it.Queued = slices.Contains(d.Queue.Waiting, it.App)
		it.Running = it.App == d.Queue.Current
		if !it.Queued && !it.Running && it.Pending == 0 {
			d.UpdateAll++
		}
	}
	if d.UpdateAll < 2 {
		d.UpdateAll = 0
	}
	if len(d.Queue.Waiting) > 0 || d.Queue.Current != "" {
		refresh = true // a página acompanha a fila sozinha
	}
	a.mu.Lock()
	if a.logoSHA != "" {
		d.Logo = a.base() + "logo?v=" + a.logoSHA[:16]
	}
	a.mu.Unlock()
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	if refresh {
		h.Set("Refresh", "15") // a página acompanha a instalação sozinha (sem JavaScript)
	}
	_ = pageTmpl.Execute(w, d)
}

// splitAccount separa DOMINIO\usuario e tira as iniciais do usuário (o painel não guarda foto).
func splitAccount(acc string) (user, domain, initials string) {
	user = acc
	if i := strings.LastIndexByte(acc, '\\'); i >= 0 {
		domain, user = acc[:i], acc[i+1:]
	}
	words := strings.FieldsFunc(user, func(r rune) bool { return unicode.IsSpace(r) || r == '.' || r == '_' || r == '-' })
	var out []rune
	for i, w := range words {
		if i != 0 && i != len(words)-1 {
			continue
		}
		if r := []rune(w); len(r) > 0 && unicode.IsLetter(r[0]) {
			out = append(out, unicode.ToUpper(r[0]))
		}
	}
	if len(out) == 0 {
		out = []rune{'?'}
	}
	return user, domain, string(out)
}

// syncLogo guarda o logotipo que o agente anuncia na loja (want: o SHA-256). O agente já
// conferiu; a página confere de novo antes de servir (o canal é local, mas não custa).
func (a *App) syncLogo(ctx context.Context, want string) {
	a.mu.Lock()
	have := a.logoSHA
	if want == "" {
		a.logoSHA, a.logoType, a.logoData = "", "", nil
	}
	a.mu.Unlock()
	if want == "" || want == have {
		return
	}
	resp, err := a.Call(ctx, localapi.Request{Op: localapi.OpLogo})
	if err != nil || !resp.OK || len(resp.Logo) == 0 {
		return
	}
	sum := sha256.Sum256(resp.Logo)
	ct, _, _, err := ui.CheckLogo(resp.Logo)
	if hex.EncodeToString(sum[:]) != want || err != nil {
		return
	}
	a.mu.Lock()
	a.logoSHA, a.logoType, a.logoData = want, ct, resp.Logo
	a.mu.Unlock()
}

// logo serve o logotipo guardado (Aula 19.1b).
func (a *App) logo(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	data, ct := a.logoData, a.logoType
	a.mu.Unlock()
	if data == nil {
		http.Error(w, "não encontrado", http.StatusNotFound)
		return
	}
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("Cache-Control", "private, max-age=86400")
	h.Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

// stateLabel é o estado do job em palavras do usuário, com a cor.
func stateLabel(s string) (label, class string) {
	switch s {
	case "pendente":
		return "Na fila", "andamento"
	case "aguardando aprovação": // Aula 18.4: os pedidos que passam pela TI
		return "Com a TI", "atencao"
	case "recusado pela TI":
		return "Recusado pela TI", "erro"
	case "expirado sem aprovação":
		return "Expirou sem resposta da TI", "neutro"
	case "entregue":
		return "Instalando", "andamento"
	case "concluido":
		return "Concluído", "sucesso"
	case "adiado":
		return "Adiado", "atencao"
	case "falhou":
		return "Não deu certo", "erro"
	case "rejeitado":
		return "Recusado pelo computador", "erro"
	case "cancelado":
		return "Cancelado pelo suporte", "neutro"
	case "expirado":
		return "Expirou (o computador ficou desligado)", "neutro"
	case "dispensado":
		return "Não foi necessário", "neutro"
	default:
		return s, "neutro"
	}
}

func (a *App) now() time.Time {
	if a.Now == nil {
		return time.Now()
	}
	return a.Now()
}

func (a *App) loc() *time.Location {
	if a.Loc == nil {
		return time.Local
	}
	return a.Loc
}

func (a *App) request(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulário inválido", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	req := localapi.Request{Op: localapi.OpRequest, App: r.PostForm.Get("app"), Action: r.PostForm.Get("action")}
	back := tabUpdates
	switch req.Action {
	case protocol.SelfServiceInstall:
		back = tabStore
	case protocol.SelfServiceReboot:
		back = tabReboot
		at, ok := a.rebootAt(r.PostForm.Get("quando"))
		if !ok {
			a.setFlash(flash{"erro", "Escolha uma das horas da lista."})
			http.Redirect(w, r, a.base()+"?aba="+back, http.StatusSeeOther)
			return
		}
		req.At = at
	}
	resp, err := a.Call(ctx, req)
	var f flash
	switch {
	case err != nil:
		f = flash{"erro", "Não foi possível falar com o patchd neste computador: " + err.Error()}
	case !resp.OK:
		f = flash{"erro", userError(resp)}
	case resp.Result.Status == protocol.SelfServiceApproval: // Aula 18.4: guardado para a TI
		f = flash{"sucesso", resp.Result.Message}
		back = tabRequests
		a.kickWatch()
	case resp.Result.Status == protocol.SelfServiceCreated:
		f = flash{"sucesso", resp.Result.Message}
		if back != tabReboot {
			back = tabRequests // o pedido aceito é acompanhado em Meus pedidos
		}
		a.kickWatch() // e o aviso de "terminou" vem do ícone
	default:
		f = flash{"atencao", resp.Result.Message}
	}
	a.setFlash(f)
	// Recarregar a página não repete o pedido (POST, depois GET).
	http.Redirect(w, r, a.base()+"?aba="+back, http.StatusSeeOther)
}

func (a *App) setFlash(f flash) {
	a.mu.Lock()
	a.flash = &f
	a.mu.Unlock()
}

// rebootAt lê a hora escolhida: "agora" (nil) ou um instante da lista (segundos Unix), entre
// 1 minuto e 12 horas à frente. O servidor confere de novo.
func (a *App) rebootAt(v string) (*time.Time, bool) {
	if v == "agora" {
		return nil, true
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return nil, false
	}
	at := time.Unix(n, 0).UTC()
	if d := at.Sub(a.now()); d < time.Minute || d > 12*time.Hour {
		return nil, false
	}
	return &at, true
}

// userError traduz o erro do canal para quem está na frente do computador.
func userError(r localapi.Response) string {
	switch r.Code {
	case localapi.CodeOff:
		return "O autoatendimento está desligado. Fale com o suporte."
	case localapi.CodeUnavailable:
		return "O servidor de atualizações não respondeu. Tente de novo em alguns minutos."
	case localapi.CodeLimit, localapi.CodeBusy:
		return sentence(r.Error)
	default:
		return "Não deu certo (" + r.Error + "). Se continuar, fale com o suporte."
	}
}

// sentence: primeira letra maiúscula e ponto final.
func sentence(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return ""
	}
	r[0] = unicode.ToUpper(r[0])
	return string(r) + "."
}

// Notice é o resumo para o aviso do ícone.
type Notice struct {
	Updates []string // nomes dos programas com atualização para o usuário
	Done    []string // pedidos que terminaram (17.5): "7-Zip 26.04: instalado"
	Reboot  bool     // reinício pendente sem hora escolhida (17.6)
}

// Tab é a aba que o clique no aviso abre.
func (n Notice) Tab() string {
	switch {
	case len(n.Done) > 0:
		return tabRequests
	case n.Reboot:
		return tabReboot
	}
	return tabUpdates
}

// noticeText é o título e o texto do balão. O fim de um pedido vem antes: é a resposta ao
// que o usuário acabou de pedir.
func noticeText(n Notice) (title, text string) {
	switch {
	case len(n.Done) == 1 && (strings.Contains(n.Done[0], "pela TI") || strings.Contains(n.Done[0], "da TI")):
		return "Resposta da TI", n.Done[0] + "."
	case len(n.Done) == 1:
		return "Pedido concluído", n.Done[0] + "."
	case len(n.Done) > 1:
		return "Pedidos concluídos", strings.Join(n.Done, "; ") + "."
	case n.Reboot:
		return "Reinício pendente", "Para concluir as atualizações, este computador precisa reiniciar. Clique para escolher a hora."
	}
	switch len(n.Updates) {
	case 0:
		return "", ""
	case 1:
		return "Atualização disponível", n.Updates[0] + " tem uma versão nova para você. Clique para atualizar."
	default:
		return "Atualizações disponíveis", fmt.Sprintf("%d programas têm versão nova para você: %s. Clique para ver.",
			len(n.Updates), strings.Join(n.Updates, ", "))
	}
}

// FastCheck: com pedido em andamento, a conferência fica mais frequente, para o aviso de
// "terminou" chegar logo.
const FastCheck = 30 * time.Second

// ApprovalCheck (Aula 18.4): com pedido esperando a TI, a conferência é a cada 5 minutos —
// a decisão leva horas; não precisa de 30 s.
const ApprovalCheck = 5 * time.Minute

// doneText é como o fim do pedido aparece no balão.
func doneText(j protocol.SelfServiceJob) string {
	what := strings.TrimSpace(j.Name + " " + j.Version)
	switch j.State {
	case "concluido":
		return what + ": pronto"
	case "adiado":
		return what + ": adiado (feche o programa e peça de novo)"
	case "dispensado":
		return what + ": não foi necessário"
	case "recusado pela TI": // Aula 18.4
		if j.Detail != "" {
			return what + ": recusado pela TI (" + strings.TrimPrefix(j.Detail, "motivo: ") + ")"
		}
		return what + ": recusado pela TI"
	case "expirado sem aprovação":
		return what + ": expirou sem resposta da TI"
	default:
		return what + ": não deu certo (veja em Meus pedidos)"
	}
}

// Watch confere a loja depois de first e a cada every (FastCheck com pedido em andamento,
// ou logo depois de um pedido aceito) e avisa: atualização nova para o usuário (a mesma
// lista não é repetida), pedido que terminou e reinício pendente sem hora escolhida (a cada
// every, enquanto durar).
func (a *App) Watch(ctx context.Context, first, every time.Duration, out chan<- Notice) {
	fast := a.fast
	if fast == 0 {
		fast = FastCheck
	}
	wait := first
	var last []string
	open := map[int64]bool{} // pedidos em andamento na última conferência
	approval := a.approvalEvery
	if approval == 0 {
		approval = ApprovalCheck
	}
	waiting := map[int64]protocol.SelfServiceJob{} // Aula 18.4: pedidos com a TI na última conferência
	var rebootSaid time.Time                       // último aviso de reinício pendente (repete a cada every)
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		case <-a.kicked():
		}
		wait = every
		cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		resp, err := a.Call(cctx, localapi.Request{Op: localapi.OpCatalog})
		cancel()
		if err != nil || !resp.OK {
			continue
		}
		var n Notice
		var ids []string
		for _, it := range resp.Catalog.Items {
			if it.Action == protocol.SelfServiceUpdate {
				ids, n.Updates = append(ids, it.App), append(n.Updates, it.Name)
			}
		}
		if slices.Equal(ids, last) {
			n.Updates = nil
		}
		last = ids
		now := map[int64]bool{}
		for _, j := range resp.Catalog.Requests {
			switch {
			case j.State == "pendente" || j.State == "entregue":
				now[j.ID] = true
			case open[j.ID]:
				n.Done = append(n.Done, doneText(j))
			}
		}
		open = now
		// Aula 18.4: o pedido que estava com a TI e saiu da fila. Se ele ainda aparece, foi
		// recusado ou expirou; se sumiu, virou job (aprovado) — o job entra no "open" acima.
		waitNow := map[int64]protocol.SelfServiceJob{}
		for _, j := range resp.Catalog.Requests {
			if j.ID == 0 && j.Request > 0 && j.State == "aguardando aprovação" {
				waitNow[j.Request] = j
			}
		}
		for id, prev := range waiting {
			if _, still := waitNow[id]; still {
				continue
			}
			i := slices.IndexFunc(resp.Catalog.Requests, func(j protocol.SelfServiceJob) bool { return j.ID == 0 && j.Request == id })
			if i >= 0 {
				n.Done = append(n.Done, doneText(resp.Catalog.Requests[i]))
			} else {
				n.Done = append(n.Done, strings.TrimSpace(prev.Name+" "+prev.Version)+": aprovado pela TI; a instalação começa em instantes")
			}
		}
		waiting = waitNow
		if rb := resp.Catalog.Reboot; rb != nil && rb.Allowed && rb.Pending != nil && *rb.Pending && rb.JobID == 0 &&
			time.Since(rebootSaid) >= every-time.Minute {
			n.Reboot, rebootSaid = true, time.Now()
		}
		if len(waitNow) > 0 && wait > approval {
			wait = approval
		}
		if len(now) > 0 || resp.Catalog.OpenJob > 0 {
			wait = fast
		}
		if len(n.Updates) > 0 || len(n.Done) > 0 || n.Reboot {
			select {
			case out <- n:
			default: // o ícone ainda não leu o anterior
			}
		}
	}
}

// kick: um pedido aceito pela página antecipa a próxima conferência.
func (a *App) kickChan() chan struct{} {
	a.kickOnce.Do(func() { a.kick = make(chan struct{}, 1) })
	return a.kick
}

func (a *App) kicked() <-chan struct{} { return a.kickChan() }

func (a *App) kickWatch() {
	select {
	case a.kickChan() <- struct{}{}:
	default:
	}
}

var pageTmpl = template.Must(template.New("p").Parse(`<!doctype html>
<html lang="pt-BR"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{if .TabLabel}}{{.TabLabel}} · {{end}}{{.Title}}</title><style>` + ui.LayoutCSS + css + `</style></head><body>
<input type="checkbox" id="menu" class="menu-chk" aria-label="recolher ou abrir o menu lateral">
<aside class="lado" aria-label="menu lateral">
<a class="marca" href="{{.Base}}" title="{{.Title}}">{{if .Logo}}<img src="{{.Logo}}" alt="" width="36" height="36">{{else}}<span class="escudo">` + ui.ShieldSVG + `</span>{{end}}<span class="rot">{{.Title}}{{if .Brand}}<small>{{.Brand}}</small>{{end}}</span></a>
{{if .Account}}<div class="usuario" title="{{.Account}}"><span class="avatar" aria-hidden="true">{{.Initials}}</span><span class="rot"><strong>{{.User}}</strong><small>{{if .Domain}}{{.Domain}} · {{end}}este computador</small></span></div>{{end}}
<nav aria-label="seções">
<p class="grupo"><span class="rot">Software</span></p>
{{range .Tabs}}{{template "item" .}}{{end}}
{{if .Computer}}<p class="grupo"><span class="rot">Computador</span></p>
{{range .Computer}}{{template "item" .}}{{end}}{{end}}
</nav></aside>
<label for="menu" class="veu" aria-hidden="true"></label>
<div class="area">
<header class="barra-topo">
<label for="menu" class="hamb" title="Recolher ou abrir o menu">` + ui.IcMenu + `<span class="sr">menu</span></label>
<span class="trilha"><span class="secao">{{.Title}}</span>{{if .TabLabel}}<span class="sep" aria-hidden="true">›</span><span class="titulo">{{.TabLabel}}</span>{{end}}</span>
<span class="selos"><span class="consulta">Consultado às {{.Checked}}</span><a class="btn-atualizar" href="{{.Base}}?aba={{.Tab}}" title="consultar de novo">` + ui.IcAtualizar + `<span>Atualizar</span></a>{{if .Version}}<span class="selo" title="versão do patchd neste computador">Agente <b>{{.Version}}</b></span>{{end}}</span>
</header>
<main>
{{with .Flash}}<p class="aviso {{.Kind}}" role="status">{{.Text}}</p>{{end}}
{{if .Error}}<p class="aviso erro" role="alert">{{.Error}}</p>{{else}}
{{if .Busy}}<p class="aviso andamento" role="status"><span class="gira" aria-hidden="true"></span>Há uma instalação em andamento neste computador (pedido nº {{.Busy}}). Esta página se atualiza sozinha.</p>{{end}}
{{if eq .Tab "atualizacoes"}}{{template "atualizacoes" .}}{{else if eq .Tab "loja"}}{{template "loja" .}}{{else if eq .Tab "reinicio"}}{{template "reinicio" .}}{{else}}{{template "pedidos" .}}{{end}}
{{end}}
</main></div></body></html>
{{define "icone"}}{{if eq . "atualizacoes"}}` + ui.IcAtualizar + `{{else if eq . "loja"}}` + ui.IcLoja + `{{else if eq . "pedidos"}}` + ui.IcPedidos + `{{else if eq . "reinicio"}}` + ui.IcReinicio + `{{end}}{{end}}
{{define "item"}}<a href="{{.Href}}" title="{{.Label}}"{{if .Active}} class="ativo" aria-current="page"{{end}}>{{template "icone" .ID}}<span class="rot">{{.Label}}</span>{{if .Count}}<span class="cont" title="atualizações disponíveis">{{.Count}}</span>{{end}}{{if .Alert}}<span class="cont alerta" title="reinício pendente">!</span>{{end}}</a>{{end}}
{{define "atualizacoes"}}<section><div class="cab"><h1>Atualizações para você</h1>{{if .UpdateAll}}<form method="post" action="{{$.Base}}atualizar-tudo"><button type="submit">Atualizar tudo ({{.UpdateAll}})</button></form>{{end}}</div>
{{with .Queue}}{{if .Waiting}}<div class="fila" role="status"><span class="gira" aria-hidden="true"></span><span>Atualizando tudo, uma de cada vez: {{len .Waiting}} na fila. Deixe a Central aberta até terminar.</span><form method="post" action="{{$.Base}}cancelar-fila"><button class="sec" type="submit">Cancelar o restante</button></form></div>{{end}}
{{if .Failed}}<ul class="falhas">{{range .Failed}}<li>{{.}}</li>{{end}}</ul>{{end}}{{end}}
{{if .Updates}}<ul class="lista">{{range .Updates}}<li><div><strong>{{.Name}}</strong><span class="ver">{{.Installed}} → <b>{{.Target}}</b>{{if .Approval}} · com aprovação da TI{{end}}</span></div>
{{if .Pending}}<span class="selo atencao">Com a TI (pedido nº {{.Pending}})</span>{{else if .Running}}<span class="selo andamento">Instalando</span>{{else if .Queued}}<span class="selo neutro">Na fila</span>{{else}}<form method="post" action="{{$.Base}}pedir"><input type="hidden" name="app" value="{{.App}}"><input type="hidden" name="action" value="atualizar">
<button type="submit"{{if $.Busy}} disabled{{end}}>{{if .Approval}}Pedir à TI{{else}}Atualizar{{end}}</button></form>{{end}}</li>{{end}}</ul>
<p class="nota">Feche o programa antes de atualizar. A instalação roda em segundo plano e leva poucos minutos.</p>
{{else}}<p class="vazio">Nenhuma atualização disponível para você neste computador.</p>{{end}}
</section>
{{if .Current}}<section><h2>Em dia</h2><ul class="lista">{{range .Current}}<li><div><strong>{{.Name}}</strong><span class="ver">{{.Installed}}</span></div><span class="selo sucesso">✓ em dia</span></li>{{end}}</ul></section>{{end}}
{{end}}
{{define "loja"}}{{with .Confirm}}<section class="confirma"><h1>Instalar {{.Name}}?</h1>
{{if .Approval}}<p>O pedido da versão <b>{{.Target}}</b> vai para a aprovação da equipe de TI. Aprovado, a instalação começa sozinha, em segundo plano; você acompanha em Meus pedidos.</p>
{{else}}<p>A versão <b>{{.Target}}</b> será instalada neste computador, em segundo plano, aprovada pela equipe de TI. Leva poucos minutos; você pode continuar trabalhando.</p>{{end}}
<div class="botoes"><form method="post" action="{{$.Base}}pedir"><input type="hidden" name="app" value="{{.App}}"><input type="hidden" name="action" value="instalar">
<button type="submit"{{if and $.Busy (not .Approval)}} disabled{{end}}>{{if .Approval}}Pedir à TI{{else}}Instalar agora{{end}}</button></form><a class="sec" href="{{$.Base}}?aba=loja">Cancelar</a></div></section>{{end}}
<section><h1>Programas para instalar</h1>
{{if .Install}}<ul class="lista">{{range .Install}}<li><div><strong>{{.Name}}</strong><span class="ver">versão {{.Target}}{{if .Approval}} · com aprovação da TI{{end}}</span></div>
{{if .Pending}}<span class="selo atencao">Com a TI (pedido nº {{.Pending}})</span>{{else}}<a class="btn" href="{{$.Base}}?aba=loja&amp;confirmar={{.App}}">{{if .Approval}}Pedir à TI{{else}}Instalar{{end}}</a>{{end}}</li>{{end}}</ul>
{{else}}<p class="vazio">Nenhum programa novo disponível para você neste computador.</p>{{end}}
</section>
{{if .Installed}}<section><h2>Já instalados</h2><ul class="lista">{{range .Installed}}<li><div><strong>{{.Name}}</strong><span class="ver">{{.Installed}}</span></div><span class="selo sucesso">instalado</span></li>{{end}}</ul></section>{{end}}
{{if .Unavailable}}<section><h2>Indisponíveis agora</h2><ul class="lista">{{range .Unavailable}}<li><div><strong>{{.Name}}</strong><span class="ver">{{.Reason}}</span></div><span class="selo neutro">indisponível</span></li>{{end}}</ul></section>{{end}}
{{end}}
{{define "reinicio"}}{{with .Reboot}}<section><h1>Reinício do computador</h1>
{{if not .Allowed}}<p class="vazio">Neste computador (um servidor), o reinício é feito pela equipe de TI.</p>
{{else if .JobID}}<p class="aviso agendado">{{if .At}}Reinício agendado para as <b>{{.At}}</b>{{else}}Reinício a caminho{{end}} (pedido nº {{.JobID}}). Salve seu trabalho antes; o Windows avisa na hora.</p>
{{else if .Options}}<p>Atualizações instaladas neste computador só terminam de valer depois de reiniciar. Escolha a melhor hora para você:</p>
<form method="post" action="{{$.Base}}pedir" class="horas"><input type="hidden" name="action" value="reiniciar">
<fieldset><legend>Quando reiniciar</legend>{{range .Options}}<label><input type="radio" name="quando" value="{{.Value}}"{{if .First}} checked{{end}}> {{.Label}}</label>{{end}}</fieldset>
<button type="submit">Agendar o reinício</button></form>
<p class="nota">Na hora marcada, o Windows mostra um aviso e reinicia. Se houver outro reinício marcado para mais cedo (pela equipe de TI, por exemplo), vale o mais cedo.</p>
{{else if .Known}}<p class="vazio">✓ Nenhum reinício pendente neste computador.</p>
{{else}}<p class="vazio">O patchd ainda não sabe se este computador precisa reiniciar.</p>{{end}}
</section>{{end}}{{end}}
{{define "pedidos"}}<section><h1>Meus pedidos neste computador</h1>
{{if .Requests}}<ul class="lista">{{range .Requests}}<li><div><strong>{{.Name}} {{.Version}}</strong><span class="ver">{{if .ID}}pedido nº {{.ID}}{{else}}pedido à TI nº {{.Request}}{{end}} · {{.When}}</span>{{if .Detail}}<span class="det">{{.Detail}}</span>{{end}}</div>
<span class="selo {{.Class}}">{{.Label}}</span></li>{{end}}</ul>
{{else}}<p class="vazio">Você ainda não fez pedidos neste computador.</p>{{end}}
</section>
{{end}}`))

// css: o que é da Central, por cima do layout do painel (ui.LayoutCSS, Aula 19.1b).
const css = `
body{font-size:15px}
.lado .marca{font-size:1rem}
main{max-width:52rem;margin:0 auto;padding:1.5rem 1.5rem 3rem}
@media (max-width:600px){main{padding:1rem .9rem 2rem}.barra-topo{padding:0 .8rem}.trilha .secao,.trilha .sep{display:none}}
.consulta{color:var(--muted);font-size:.8rem;white-space:nowrap;align-self:center}
a.btn-atualizar{display:inline-flex;align-items:center;gap:.35rem;padding:.2rem .65rem;border:1px solid var(--borda);border-radius:999px;color:var(--texto);
text-decoration:none;font-size:.8rem;background:#fff;white-space:nowrap}a.btn-atualizar:hover{background:#f1f3f7}a.btn-atualizar .ic{width:14px;height:14px}
.selos{align-items:center}
@media (max-width:960px){.selos{display:flex}.consulta,.selos .selo{display:none}}
.lado .cont.alerta{background:var(--laranja)}
.cab{display:flex;align-items:center;justify-content:space-between;gap:1rem;margin:0 0 .75rem}.cab h1{margin:0}
.fila{display:flex;align-items:center;gap:.7rem;padding:.65rem .85rem;border:1px solid #c3dbf5;background:#eef5fd;color:#0b3f73;border-radius:var(--raio);margin:0 0 .75rem}
.fila form{margin-left:auto}button.sec{background:#fff;color:var(--azul)}button.sec:hover{background:#eef5fd}
ul.falhas{margin:0 0 .75rem;padding:.6rem .9rem .6rem 2rem;background:#fff6e0;border:1px solid #f6d68b;border-radius:var(--raio);color:#7a4b00;font-size:.88rem}
section{background:#fff;border:1px solid var(--borda);border-radius:var(--raio);box-shadow:var(--sombra);padding:1rem 1.25rem;margin:0 0 1rem}
h1{font-size:1.15rem;font-weight:600;margin:0 0 .75rem}h2{font-size:1rem;font-weight:600;margin:0 0 .5rem;color:var(--muted)}
ul.lista{list-style:none;margin:0;padding:0}
ul.lista li{display:flex;align-items:center;justify-content:space-between;gap:1rem;padding:.7rem 0;border-top:1px solid var(--borda)}
ul.lista li:first-child{border-top:0}
ul.lista li div{display:flex;flex-direction:column}
.ver{color:var(--muted);font-size:.85rem}.ver b{color:var(--texto)}
form{margin:0}
button{font:inherit;font-size:.9rem;padding:.45rem 1.1rem;border-radius:6px;border:1px solid var(--azul);background:var(--azul);color:#fff;cursor:pointer;white-space:nowrap}
button:hover{background:var(--azul-2)}button:disabled{background:#c7d2df;border-color:#c7d2df;cursor:not-allowed}
.selo{font-size:.82rem;font-weight:600;padding:.15rem .6rem;border-radius:999px;white-space:nowrap;text-align:right;border:0}
.barra-topo .selo{font-size:.74rem;font-weight:400;border:1px solid var(--borda);background:#f7f8fb;color:var(--muted)}
.selo.sucesso{color:#14532d;background:#eaf7ee}.selo.andamento{color:#0b3f73;background:#eef5fd}.selo.atencao{color:#7a4b00;background:#fff6e0}
.selo.erro{color:#7f1d1d;background:#fbe9e9}.selo.neutro{color:var(--muted);background:#f1f3f5}
.det{color:var(--muted);font-size:.8rem;margin-top:.15rem}
a.btn{display:inline-block;padding:.45rem 1.1rem;border:1px solid var(--azul);border-radius:6px;color:var(--azul);text-decoration:none;font-size:.9rem;background:#fff;white-space:nowrap}
a.btn:hover{background:#eef5fd}
section.confirma{border-color:#9cc3ec;background:#f7fbff}.confirma p{margin:.25rem 0 1rem}
.botoes{display:flex;align-items:center;gap:1rem}a.sec{color:var(--muted)}
.aviso.agendado{background:#eef5fd;border-color:#c3dbf5;color:#0b3f73}
form.horas fieldset{border:0;margin:0 0 1rem;padding:0;display:flex;flex-direction:column;gap:.35rem}
form.horas legend{font-weight:600;margin-bottom:.35rem}
form.horas label{display:flex;align-items:center;gap:.55rem;padding:.55rem .75rem;border:1px solid var(--borda);border-radius:6px;cursor:pointer;background:#fff}
form.horas label:has(input:checked){border-color:var(--azul);background:#eef5fd}
.vazio,.nota{color:var(--muted);margin:.25rem 0 0}.nota{font-size:.85rem;margin-top:.75rem}
.aviso{padding:.75rem .9rem;border-radius:var(--raio);margin:0 0 1rem;border:1px solid transparent}
.aviso.sucesso{background:#eaf7ee;border-color:#bfe5c9;color:#14532d}.aviso.atencao{background:#fff6e0;border-color:#f6d68b;color:#7a4b00}
.aviso.erro{background:#fbe9e9;border-color:#ecb3b3;color:#7f1d1d}.aviso.andamento{background:#eef5fd;border-color:#c3dbf5;color:#0b3f73;display:flex;align-items:center;gap:.6rem}
.gira{width:14px;height:14px;border:2px solid #9cc3ec;border-top-color:var(--azul);border-radius:50%;animation:g 1s linear infinite;flex:none}
@keyframes g{to{transform:rotate(360deg)}}
`
