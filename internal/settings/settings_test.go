package settings

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/store"
)

func dias(v string) (string, error) {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 30 || n > 3650 {
		return "", fmt.Errorf("use de 30 a 3650 dias (recebido %q)", v)
	}
	return strconv.Itoa(n), nil
}

func titulo(v string) (string, error) {
	v = strings.Trim(strings.TrimSpace(v), `"`)
	if v == "" || len([]rune(v)) > 40 {
		return "", errors.New("use de 1 a 40 caracteres")
	}
	return v, nil
}

func servico(env map[string]string, st Store) *Service {
	defs := []Def{
		{Key: "PATCHD_PANEL_TITLE", Group: "Aparência", Label: "Nome do painel", Default: "patchd", Check: titulo},
		{Key: "PATCHD_RETENTION_HISTORY_DAYS", Group: "Retenção", Label: "Histórico", Default: "90", Kind: Number, Unit: "dias", Check: dias},
		{Key: "PATCHD_REPO_CACHE_URL", Group: "Cache", Label: "URL", Check: func(v string) (string, error) { return strings.TrimSpace(v), nil },
			Locked: func() string {
				if env["PATCHD_REPO_CACHE_DIR"] == "" {
					return "exige PATCHD_REPO_CACHE_DIR"
				}
				return ""
			}},
	}
	look := func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	return New(defs, look, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// Aula 19.2: a tela vence; o .env é o padrão e o "restaurar"; tudo ou nada; conferido como
// na partida.
func TestTelaVenceEnvEhOPadrao(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	env := map[string]string{"PATCHD_PANEL_TITLE": `"patchd · TJ"`, "PATCHD_RETENTION_HISTORY_DAYS": "abc"}
	s := servico(env, st)
	mudou := s.Changed()
	if err := s.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	g := s.Groups()
	if len(g) != 3 || g[0].Fields[0].Source != FromEnv || g[1].Fields[0].Source != FromDefault || g[1].Fields[0].Value != "90" ||
		g[2].Fields[0].LockedWhy == "" {
		t.Fatalf("partida: %+v", g)
	}
	if v, ok := s.Lookup("PATCHD_PANEL_TITLE"); !ok || v != `"patchd · TJ"` {
		t.Errorf("Lookup sem tela = .env cru: %q", v)
	}

	// Tudo ou nada: um inválido, nada grava.
	_, err := s.Apply(ctx, map[string]string{"PATCHD_PANEL_TITLE": "Novo", "PATCHD_RETENTION_HISTORY_DAYS": "10"}, "admin")
	var fe FieldErrors
	if !errors.As(err, &fe) || fe["PATCHD_RETENTION_HISTORY_DAYS"] == "" || s.Value("PATCHD_PANEL_TITLE") != "patchd · TJ" {
		t.Fatalf("tudo ou nada: %v", err)
	}
	if l, _ := st.Settings(ctx); len(l) != 0 {
		t.Fatal("gravou com erro")
	}
	if _, err := s.Apply(ctx, map[string]string{"PATCHD_LDAP_HOST": "x"}, "admin"); !errors.Is(err, ErrUnknown) {
		t.Errorf("fora da lista: %v", err)
	}
	if _, err := s.Apply(ctx, map[string]string{"PATCHD_REPO_CACHE_URL": "http://x"}, "admin"); err == nil {
		t.Error("travado: exige o diretório")
	}

	ch, err := s.Apply(ctx, map[string]string{"PATCHD_PANEL_TITLE": "  Painel  ", "PATCHD_RETENTION_HISTORY_DAYS": " 120 "}, "admin")
	if err != nil || len(ch) != 2 || ch[0].String() != "Nome do painel (PATCHD_PANEL_TITLE): patchd · TJ → Painel" ||
		ch[1].Before != "90" || ch[1].After != "120" {
		t.Fatalf("aplicar: %v %v", ch, err)
	}
	select {
	case <-mudou:
	default:
		t.Error("sem aviso de mudança")
	}
	if v, _ := s.Lookup("PATCHD_RETENTION_HISTORY_DAYS"); v != "120" || s.Value("PATCHD_PANEL_TITLE") != "Painel" {
		t.Errorf("a tela vence: %q", v)
	}
	if ch, _ := s.Apply(ctx, map[string]string{"PATCHD_PANEL_TITLE": "Painel"}, "admin"); len(ch) != 0 {
		t.Error("igual ao que vale: nada muda")
	}
	f := s.Groups()[0].Fields[0]
	if f.Source != FromScreen || f.UpdatedBy != "admin" || f.EnvValue != "patchd · TJ" || f.UpdatedAt.IsZero() {
		t.Errorf("campo: %+v", f)
	}

	// Outro servidor no mesmo banco vê na releitura.
	outro := servico(env, st)
	_ = outro.Refresh(ctx)
	if outro.Value("PATCHD_RETENTION_HISTORY_DAYS") != "120" {
		t.Error("outro servidor")
	}

	// Restaurar: o .env volta (aqui, inválido no .env: vale o padrão).
	c, ok, err := s.Reset(ctx, "PATCHD_RETENTION_HISTORY_DAYS", "admin")
	if err != nil || !ok || c.Before != "120" || c.After != "90" || s.Value("PATCHD_RETENTION_HISTORY_DAYS") != "90" {
		t.Errorf("restaurar: %+v %v %v", c, ok, err)
	}
	if _, ok, _ := s.Reset(ctx, "PATCHD_RETENTION_HISTORY_DAYS", "admin"); ok {
		t.Error("restaurar de novo: nada a fazer")
	}

	// Linha inválida no banco (outra versão, mexida à mão): ignorada, vale o .env.
	_ = st.SetSettings(ctx, []store.Setting{{Key: "PATCHD_RETENTION_HISTORY_DAYS", Value: "5"}})
	_ = s.Refresh(ctx)
	if s.Value("PATCHD_RETENTION_HISTORY_DAYS") != "90" {
		t.Error("linha inválida não vale")
	}

	// Conferência do conjunto: recusa e não grava.
	s.CheckAll = func(look Lookup, changed []string) error {
		if v, _ := look("PATCHD_PANEL_TITLE"); v == "Proibido" {
			return errors.New("combinação inválida")
		}
		return nil
	}
	if _, err := s.Apply(ctx, map[string]string{"PATCHD_PANEL_TITLE": "Proibido"}, "admin"); err == nil || s.Value("PATCHD_PANEL_TITLE") != "Painel" {
		t.Errorf("conjunto: %v", err)
	}
}
