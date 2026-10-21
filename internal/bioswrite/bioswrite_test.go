package bioswrite

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/machinebox"
)

// fabricante: um Vendor de mentira que guarda o que recebeu.
type fabricante struct {
	nome             string
	sure             bool
	sureErr          error
	code             uint32
	called           bool
	err              error
	chamadas         int
	novaVista, atual string
	temAtual         bool
}

func (f *fabricante) Name() string                            { return f.nome }
func (f *fabricante) SureAdmin(context.Context) (bool, error) { return f.sure, f.sureErr }
func (f *fabricante) SetAdminPassword(_ context.Context, n, c []byte) (uint32, bool, error) {
	f.chamadas++
	f.novaVista, f.atual, f.temAtual = string(n), string(c), c != nil
	return f.code, f.called, f.err
}

func TestGravacao(t *testing.T) {
	priv, _ := machinebox.Generate()
	pub, _ := machinebox.Public(priv)
	kid := machinebox.KeyID(pub)
	fecha := func(purpose, pw string) string {
		b, _ := machinebox.Seal(pub, purpose, []byte(pw))
		return base64.StdEncoding.EncodeToString(b)
	}
	const nova = "abcd2345efgh"
	params := func(atual string) jobs.BIOSPasswordParams {
		p := jobs.BIOSPasswordParams{Guard: 1, Vendor: "hp", BoxKey: kid, New: fecha(machinebox.PurposeBIOS, nova)}
		if atual != "" {
			p.Current = fecha(machinebox.PurposeBIOSCurrent, atual)
		}
		return p
	}
	var log bytes.Buffer
	runner := func(f *fabricante) Runner {
		return Runner{BoxKeyID: kid, Logger: slog.New(slog.NewTextHandler(&log, nil)),
			Open:   func(purpose string, sealed []byte) ([]byte, error) { return machinebox.Open(priv, purpose, sealed) },
			Detect: func(context.Context) (Vendor, error) { return f, nil }}
	}
	ctx := context.Background()

	// Troca: a nova e a atual chegam abertas ao fabricante, uma chamada só.
	f := &fabricante{nome: "hp", called: true}
	det, err := runner(f).Run(ctx, 7, params("Frota#2024"))
	if err != nil || f.chamadas != 1 || f.novaVista != nova || f.atual != "Frota#2024" || !strings.Contains(det, "trocada (hp, código 0)") {
		t.Fatalf("troca: %q %v %+v", det, err, f)
	}
	// Definição (BIOS sem senha): a atual vai vazia.
	f = &fabricante{nome: "hp", called: true}
	if det, err := runner(f).Run(ctx, 8, params("")); err != nil || f.temAtual || !strings.Contains(det, "definida") {
		t.Errorf("definição: %q %v %+v", det, err, f)
	}
	if strings.Contains(log.String(), nova) || strings.Contains(log.String(), "Frota#2024") {
		t.Error("senha no log")
	}

	// Recusas: nada chega ao fabricante.
	outraChave := params("")
	outraChave.BoxKey = "ffffffffffff"
	trocada := params("x")
	trocada.New, trocada.Current = trocada.Current, trocada.New // propósitos trocados
	foraDoFormato := params("")
	foraDoFormato.New = fecha(machinebox.PurposeBIOS, "curta")
	for nome, c := range map[string]struct {
		f    *fabricante
		p    jobs.BIOSPasswordParams
		quer string
	}{
		"outra chave da caixa": {&fabricante{nome: "hp"}, outraChave, "fechados para a chave ffffffffffff"},
		"envelopes trocados":   {&fabricante{nome: "hp"}, trocada, "envelope da senha nova não abre"},
		"nova fora do formato": {&fabricante{nome: "hp"}, foraDoFormato, "fora do formato"},
		"outro fabricante":     {&fabricante{nome: "dell"}, params(""), `interface "hp"`},
		"sure admin":           {&fabricante{nome: "hp", sure: true}, params(""), "HP Sure Admin ligado"},
		"sure admin ilegível":  {&fabricante{nome: "hp", sureErr: errors.New("x")}, params(""), "Sure Admin"},
		"chamada não feita":    {&fabricante{nome: "hp", err: errors.New("WMI: método"), called: false}, params(""), "não chegou a ser feita"},
	} {
		det, err := runner(c.f).Run(ctx, 9, c.p)
		if !errors.Is(err, jobs.ErrRefused) || !strings.Contains(err.Error(), c.quer) || !strings.Contains(err.Error(), "não foi tocada") || det != "" {
			t.Errorf("%s: %v", nome, err)
		}
		if nome != "chamada não feita" && c.f.chamadas != 0 {
			t.Errorf("%s: o fabricante foi chamado", nome)
		}
	}
	r := runner(&fabricante{nome: "hp"})
	r.Detect = func(context.Context) (Vendor, error) { return nil, ErrNoVendor }
	if _, err := r.Run(ctx, 10, params("")); !errors.Is(err, jobs.ErrRefused) {
		t.Errorf("sem interface: %v", err)
	}

	// Respostas do fabricante: falha clara (a anterior vale) e incerta.
	if _, err := runner(&fabricante{nome: "hp", called: true, code: 6}).Run(ctx, 11, params("errada")); err == nil ||
		errors.Is(err, ErrUncertain) || errors.Is(err, jobs.ErrRefused) || !strings.Contains(err.Error(), "6 (acesso negado") {
		t.Errorf("código 6: %v", err)
	}
	for _, f := range []*fabricante{{nome: "hp", called: true, code: 3}, {nome: "hp", called: true, code: 99},
		{nome: "hp", called: true, err: errors.New("RPC caiu")}} {
		if _, err := runner(f).Run(ctx, 12, params("")); !errors.Is(err, ErrUncertain) {
			t.Errorf("incerto (%d, %v): %v", f.code, f.err, err)
		}
	}
}

func TestHP(t *testing.T) {
	if hpPassword([]byte("abc")) != "<utf-16/>abc" || hpPassword(nil) != "<utf-16/>" {
		t.Error("prefixo")
	}
	for v, quer := range map[any]bool{"Enable": true, " enable ": true, "Disable": false, nil: false, 1: false} {
		if hpSureAdminOn(v) != quer {
			t.Errorf("%v", v)
		}
	}
	for m, quer := range map[string]string{"HP": "hp", "Hewlett-Packard": "hp", "HP Inc.": "hp", "LENOVO": "", "Dell Inc.": ""} {
		if vendorOf(m) != quer {
			t.Errorf("%s", m)
		}
	}
}
