// Package settings guarda os parâmetros que o admin muda pela tela (Aula 19.2).
//
// A regra é a que a equipe decidiu: a tela vence. O .env é o valor inicial e o "restaurar
// o padrão"; enquanto houver uma linha gravada pela tela, ela vale no lugar da variável. Só
// as variáveis listadas (Def) podem ser mudadas, cada uma conferida como o .env é conferido
// na partida; banco, portas, TLS, chaves e segredos ficam sempre no ambiente.
//
// Lookup tem a mesma forma de config.Lookup: o código que já lia o .env lê o valor que vale
// sem mudar nada. Vários servidores no mesmo banco: cada um relê a tabela a cada Loop.
package settings

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tasantiago/patchd/internal/store"
)

// Kind é o tipo de campo da tela.
type Kind int

const (
	Text Kind = iota
	Number
	Select
	Multi // Aula 19.3: várias escolhas (caixas), gravadas separadas por vírgula
)

// Option é uma escolha de um Select.
type Option struct{ Value, Label string }

// Def é um parâmetro que a tela pode mudar.
type Def struct {
	Key     string // a variável do .env (PATCHD_...)
	Group   string // o cartão da tela
	Label   string
	Help    string
	Default string // sem nada no .env
	Kind    Kind
	Options []Option
	Unit    string // "dias", por exemplo
	// Check normaliza e confere um valor (da tela ou do .env), com a mesma regra da partida.
	Check func(v string) (string, error)
	// Effect: quando a mudança passa a valer ("na hora", "na próxima passada"...).
	Effect string
	// Locked: o motivo de não dar para mudar pela tela agora ("" = pode).
	Locked func() string
	// Show: como o valor aparece na tela e na auditoria (nil: como está).
	Show func(v string) string
	// Sessions (Aula 19.4): a origem das sessões (panel.SourceAD, panel.SourceOIDC) que caem
	// quando um item sai desta lista: quem entrou pelo grupo tirado não fica com o perfil até
	// a sessão vencer. Vazio: nenhuma.
	Sessions string
}

// Source diz de onde vem o valor que vale.
type Source string

const (
	FromScreen  Source = "tela"
	FromEnv     Source = ".env"
	FromDefault Source = "padrão"
)

// Store é onde ficam as linhas gravadas pela tela.
type Store interface {
	Settings(ctx context.Context) ([]store.Setting, error)
	SetSettings(ctx context.Context, list []store.Setting) error
	DeleteSetting(ctx context.Context, key string) (bool, error)
}

// Lookup tem a forma de config.Lookup.
type Lookup = func(string) (string, bool)

// Service junta o .env, a tabela e as regras.
type Service struct {
	defs   []Def
	byKey  map[string]*Def
	env    Lookup
	st     Store
	logger *slog.Logger
	// CheckAll confere o conjunto (parâmetros que dependem uns dos outros) antes de gravar;
	// changed são as chaves que mudam (Aula 19.3: o SMTP só é conferido quando é ele que muda).
	CheckAll func(look Lookup, changed []string) error

	mu   sync.RWMutex
	rows map[string]store.Setting // só as válidas
	bad  map[string]bool          // linhas inválidas já avisadas no log
	subs []chan struct{}
}

// New monta o serviço. Refresh carrega a tabela.
func New(defs []Def, env Lookup, st Store, logger *slog.Logger) *Service {
	s := &Service{defs: defs, byKey: map[string]*Def{}, env: env, st: st, logger: logger, rows: map[string]store.Setting{}, bad: map[string]bool{}}
	for i := range defs {
		s.byKey[defs[i].Key] = &s.defs[i]
	}
	return s
}

// Defs devolve as definições, na ordem da tela.
func (s *Service) Defs() []Def { return s.defs }

// Refresh relê a tabela. Uma linha que não passa na conferência (gravada por outra versão,
// ou mexida no banco) é ignorada, com aviso no log, e o .env vale.
func (s *Service) Refresh(ctx context.Context) error {
	list, err := s.st.Settings(ctx)
	if err != nil {
		return err
	}
	rows := map[string]store.Setting{}
	for _, r := range list {
		d, ok := s.byKey[r.Key]
		if !ok {
			continue // de uma versão mais nova, ou que deixou de ser da tela
		}
		v, err := d.Check(r.Value)
		if err != nil {
			s.mu.Lock()
			warn := !s.bad[r.Key]
			s.bad[r.Key] = true
			s.mu.Unlock()
			if warn {
				s.logger.Warn("parâmetro da tela ignorado: valor inválido; vale o .env", "parametro", r.Key, "error", err)
			}
			continue
		}
		r.Value = v
		rows[r.Key] = r
	}
	s.mu.Lock()
	changed := !sameRows(s.rows, rows)
	s.rows = rows
	s.mu.Unlock()
	if changed {
		s.notify()
	}
	return nil
}

func sameRows(a, b map[string]store.Setting) bool {
	if len(a) != len(b) {
		return false
	}
	for k, x := range a {
		if y, ok := b[k]; !ok || y.Value != x.Value {
			return false
		}
	}
	return true
}

// Loop relê a tabela a cada every (outro servidor no mesmo banco pode ter mudado).
func (s *Service) Loop(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	failing := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		err := s.Refresh(ctx)
		switch {
		case err != nil && ctx.Err() == nil && !failing:
			s.logger.Warn("parâmetros da tela: a releitura falhou; ficam os últimos lidos", "error", err)
			failing = true
		case err == nil && failing:
			s.logger.Info("parâmetros da tela: a releitura voltou a funcionar")
			failing = false
		}
	}
}

// Changed devolve um canal que recebe um sinal a cada mudança (pela tela deste servidor ou
// percebida na releitura). O sinal não se acumula: um basta para reler tudo.
func (s *Service) Changed() <-chan struct{} {
	c := make(chan struct{}, 1)
	s.mu.Lock()
	s.subs = append(s.subs, c)
	s.mu.Unlock()
	return c
}

func (s *Service) notify() {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, c := range s.subs {
		select {
		case c <- struct{}{}:
		default:
		}
	}
}

// Lookup: o valor gravado pela tela; sem ele, o do ambiente.
func (s *Service) Lookup(key string) (string, bool) {
	s.mu.RLock()
	r, ok := s.rows[key]
	s.mu.RUnlock()
	if ok {
		return r.Value, true
	}
	return s.env(key)
}

// envValue: o que vale sem a tela (o .env conferido, ou o padrão).
func (s *Service) envValue(d *Def) (string, Source) {
	raw, ok := s.env(d.Key)
	if !ok || strings.TrimSpace(raw) == "" {
		return d.Default, FromDefault
	}
	v, err := d.Check(raw)
	if err != nil {
		return d.Default, FromDefault
	}
	return v, FromEnv
}

// Value é o valor que vale, já normalizado. Para uma chave sem Def, o do ambiente.
func (s *Service) Value(key string) string {
	d, ok := s.byKey[key]
	if !ok {
		v, _ := s.env(key)
		return v
	}
	s.mu.RLock()
	r, ok := s.rows[key]
	s.mu.RUnlock()
	if ok {
		return r.Value
	}
	v, _ := s.envValue(d)
	return v
}

// Change é uma mudança, para a auditoria e a mensagem da tela.
type Change struct {
	Key, Label    string
	Before, After string // como a tela mostra
}

// String: "Fuso horário (PATCHD_TIMEZONE): America/Manaus → America/Porto_Velho".
func (c Change) String() string {
	return fmt.Sprintf("%s (%s): %s → %s", c.Label, c.Key, c.Before, c.After)
}

// FieldErrors são os problemas por parâmetro (nada foi gravado).
type FieldErrors map[string]string

func (f FieldErrors) Error() string {
	keys := make([]string, 0, len(f))
	for k := range f {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b []string
	for _, k := range keys {
		b = append(b, k+": "+f[k])
	}
	return strings.Join(b, "; ")
}

// RejectedError: o conjunto não passou na conferência (CheckAll). Nada foi gravado.
type RejectedError struct{ Err error }

func (e *RejectedError) Error() string { return e.Err.Error() }
func (e *RejectedError) Unwrap() error { return e.Err }

// ErrUnknown: parâmetro que a tela não muda.
var ErrUnknown = errors.New("parâmetro desconhecido ou que não muda pela tela")

func (s *Service) show(d *Def, v string) string {
	if d.Show != nil {
		v = d.Show(v)
	}
	if v == "" {
		return "(vazio)"
	}
	return v
}

// Apply grava os valores da tela: tudo ou nada. Os iguais ao que já vale não mudam nada.
func (s *Service) Apply(ctx context.Context, values map[string]string, by string) ([]Change, error) {
	errs := FieldErrors{}
	var rows []store.Setting
	var changes []Change
	cand := map[string]string{}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b string) int { return s.order(a) - s.order(b) })
	for _, k := range keys {
		d, ok := s.byKey[k]
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrUnknown, k)
		}
		if d.Locked != nil {
			if why := d.Locked(); why != "" {
				if strings.TrimSpace(values[k]) != s.Value(k) {
					errs[k] = why
				}
				continue
			}
		}
		v, err := d.Check(values[k])
		if err != nil {
			errs[k] = err.Error()
			continue
		}
		before := s.Value(k)
		if v == before {
			continue
		}
		cand[k] = v
		rows = append(rows, store.Setting{Key: k, Value: v, UpdatedBy: by, UpdatedAt: time.Now().UTC()})
		changes = append(changes, Change{Key: k, Label: d.Label, Before: s.show(d, before), After: s.show(d, v)})
	}
	if len(errs) > 0 {
		return nil, errs
	}
	if len(rows) == 0 {
		return nil, nil
	}
	if s.CheckAll != nil {
		look := func(key string) (string, bool) {
			if v, ok := cand[key]; ok {
				return v, true
			}
			return s.Lookup(key)
		}
		changed := make([]string, 0, len(cand))
		for _, r := range rows {
			changed = append(changed, r.Key)
		}
		if err := s.CheckAll(look, changed); err != nil {
			return nil, &RejectedError{err}
		}
	}
	if err := s.st.SetSettings(ctx, rows); err != nil {
		return nil, err
	}
	s.mu.Lock()
	for _, r := range rows {
		s.rows[r.Key] = r
		delete(s.bad, r.Key)
	}
	s.mu.Unlock()
	s.notify()
	return changes, nil
}

func (s *Service) order(key string) int {
	for i, d := range s.defs {
		if d.Key == key {
			return i
		}
	}
	return len(s.defs)
}

// Reset apaga o valor da tela: o .env (ou o padrão) volta a valer. changed=false quando não
// havia valor da tela.
func (s *Service) Reset(ctx context.Context, key, by string) (Change, bool, error) {
	d, ok := s.byKey[key]
	if !ok {
		return Change{}, false, fmt.Errorf("%w: %s", ErrUnknown, key)
	}
	before := s.Value(key)
	if s.CheckAll != nil {
		look := func(k string) (string, bool) {
			if k == key {
				return s.env(k)
			}
			return s.Lookup(k)
		}
		if err := s.CheckAll(look, []string{key}); err != nil {
			return Change{}, false, &RejectedError{fmt.Errorf("o .env não combina com o resto da tela: %w", err)}
		}
	}
	found, err := s.st.DeleteSetting(ctx, key)
	if err != nil {
		return Change{}, false, err
	}
	s.mu.Lock()
	_, had := s.rows[key]
	delete(s.rows, key)
	s.mu.Unlock()
	if !found && !had {
		return Change{}, false, nil
	}
	s.notify()
	after, _ := s.envValue(d)
	return Change{Key: key, Label: d.Label, Before: s.show(d, before), After: s.show(d, after)}, true, nil
}

// Field é um parâmetro como a tela mostra.
type Field struct {
	Def
	Value     string // o que vale (normalizado)
	Source    Source
	EnvValue  string // o que valeria sem a tela (para o "restaurar o padrão")
	EnvSource Source
	UpdatedAt time.Time
	UpdatedBy string
	LockedWhy string
	ShowValue string // Value como a tela mostra
	ShowEnv   string
}

// Group é um cartão da tela.
type Group struct {
	Name   string
	Fields []Field
}

// Groups devolve os parâmetros por cartão, na ordem das Defs.
func (s *Service) Groups() []Group {
	var out []Group
	for i := range s.defs {
		d := &s.defs[i]
		f := Field{Def: *d}
		f.EnvValue, f.EnvSource = s.envValue(d)
		s.mu.RLock()
		r, ok := s.rows[d.Key]
		s.mu.RUnlock()
		if ok {
			f.Value, f.Source, f.UpdatedAt, f.UpdatedBy = r.Value, FromScreen, r.UpdatedAt, r.UpdatedBy
		} else {
			f.Value, f.Source = f.EnvValue, f.EnvSource
		}
		if d.Locked != nil {
			f.LockedWhy = d.Locked()
		}
		f.ShowValue, f.ShowEnv = s.show(d, f.Value), s.show(d, f.EnvValue)
		if len(out) == 0 || out[len(out)-1].Name != d.Group {
			out = append(out, Group{Name: d.Group})
		}
		out[len(out)-1].Fields = append(out[len(out)-1].Fields, f)
	}
	return out
}
