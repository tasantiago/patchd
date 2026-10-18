// Package alerts decide o que vira e-mail para a equipe (Aula 18.6): pedidos do
// autoatendimento esperando a TI, campanhas que pararam sozinhas e máquinas cuja busca na
// fonte principal falha. Cada aviso tem uma chave; a chave mandada fica no banco e o mesmo
// aviso não sai de novo (nem depois de reiniciar o servidor).
package alerts

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/campaign"
	"github.com/tasantiago/patchd/internal/store"
)

// Tipos de aviso (PATCHD_NOTIFY_EVENTS).
const (
	KindRequests  = "pedidos"
	KindCampaigns = "campanhas"
	KindSources   = "fontes"
)

// Kinds: todos, na ordem do e-mail.
var Kinds = []string{KindRequests, KindCampaigns, KindSources}

// Store é o que o laço lê e grava.
type Store interface {
	SelfServiceRequests(ctx context.Context, f store.RequestFilter) ([]store.SelfServiceRequest, error)
	Campaigns(ctx context.Context, limit int) ([]campaign.Campaign, error)
	SourceFailures(ctx context.Context, machineID string) ([]store.SourceFailure, error)
	NotificationsSent(ctx context.Context, keys []string) (map[string]bool, error)
	MarkNotificationsSent(ctx context.Context, keys []string, at time.Time) error
	PruneNotifications(ctx context.Context, before time.Time) (int64, error)
}

// Sender manda o e-mail (internal/notify).
type Sender interface {
	Send(ctx context.Context, subject, body string) error
}

const (
	// campaignRecent: campanha pausada há mais tempo que isso não é novidade (a primeira
	// passada, logo depois de ligar as notificações, não manda as pausas de meses atrás).
	campaignRecent = 7 * 24 * time.Hour
	// keepKeys: as chaves mandadas ficam 90 dias.
	keepKeys = 90 * 24 * time.Hour
	// maxListed: as linhas de cada e-mail; o resto vira "e mais N".
	maxListed = 20
)

// Watcher olha o estado e manda o que for novo.
type Watcher struct {
	Store     Store
	Mail      Sender
	Kinds     []string // vazio: todos
	PublicURL string   // para os links; vazio: sem links
	Logger    *slog.Logger
	Now       func() time.Time
}

// message é um e-mail a mandar e as chaves que ele cobre.
type message struct {
	kind, subject, body string
	keys                []string
}

// ParseKinds lê PATCHD_NOTIFY_EVENTS ("pedidos,campanhas,fontes"; vazio: todos).
func ParseKinds(v string) ([]string, error) {
	if strings.TrimSpace(v) == "" {
		return slices.Clone(Kinds), nil
	}
	var out []string
	for _, k := range strings.Split(v, ",") {
		k = strings.ToLower(strings.TrimSpace(k))
		if k == "" {
			continue
		}
		if !slices.Contains(Kinds, k) {
			return nil, fmt.Errorf("PATCHD_NOTIFY_EVENTS: %q desconhecido (use %s)", k, strings.Join(Kinds, ", "))
		}
		if !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	return out, nil
}

func (w *Watcher) on(kind string) bool { return len(w.Kinds) == 0 || slices.Contains(w.Kinds, kind) }

func (w *Watcher) link(path string) string {
	if w.PublicURL == "" {
		return ""
	}
	return "\n\n" + strings.TrimRight(w.PublicURL, "/") + path
}

// Step faz uma passada: monta os avisos, tira os já mandados, manda e guarda as chaves de
// quem foi aceito pelo SMTP. Um e-mail que falha não impede os outros; volta na próxima.
func (w *Watcher) Step(ctx context.Context) (sent int, err error) {
	now := w.Now().UTC()
	msgs, errs := w.collect(ctx, now)
	for _, m := range msgs {
		if err := w.Mail.Send(ctx, m.subject, m.body); err != nil {
			errs = append(errs, fmt.Errorf("e-mail de %s: %w", m.kind, err))
			continue
		}
		if err := w.Store.MarkNotificationsSent(ctx, m.keys, now); err != nil {
			// Mandado e não guardado: sai de novo na próxima passada. Melhor repetido que perdido.
			errs = append(errs, fmt.Errorf("guardar o aviso de %s: %w", m.kind, err))
			continue
		}
		sent++
		if w.Logger != nil {
			w.Logger.Info("notificação enviada", "tipo", m.kind, "avisos", len(m.keys))
		}
	}
	if _, err := w.Store.PruneNotifications(ctx, now.Add(-keepKeys)); err != nil {
		errs = append(errs, err)
	}
	return sent, errors.Join(errs...)
}

// Mail é um e-mail que a passada mandaria.
type Mail struct{ Kind, Subject, Body string }

// Preview mostra o que a próxima passada mandaria, sem mandar nem marcar
// (patchd-server notify pending).
func (w *Watcher) Preview(ctx context.Context) ([]Mail, error) {
	msgs, errs := w.collect(ctx, w.Now().UTC())
	out := make([]Mail, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, Mail{m.kind, m.subject, m.body})
	}
	return out, errors.Join(errs...)
}

// collect monta os avisos novos de cada tipo ligado.
func (w *Watcher) collect(ctx context.Context, now time.Time) ([]message, []error) {
	var msgs []message
	var errs []error
	if w.on(KindRequests) {
		m, err := w.requests(ctx)
		errs = append(errs, err)
		msgs = append(msgs, m...)
	}
	if w.on(KindCampaigns) {
		m, err := w.campaigns(ctx, now)
		errs = append(errs, err)
		msgs = append(msgs, m...)
	}
	if w.on(KindSources) {
		m, err := w.sources(ctx)
		errs = append(errs, err)
		msgs = append(msgs, m...)
	}
	return msgs, errs
}

// fresh tira as chaves já mandadas; devolve os índices dos que ficam.
func (w *Watcher) fresh(ctx context.Context, keys []string) ([]int, error) {
	done, err := w.Store.NotificationsSent(ctx, keys)
	if err != nil {
		return nil, err
	}
	var idx []int
	for i, k := range keys {
		if !done[k] {
			idx = append(idx, i)
		}
	}
	return idx, nil
}

func stamp(t time.Time) string { return t.UTC().Format("02/01 15:04") + " UTC" }

func more(n int) string {
	if n > maxListed {
		return fmt.Sprintf("\n… e mais %d.", n-maxListed)
	}
	return ""
}

// requests: um e-mail com os pedidos novos esperando a TI.
func (w *Watcher) requests(ctx context.Context) ([]message, error) {
	list, err := w.Store.SelfServiceRequests(ctx, store.RequestFilter{Waiting: true, Limit: 500})
	if err != nil {
		return nil, fmt.Errorf("pedidos: %w", err)
	}
	keys := make([]string, len(list))
	for i, q := range list {
		keys[i] = fmt.Sprintf("pedido:%d", q.ID)
	}
	idx, err := w.fresh(ctx, keys)
	if err != nil || len(idx) == 0 {
		return nil, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d pedido(s) novo(s) do autoatendimento aguardando a TI (%d no total na fila):\n", len(idx), len(list))
	var mk []string
	for n, i := range idx {
		q := list[i]
		mk = append(mk, keys[i])
		if n < maxListed {
			fmt.Fprintf(&b, "\n- nº %d · %s %s (%s) · %s · máquina %s · %s", q.ID, q.Action, q.AppID, q.Version, q.Account, short(q.MachineID), stamp(q.CreatedAt))
		}
	}
	b.WriteString(more(len(idx)))
	b.WriteString("\n\nSem decisão em 7 dias, o pedido expira. Aprovar e recusar: Automação › Autoatendimento.")
	b.WriteString(w.link("/painel/autoatendimento"))
	return []message{{kind: KindRequests, subject: fmt.Sprintf("%d pedido(s) aguardando a TI", len(idx)), body: b.String(), keys: mk}}, nil
}

// campaigns: um e-mail por campanha que o servidor pausou (a pausa do admin não avisa).
func (w *Watcher) campaigns(ctx context.Context, now time.Time) ([]message, error) {
	list, err := w.Store.Campaigns(ctx, 200)
	if err != nil {
		return nil, fmt.Errorf("campanhas: %w", err)
	}
	var cs []campaign.Campaign
	var keys []string
	for _, c := range list {
		if c.State != campaign.StatePaused || strings.HasPrefix(c.Detail, "pausada por ") || now.Sub(c.UpdatedAt) > campaignRecent {
			continue
		}
		cs = append(cs, c)
		keys = append(keys, fmt.Sprintf("campanha:%d:%d", c.ID, c.UpdatedAt.Unix()))
	}
	idx, err := w.fresh(ctx, keys)
	if err != nil {
		return nil, err
	}
	var out []message
	for _, i := range idx {
		c := cs[i]
		body := fmt.Sprintf("A campanha nº %d (%s, %s) parou sozinha em %s:\n\n%s\n\nOs anéis seguintes só são liberados depois de resolver e retomar (painel ou patchd-server campaign resume).",
			c.ID, c.Name, c.Target(), stamp(c.UpdatedAt), c.Detail) + w.link(fmt.Sprintf("/painel/campanhas/%d", c.ID))
		out = append(out, message{kind: KindCampaigns, subject: "Campanha pausada: " + c.Name, body: body, keys: []string{keys[i]}})
	}
	return out, nil
}

// sources: um e-mail com as máquinas que passaram a falhar na fonte principal (o catálogo
// offline só confere, não avisa). A chave leva a última resposta boa: enquanto a falha
// continua, a chave é a mesma; voltou e falhou de novo, é aviso novo.
func (w *Watcher) sources(ctx context.Context) ([]message, error) {
	all, err := w.Store.SourceFailures(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("fontes: %w", err)
	}
	var list []store.SourceFailure
	var keys []string
	for _, f := range all {
		if f.Source == "wua-offline" {
			continue
		}
		var ok int64
		if f.LastOK != nil {
			ok = f.LastOK.Unix()
		}
		list = append(list, f)
		keys = append(keys, fmt.Sprintf("fonte:%s:%s:%d", f.MachineID, f.Source, ok))
	}
	idx, err := w.fresh(ctx, keys)
	if err != nil || len(idx) == 0 {
		return nil, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d máquina(s) passaram a falhar na busca de atualizações (%d com falha agora):\n", len(idx), len(list))
	var mk []string
	for n, i := range idx {
		f := list[i]
		mk = append(mk, keys[i])
		if n >= maxListed {
			continue
		}
		since := "nenhuma resposta guardada"
		if f.LastOK != nil {
			since = "última resposta em " + stamp(*f.LastOK)
		}
		host := f.Hostname
		if host == "" {
			host = short(f.MachineID)
		}
		fmt.Fprintf(&b, "\n- %s · %s · %s · %s", host, f.Source, since, oneLine(f.Error, 160))
	}
	b.WriteString(more(len(idx)))
	b.WriteString("\n\nSem a fonte, a máquina não recebe atualização por ela. Roteiro: docs/operacao.md, \"Fonte de busca falhando\".")
	b.WriteString(w.link("/painel/patch?aba=geral"))
	return []message{{kind: KindSources, subject: fmt.Sprintf("%d máquina(s) com a busca falhando", len(idx)), body: b.String(), keys: mk}}, nil
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return "sem detalhe"
	}
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
