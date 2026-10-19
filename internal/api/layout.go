package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/tasantiago/patchd/internal/campaign"
	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/store"
)

// Layout do painel (Aula 19.1): o padrão das aplicações do tribunal. Barra lateral fixa com o
// logotipo, o usuário e os grupos do menu; barra superior com o recolher, a trilha, as versões
// e o menu da conta. Sem JavaScript (a CSP proíbe): o recolher é um checkbox com <label>, o menu
// da conta é um <details>. Os contadores do menu e o logotipo ficam num cache curto, para cada
// página não custar três consultas a mais.

const (
	chromeTTL = 20 * time.Second // contadores do menu
	logoTTL   = time.Minute      // logotipo (outro servidor no mesmo banco vê a troca em até 1 min)
	logoName  = "logo"
	// logoMaxSide: o maior lado aceito, em pixels. O arquivo tem até 256 KB, mas um PNG pequeno
	// pode declarar 30000x30000 e pedir gigabytes ao ser aberto: a conferência lê só o cabeçalho
	// antes de decodificar.
	logoMaxSide = 1024
)

// navCounts: os números ao lado dos itens do menu. Zero não aparece.
type navCounts struct {
	Requests int // pedidos do autoatendimento esperando a TI (Aula 18.3)
	Paused   int // campanhas pausadas
	Sources  int // máquinas com a fonte de busca falhando (Aula 16.2), sem o catálogo offline
}

// chromeCache guarda os contadores, a versão publicada do agente e o logotipo.
type chromeCache struct {
	mu      sync.Mutex
	at      time.Time
	counts  navCounts
	agent   string
	logoMu  sync.Mutex
	logoAt  time.Time
	logo    *store.PanelAsset // nil: sem logotipo
	logoErr bool              // a última leitura falhou (o log já disse)
}

// chromeFor devolve os contadores e a versão do agente, refazendo a conta depois de chromeTTL.
// Falha numa consulta mantém o número anterior: o menu não é lugar de erro.
func (a *api) chromeFor(ctx context.Context) (navCounts, string) {
	c := &a.chrome
	c.mu.Lock()
	defer c.mu.Unlock()
	if now := a.now(); c.at.IsZero() || now.Sub(c.at) >= chromeTTL || now.Before(c.at) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		if err := a.countNav(ctx, &c.counts); err != nil {
			a.logger.Debug("menu: contadores não atualizados", "error", err)
		}
		c.agent, _ = a.publishedVersion()
		c.at = now
	}
	return c.counts, c.agent
}

// chromeStale faz a próxima página refazer os contadores (depois de aprovar um pedido, por
// exemplo, o número tem que baixar na volta).
func (a *api) chromeStale() {
	a.chrome.mu.Lock()
	a.chrome.at = time.Time{}
	a.chrome.mu.Unlock()
}

// staleAfter: a ação muda um contador do menu (pedido decidido, campanha retomada).
func (a *api) staleAfter(next func(http.ResponseWriter, *http.Request, panel.Session)) func(http.ResponseWriter, *http.Request, panel.Session) {
	return func(w http.ResponseWriter, r *http.Request, s panel.Session) {
		next(w, r, s)
		a.chromeStale()
	}
}

func (a *api) countNav(ctx context.Context, n *navCounts) error {
	var errs []error
	if l, err := a.store.SelfServiceRequests(ctx, store.RequestFilter{Waiting: true, Limit: 500}); err == nil {
		n.Requests = len(l)
	} else {
		errs = append(errs, err)
	}
	if l, err := a.store.Campaigns(ctx, 200); err == nil {
		n.Paused = 0
		for _, c := range l {
			if c.State == campaign.StatePaused {
				n.Paused++
			}
		}
	} else {
		errs = append(errs, err)
	}
	if l, err := a.store.SourceFailures(ctx, ""); err == nil {
		seen := map[string]bool{}
		for _, f := range l {
			if f.Source != "wua-offline" {
				seen[f.MachineID] = true
			}
		}
		n.Sources = len(seen)
	} else {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// panelLogo devolve o logotipo (nil: nenhum), do cache.
func (a *api) panelLogo(ctx context.Context) *store.PanelAsset {
	c := &a.chrome
	c.logoMu.Lock()
	defer c.logoMu.Unlock()
	if now := a.now(); c.logoAt.IsZero() || now.Sub(c.logoAt) >= logoTTL || now.Before(c.logoAt) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		l, ok, err := a.store.PanelAsset(ctx, logoName)
		switch {
		case err != nil:
			if !c.logoErr {
				a.logger.Warn("painel: não deu para ler o logotipo; fica o anterior", "error", err)
			}
			c.logoErr = true
		case ok:
			c.logo, c.logoErr = &l, false
		default:
			c.logo, c.logoErr = nil, false
		}
		c.logoAt = now
	}
	return c.logo
}

// logoURL: o endereço do logotipo com a versão (o navegador guarda e só pede de novo quando
// o arquivo muda); "" sem logotipo.
func (a *api) logoURL(ctx context.Context) string {
	if l := a.panelLogo(ctx); l != nil {
		return "/painel/logo?v=" + l.SHA256[:16]
	}
	return ""
}

// logoChanged esquece o logotipo do cache: a próxima página lê de novo do banco.
func (a *api) logoChanged() {
	a.chrome.logoMu.Lock()
	a.chrome.logoAt = time.Time{}
	a.chrome.logoMu.Unlock()
}

// logoFile: GET /painel/logo. Sem sessão: a tela de login também mostra. Só os bytes que o
// admin enviou e que passaram pela conferência, com o tipo conferido.
func (a *api) logoFile(w http.ResponseWriter, r *http.Request) {
	l := a.panelLogo(r.Context())
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	if l == nil {
		h.Set("Cache-Control", "no-store")
		http.NotFound(w, r)
		return
	}
	etag := `"` + l.SHA256[:32] + `"`
	h.Set("ETag", etag)
	if v := r.URL.Query().Get("v"); v != "" && strings.HasPrefix(l.SHA256, v) && len(v) >= 16 {
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "no-cache")
	}
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Type", l.ContentType)
	h.Set("Content-Length", fmt.Sprint(len(l.Data)))
	_, _ = w.Write(l.Data)
}

// checkLogo confere o arquivo pelos bytes, nunca pelo nome nem pelo tipo que o navegador
// mandou: PNG ou JPEG de verdade, inteiro, até logoMaxSide de lado.
func checkLogo(b []byte) (contentType string, err error) {
	switch {
	case len(b) == 0:
		return "", errors.New("o arquivo está vazio")
	case len(b) > store.MaxPanelAsset:
		return "", fmt.Errorf("o arquivo tem %d KB; o limite é %d KB", (len(b)+1023)/1024, store.MaxPanelAsset/1024)
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		contentType = "image/png"
	case bytes.HasPrefix(b, []byte{0xff, 0xd8, 0xff}):
		contentType = "image/jpeg"
	default:
		return "", errors.New("só PNG ou JPEG (o conteúdo não começa como nenhum dos dois)")
	}
	decodeConfig, decode := png.DecodeConfig, png.Decode
	if contentType == "image/jpeg" {
		decodeConfig, decode = jpeg.DecodeConfig, jpeg.Decode
	}
	cfg, err := decodeConfig(bytes.NewReader(b))
	if err != nil {
		return "", errors.New("a imagem está corrompida ou truncada")
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > logoMaxSide || cfg.Height > logoMaxSide {
		return "", fmt.Errorf("a imagem tem %dx%d pixels; o máximo é %dx%d", cfg.Width, cfg.Height, logoMaxSide, logoMaxSide)
	}
	if _, err := decode(bytes.NewReader(b)); err != nil {
		return "", errors.New("a imagem está corrompida ou truncada")
	}
	return contentType, nil
}

// logoMaxBody: o arquivo e a moldura do multipart.
const logoMaxBody = store.MaxPanelAsset + 16<<10

// logoAction: POST /painel/configuracao/aparencia/logo, só admin. op=enviar (com o arquivo)
// ou op=remover.
func (a *api) logoAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	back := func(q string) { http.Redirect(w, r, "/painel/configuracao?"+q, http.StatusSeeOther) }
	r.Body = http.MaxBytesReader(w, r.Body, logoMaxBody)
	if err := r.ParseMultipartForm(logoMaxBody); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			back("erro=logo&motivo=" + url.QueryEscape(fmt.Sprintf("o arquivo passa de %d KB", store.MaxPanelAsset/1024)))
			return
		}
		back("erro=logo&motivo=" + url.QueryEscape("formulário inválido"))
		return
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()
	if r.FormValue("op") == "remover" {
		ok, err := a.store.DeletePanelAsset(r.Context(), logoName)
		if err != nil {
			a.internalError(w, r, "remover o logotipo", err)
			return
		}
		a.logoChanged()
		if ok {
			a.audit(r, s, "aparencia.logo_remover", "logotipo", "")
			a.logger.Info("logotipo do painel removido", "username", s.Username, "request_id", RequestID(r.Context()))
		}
		back("logo=removido")
		return
	}
	f, _, err := r.FormFile("arquivo")
	if err != nil {
		back("erro=logo&motivo=" + url.QueryEscape("escolha um arquivo"))
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, store.MaxPanelAsset+1))
	if err != nil {
		back("erro=logo&motivo=" + url.QueryEscape("não deu para ler o arquivo"))
		return
	}
	ct, err := checkLogo(data)
	if err != nil {
		back("erro=logo&motivo=" + url.QueryEscape(err.Error()))
		return
	}
	if err := a.store.SetPanelAsset(r.Context(), store.PanelAsset{Name: logoName, ContentType: ct, Data: data, UpdatedBy: s.Username}); err != nil {
		a.internalError(w, r, "gravar o logotipo", err)
		return
	}
	a.logoChanged()
	cfg, _, _ := image.DecodeConfig(bytes.NewReader(data))
	detail := fmt.Sprintf("%s · %d KB · %dx%d", strings.TrimPrefix(ct, "image/"), (len(data)+1023)/1024, cfg.Width, cfg.Height)
	a.audit(r, s, "aparencia.logo", "logotipo", detail)
	a.logger.Info("logotipo do painel trocado", "username", s.Username, "detail", detail, "request_id", RequestID(r.Context()))
	back("logo=gravado")
}

// logoView: o cartão Aparência da aba Sistema.
type logoView struct {
	URL       string // vazio: sem logotipo
	Type      string
	KB        int
	UpdatedAt string
	UpdatedBy string
	MaxKB     int
	MaxSide   int
}

func (a *api) logoCard(ctx context.Context) (*logoView, error) {
	v := &logoView{MaxKB: store.MaxPanelAsset / 1024, MaxSide: logoMaxSide}
	l, ok, err := a.store.PanelAsset(ctx, logoName)
	if err != nil || !ok {
		return v, err
	}
	v.URL = "/painel/logo?v=" + l.SHA256[:16]
	v.Type = strings.ToUpper(strings.TrimPrefix(l.ContentType, "image/"))
	v.KB = (len(l.Data) + 1023) / 1024
	v.UpdatedAt = l.UpdatedAt.UTC().Format("02/01/2006 15:04") + " UTC"
	v.UpdatedBy = l.UpdatedBy
	return v, nil
}

// logoFlash: o resultado do envio, na volta para a aba Sistema.
func logoFlash(q url.Values, v *pageBase) {
	switch {
	case q.Get("logo") == "gravado":
		v.Flash = "Logotipo gravado. Ele aparece no menu lateral e na tela de login (em até 1 minuto em outro servidor no mesmo banco)."
	case q.Get("logo") == "removido":
		v.Flash = "Logotipo removido: o painel volta ao ícone padrão."
	case q.Get("erro") == "logo":
		v.Error = "O logotipo não foi gravado: " + shortError(q.Get("motivo"), 160) + "."
	}
}

// Section: o grupo do menu da página, para a trilha da barra superior.
func (b pageBase) Section() string {
	switch b.Nav {
	case "frota", "inventario", "aposentadas":
		return "Navegação"
	case "patch":
		return "Patch"
	case "campanhas", "jobs", "aneis", "autoatendimento":
		return "Automação"
	case "configuracao":
		return "Administração"
	}
	return ""
}

// Initials: as iniciais do nome, no lugar da foto (o painel não guarda foto).
func (b pageBase) Initials() string {
	var out []rune
	words := strings.FieldsFunc(b.Session.Greeting(), func(r rune) bool { return unicode.IsSpace(r) || r == '.' || r == '_' || r == '-' })
	for i, w := range words {
		if i != 0 && i != len(words)-1 {
			continue
		}
		if r := []rune(w); len(r) > 0 && unicode.IsLetter(r[0]) {
			out = append(out, unicode.ToUpper(r[0]))
		}
	}
	if len(out) == 0 {
		return "?"
	}
	return string(out)
}

// RoleLabel: o perfil como a tela fala.
func (b pageBase) RoleLabel() string {
	if b.Session.Role == panel.RoleAdmin {
		return "Administrador"
	}
	return "Leitura"
}

// Ícones de linha do menu (Aula 19.1), desenhados aqui: currentColor, sem arquivo de fora.
func icon(paths string) string {
	return `<svg class="ic" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">` + paths + `</svg>`
}

var (
	icFrota       = icon(`<rect x="3" y="4" width="18" height="12" rx="2"/><path d="M8 20h8M12 16v4"/>`)
	icInventario  = icon(`<rect x="5" y="3" width="14" height="18" rx="2"/><path d="M9 8h6M9 12h6M9 16h3"/>`)
	icAposentadas = icon(`<rect x="3" y="4" width="18" height="4" rx="1"/><path d="M5 8v11a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V8M10 12h4"/>`)
	icGeral       = icon(`<path d="M3 21h18M7 17v-6M12 17V5M17 17v-9"/>`)
	icSistema     = icon(`<rect x="3" y="3" width="18" height="18" rx="2"/><path d="M3 12h18M12 3v18"/>`)
	icTerceiros   = icon(`<path d="M21 8l-9-5-9 5v8l9 5 9-5z"/><path d="M3 8l9 5 9-5M12 13v8"/>`)
	icCampanhas   = icon(`<path d="M5 21V4"/><path d="M5 4h11l-2 4 2 4H5"/>`)
	icJobs        = icon(`<path d="M10 6h11M10 12h11M10 18h11"/><path d="M3 6l1.5 1.5L7 5M3 12l1.5 1.5L7 11M3 18l1.5 1.5L7 17"/>`)
	icAneis       = icon(`<circle cx="12" cy="12" r="9"/><circle cx="12" cy="12" r="5"/><circle cx="12" cy="12" r="1"/>`)
	icLoja        = icon(`<path d="M6 7h12l1 14H5z"/><path d="M9 10V6a3 3 0 0 1 6 0v4"/>`)
	icConfig      = icon(`<path d="M4 6h10M18 6h2M4 12h4M12 12h8M4 18h12"/><circle cx="16" cy="6" r="2"/><circle cx="10" cy="12" r="2"/><circle cx="18" cy="18" r="2"/>`)
	icAuditoria   = icon(`<path d="M14 3H6a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h5"/><path d="M14 3l5 5v3"/><circle cx="16.5" cy="16.5" r="3"/><path d="M21 21l-2.3-2.3"/>`)
	icMenu        = icon(`<path d="M4 6h16M4 12h16M4 18h16"/>`)
	icSair        = icon(`<path d="M9 21H6a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h3"/><path d="M16 17l5-5-5-5M21 12H9"/>`)
	icAbaixo      = icon(`<path d="M6 9l6 6 6-6"/>`)
)
