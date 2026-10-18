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
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/tasantiago/patchd/internal/localapi"
	"github.com/tasantiago/patchd/internal/protocol"
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

	mu    sync.Mutex
	flash *flash
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

// Serve atende até o contexto acabar.
func (a *App) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{Handler: a.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 2 * time.Minute, MaxHeaderBytes: 16 << 10}
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
		h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src data:; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer") // o segredo está no caminho
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
		case sub == "pedir" && r.Method == http.MethodPost:
			if !a.sameOrigin(r) {
				http.Error(w, "origem recusada", http.StatusForbidden)
				return
			}
			a.request(w, r)
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

// Tabs: as abas da página. A 17.4 tem só Atualizações; a Loja (17.5) e o Reinício (17.6)
// entram aqui.
type tab struct {
	ID, Label string
	Active    bool
}

// Item é um programa como a página mostra.
type Item struct {
	App, Name, Installed, Target, Reason string
}

type pageData struct {
	Title, Base, Account, Error, Checked string
	Tabs                                 []tab
	Flash                                *flash
	Updates, Current                     []Item
	Busy                                 int64
}

func (a *App) page(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	d := pageData{Title: Title, Base: a.base(), Checked: a.Now().Format("02/01 15:04"),
		Tabs: []tab{{ID: "atualizacoes", Label: "Atualizações", Active: true}}}
	a.mu.Lock()
	d.Flash, a.flash = a.flash, nil
	a.mu.Unlock()

	resp, err := a.Call(ctx, localapi.Request{Op: localapi.OpCatalog})
	switch {
	case err != nil:
		d.Error = "Não foi possível falar com o patchd neste computador: " + err.Error()
	case !resp.OK:
		d.Error = userError(resp)
	default:
		d.Account = resp.Account
		d.Busy = resp.Catalog.OpenJob
		for _, it := range resp.Catalog.Items {
			x := Item{App: it.App, Name: it.Name, Installed: it.Installed, Target: it.Target, Reason: it.Reason}
			switch it.Action {
			case protocol.SelfServiceUpdate:
				d.Updates = append(d.Updates, x)
			case "em_dia":
				d.Current = append(d.Current, x)
			}
		}
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	if d.Busy > 0 {
		h.Set("Refresh", "15") // a página acompanha a instalação sozinha (sem JavaScript)
	}
	_ = pageTmpl.Execute(w, d)
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
	resp, err := a.Call(ctx, req)
	var f flash
	switch {
	case err != nil:
		f = flash{"erro", "Não foi possível falar com o patchd neste computador: " + err.Error()}
	case !resp.OK:
		f = flash{"erro", userError(resp)}
	case resp.Result.Status == protocol.SelfServiceCreated:
		f = flash{"sucesso", resp.Result.Message}
	default:
		f = flash{"atencao", resp.Result.Message}
	}
	a.mu.Lock()
	a.flash = &f
	a.mu.Unlock()
	http.Redirect(w, r, a.base(), http.StatusSeeOther) // recarregar a página não repete o pedido
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
}

// noticeText é o título e o texto do balão.
func noticeText(n Notice) (title, text string) {
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

// Watch confere a loja depois de first e a cada every, e manda um aviso quando aparece
// atualização nova para o usuário (a mesma lista não é repetida).
func (a *App) Watch(ctx context.Context, first, every time.Duration, out chan<- Notice) {
	wait := first
	var last []string
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = every
		cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		resp, err := a.Call(cctx, localapi.Request{Op: localapi.OpCatalog})
		cancel()
		if err != nil || !resp.OK {
			continue
		}
		var ids, names []string
		for _, it := range resp.Catalog.Items {
			if it.Action == protocol.SelfServiceUpdate {
				ids, names = append(ids, it.App), append(names, it.Name)
			}
		}
		if len(ids) > 0 && !slices.Equal(ids, last) {
			select {
			case out <- Notice{Updates: names}:
			default: // o ícone ainda não leu o anterior
			}
		}
		last = ids
	}
}

var pageTmpl = template.Must(template.New("p").Parse(`<!doctype html>
<html lang="pt-BR"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Title}}</title><style>` + css + `</style></head><body>
<header><span class="marca">` + shield + `{{.Title}}</span>{{if .Account}}<span class="quem">{{.Account}}</span>{{end}}</header>
<nav class="abas" aria-label="seções">{{range .Tabs}}<a href="{{$.Base}}?aba={{.ID}}"{{if .Active}} class="ativa" aria-current="page"{{end}}>{{.Label}}</a>{{end}}</nav>
<main>
{{with .Flash}}<p class="aviso {{.Kind}}" role="status">{{.Text}}</p>{{end}}
{{if .Error}}<p class="aviso erro" role="alert">{{.Error}}</p>{{else}}
{{if .Busy}}<p class="aviso andamento" role="status"><span class="gira" aria-hidden="true"></span>Há uma instalação em andamento neste computador (pedido nº {{.Busy}}). Esta página se atualiza sozinha.</p>{{end}}
<section><h1>Atualizações para você</h1>
{{if .Updates}}<ul class="lista">{{range .Updates}}<li><div><strong>{{.Name}}</strong><span class="ver">{{.Installed}} → <b>{{.Target}}</b></span></div>
<form method="post" action="{{$.Base}}pedir"><input type="hidden" name="app" value="{{.App}}"><input type="hidden" name="action" value="atualizar">
<button type="submit"{{if $.Busy}} disabled{{end}}>Atualizar</button></form></li>{{end}}</ul>
<p class="nota">Feche o programa antes de atualizar. A instalação roda em segundo plano e leva poucos minutos.</p>
{{else}}<p class="vazio">Nenhuma atualização disponível para você neste computador.</p>{{end}}
</section>
{{if .Current}}<section><h2>Em dia</h2><ul class="lista em-dia">{{range .Current}}<li><div><strong>{{.Name}}</strong><span class="ver">{{.Installed}}</span></div><span class="ok">✓ em dia</span></li>{{end}}</ul></section>{{end}}
{{end}}
</main><footer>Consultado às {{.Checked}} · <a href="{{.Base}}">atualizar a página</a></footer></body></html>`))

const shield = `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 3l8 3v6c0 4.5-3.4 8.3-8 9-4.6-.7-8-4.5-8-9V6l8-3z"/><path d="M8.5 12l2.5 2.5 4.5-5"/></svg>`

const css = `
:root{--verde:#0b7a45;--fundo:#f9fafb;--borda:#e5e7eb;--texto:#111827;--muted:#6b7280;--azul:#066fd1;--azul-2:#0559a8;--vermelho:#d63939;--laranja:#f59f00;--ok:#2fb344;--raio:8px}
*{box-sizing:border-box}
body{margin:0;font:15px/1.5 "Segoe UI",system-ui,sans-serif;color:var(--texto);background:var(--fundo)}
header{display:flex;align-items:center;justify-content:space-between;gap:1rem;padding:0 1.25rem;min-height:56px;background:var(--verde);color:#fff}
.marca{display:flex;align-items:center;gap:.55rem;font-weight:700;font-size:1.15rem}.marca svg{width:24px;height:24px}
.quem{font-size:.8rem;color:#d5efe0}
nav.abas{display:flex;gap:.25rem;padding:0 1.25rem;background:#fff;border-bottom:1px solid var(--borda)}
nav.abas a{padding:.7rem .9rem;color:var(--muted);text-decoration:none;border-bottom:3px solid transparent;font-weight:600}
nav.abas a.ativa{color:var(--verde);border-bottom-color:var(--verde)}
main{max-width:44rem;margin:1.25rem auto;padding:0 1rem}
section{background:#fff;border:1px solid var(--borda);border-radius:var(--raio);padding:1rem 1.25rem;margin:0 0 1rem}
h1{font-size:1.15rem;margin:0 0 .75rem}h2{font-size:1rem;margin:0 0 .5rem;color:var(--muted)}
ul.lista{list-style:none;margin:0;padding:0}
ul.lista li{display:flex;align-items:center;justify-content:space-between;gap:1rem;padding:.7rem 0;border-top:1px solid var(--borda)}
ul.lista li:first-child{border-top:0}
ul.lista li div{display:flex;flex-direction:column}
.ver{color:var(--muted);font-size:.85rem}.ver b{color:var(--texto)}
form{margin:0}
button{font:inherit;font-size:.9rem;padding:.45rem 1.1rem;border-radius:6px;border:1px solid var(--azul);background:var(--azul);color:#fff;cursor:pointer}
button:hover{background:var(--azul-2)}button:disabled{background:#c7d2df;border-color:#c7d2df;cursor:not-allowed}
.ok{color:var(--ok);font-size:.85rem;font-weight:600}
.vazio,.nota{color:var(--muted);margin:.25rem 0 0}.nota{font-size:.85rem;margin-top:.75rem}
.aviso{padding:.75rem .9rem;border-radius:var(--raio);margin:0 0 1rem;border:1px solid transparent}
.aviso.sucesso{background:#eaf7ee;border-color:#bfe5c9;color:#14532d}.aviso.atencao{background:#fff6e0;border-color:#f6d68b;color:#7a4b00}
.aviso.erro{background:#fbe9e9;border-color:#ecb3b3;color:#7f1d1d}.aviso.andamento{background:#eef5fd;border-color:#c3dbf5;color:#0b3f73;display:flex;align-items:center;gap:.6rem}
.gira{width:14px;height:14px;border:2px solid #9cc3ec;border-top-color:var(--azul);border-radius:50%;animation:g 1s linear infinite;flex:none}
@keyframes g{to{transform:rotate(360deg)}}
footer{text-align:center;color:var(--muted);font-size:.8rem;margin:0 0 1.5rem}footer a{color:var(--muted)}
`
