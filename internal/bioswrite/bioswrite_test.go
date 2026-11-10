package bioswrite

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/biospw"
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
func (f *fabricante) SetAdminPassword(_ context.Context, n, c []byte) (Reply, bool, error) {
	f.chamadas++
	f.novaVista, f.atual, f.temAtual = string(n), string(c), c != nil
	return hpReply(f.code), f.called, f.err
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
	for m, quer := range map[string]string{"HP": "hp", "Hewlett-Packard": "hp", "HP Inc.": "hp", "LENOVO": "lenovo", "Dell Inc.": "dell", "ASUSTeK": ""} {
		if vendorOf(m) != quer {
			t.Errorf("%s", m)
		}
	}
}

// TestCopiaNoRegistro: a cópia de emergência vai para o Registro depois do código 0; numa
// gravação incerta, vai como incerta (a anterior fica); recusa ou falha clara não mexem nela;
// a cópia de outra máquina faz recusar o job antes de tocar a BIOS.
func TestCopiaNoRegistro(t *testing.T) {
	priv, _ := machinebox.Generate()
	pub, _ := machinebox.Public(priv)
	kid := machinebox.KeyID(pub)
	guardaPriv, _ := machinebox.Generate()
	guardaPub, _ := machinebox.Public(guardaPriv)
	const maq, nova = "60ef97e4-0001", "abcd2345efgh"
	copia := func(id string) string {
		env, _ := biospw.Seal(guardaPub, id, nova)
		s, _ := biospw.Copy{MachineID: id, Guard: 1, KeyID: machinebox.KeyID(guardaPub), Escrow: env}.Format()
		return s
	}
	sealed, _ := machinebox.Seal(pub, machinebox.PurposeBIOS, []byte(nova))
	p := jobs.BIOSPasswordParams{Guard: 1, Vendor: "hp", BoxKey: kid, New: base64.StdEncoding.EncodeToString(sealed), Copy: copia(maq)}
	type gravada struct {
		copia   string
		incerta bool
	}
	run := func(f *fabricante, p jobs.BIOSPasswordParams, saveErr error) (string, []gravada, error) {
		var got []gravada
		r := Runner{BoxKeyID: kid, MachineID: maq, Logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
			Open:   func(purpose string, s []byte) ([]byte, error) { return machinebox.Open(priv, purpose, s) },
			Detect: func(context.Context) (Vendor, error) { return f, nil },
			SaveCopy: func(c string, u bool) error {
				got = append(got, gravada{c, u})
				return saveErr
			}}
		det, err := r.Run(context.Background(), 1, p)
		return det, got, err
	}

	det, got, err := run(&fabricante{nome: "hp", called: true}, p, nil)
	if err != nil || len(got) != 1 || got[0].copia != p.Copy || got[0].incerta || !strings.Contains(det, "cópia cifrada no Registro") {
		t.Fatalf("código 0: %q %v %+v", det, err, got)
	}
	if c, _ := biospw.ParseCopy(got[0].copia); func() string { s, _ := c.Reveal(guardaPriv); return s }() != nova {
		t.Error("a cópia gravada não revela a senha nova")
	}
	_, got, err = run(&fabricante{nome: "hp", called: true, code: 3}, p, nil)
	if !errors.Is(err, ErrUncertain) || len(got) != 1 || !got[0].incerta || !strings.Contains(err.Error(), "ao lado da anterior") {
		t.Errorf("incerta: %v %+v", err, got)
	}
	_, got, err = run(&fabricante{nome: "hp", called: true, err: errors.New("RPC")}, p, nil)
	if !errors.Is(err, ErrUncertain) || len(got) != 1 || !got[0].incerta {
		t.Errorf("quebrou no meio: %v %+v", err, got)
	}
	if _, got, err = run(&fabricante{nome: "hp", called: true, code: 6}, p, nil); err == nil || len(got) != 0 {
		t.Errorf("falha clara não mexe na cópia: %v %+v", err, got)
	}
	// O Registro falhou: a BIOS já mudou, o job é sucesso com a nota.
	det, _, err = run(&fabricante{nome: "hp", called: true}, p, errors.New("acesso negado"))
	if err != nil || !strings.Contains(det, "não foi para o Registro (acesso negado)") {
		t.Errorf("Registro falhou: %q %v", det, err)
	}
	// Cópia de outra máquina: recusa, nada chega ao fabricante.
	outra := p
	outra.Copy = copia("7aa604f7-0002")
	f := &fabricante{nome: "hp", called: true}
	if _, got, err := run(f, outra, nil); !errors.Is(err, jobs.ErrRefused) || f.chamadas != 0 || len(got) != 0 {
		t.Errorf("outra máquina: %v %d %+v", err, f.chamadas, got)
	}
	// Job sem cópia (servidor anterior): grava, sem Registro.
	sem := p
	sem.Copy = ""
	if det, got, err := run(&fabricante{nome: "hp", called: true}, sem, nil); err != nil || len(got) != 0 || strings.Contains(det, "Registro") {
		t.Errorf("sem cópia: %q %v %+v", det, err, got)
	}
}

// TestSenhaAtualDoMarcador (docs/bios-senha.md, seção 11): a senha em uso é a que o .bat deixou
// no Registro. O agente a lê na hora, chama o fabricante uma vez e só a apaga depois do "ok".
func TestSenhaAtualDoMarcador(t *testing.T) {
	priv, _ := machinebox.Generate()
	pub, _ := machinebox.Public(priv)
	kid := machinebox.KeyID(pub)
	const nova, antiga = "abcd2345efgh", "Exemplo@12345678"
	sealed, _ := machinebox.Seal(pub, machinebox.PurposeBIOS, []byte(nova))
	p := jobs.BIOSPasswordParams{Guard: 1, Vendor: "hp", BoxKey: kid, New: base64.StdEncoding.EncodeToString(sealed), CurrentFrom: jobs.BIOSCurrentMarker}
	var log bytes.Buffer
	type marcador struct {
		valor    string
		lerErr   error
		apagaErr error
		apagou   int
	}
	runner := func(f *fabricante, m *marcador) Runner {
		return Runner{BoxKeyID: kid, Logger: slog.New(slog.NewTextHandler(&log, nil)),
			Open:   func(purpose string, sealed []byte) ([]byte, error) { return machinebox.Open(priv, purpose, sealed) },
			Detect: func(context.Context) (Vendor, error) { return f, nil },
			Legacy: func() ([]byte, error) {
				if m.valor == "" {
					return nil, m.lerErr
				}
				return []byte(m.valor), m.lerErr
			},
			ClearLegacy: func() (bool, error) {
				if m.apagaErr != nil {
					return false, m.apagaErr
				}
				m.apagou++
				had := m.valor != ""
				m.valor = ""
				return had, nil
			}}
	}
	ctx := context.Background()

	// O caminho feliz: a do marcador vai como atual, e sai do Registro depois do código 0.
	f, m := &fabricante{nome: "hp", called: true}, &marcador{valor: antiga}
	det, err := runner(f, m).Run(ctx, 1, p)
	if err != nil || f.chamadas != 1 || f.atual != antiga || f.novaVista != nova || m.valor != "" ||
		!strings.Contains(det, "trocada (hp, código 0)") || !strings.Contains(det, "senha antiga do marcador do .bat apagada do Registro") {
		t.Fatalf("troca pelo marcador: %q %v %+v %+v", det, err, f, m)
	}
	// Não deu para apagar: a BIOS já mudou, o job segue concluído e o detalhe avisa.
	f, m = &fabricante{nome: "hp", called: true}, &marcador{valor: antiga, apagaErr: errors.New("acesso negado")}
	if det, err := runner(f, m).Run(ctx, 2, p); err != nil || !strings.Contains(det, "continua no Registro (acesso negado)") {
		t.Errorf("apagar falhou: %q %v", det, err)
	}

	// A BIOS recusou (código 6): uma chamada só, o marcador fica, e o detalhe diz o que fazer.
	f, m = &fabricante{nome: "hp", called: true, code: 6}, &marcador{valor: antiga}
	_, err = runner(f, m).Run(ctx, 3, p)
	if err == nil || errors.Is(err, jobs.ErrRefused) || errors.Is(err, ErrUncertain) || f.chamadas != 1 || m.apagou != 0 || m.valor != antiga ||
		!strings.Contains(err.Error(), "a senha do marcador do .bat não é a que a BIOS aceita") {
		t.Errorf("código 6: %v %+v %+v", err, f, m)
	}
	// Resultado incerto: nada é apagado (a senha antiga pode ser a que ainda vale).
	f, m = &fabricante{nome: "hp", called: true, err: errors.New("COM quebrou")}, &marcador{valor: antiga}
	if _, err := runner(f, m).Run(ctx, 4, p); !errors.Is(err, ErrUncertain) || m.apagou != 0 || m.valor != antiga {
		t.Errorf("incerto: %v %+v", err, m)
	}

	// Recusas: nada chega ao fabricante.
	for nome, c := range map[string]struct {
		m    *marcador
		quer string
	}{
		"marcador sem senha": {&marcador{}, "já não tem senha no Registro"},
		"erro de leitura":    {&marcador{lerErr: errors.New("acesso negado")}, "não pôde ser lido (acesso negado)"},
		"valor longo demais": {&marcador{valor: strings.Repeat("a", 33)}, "não tem formato de senha"},
		"valor com controle": {&marcador{valor: "a\x00b"}, "não tem formato de senha"},
	} {
		f := &fabricante{nome: "hp", called: true}
		_, err := runner(f, c.m).Run(ctx, 5, p)
		if !errors.Is(err, jobs.ErrRefused) || !strings.Contains(err.Error(), c.quer) || !strings.Contains(err.Error(), "não foi tocada") || f.chamadas != 0 || c.m.apagou != 0 {
			t.Errorf("%s: %v %+v", nome, err, f)
		}
	}
	semLeitor := runner(&fabricante{nome: "hp"}, &marcador{valor: antiga})
	semLeitor.Legacy = nil
	if _, err := semLeitor.Run(ctx, 6, p); !errors.Is(err, jobs.ErrRefused) || !strings.Contains(err.Error(), "não lê o marcador") {
		t.Errorf("agente sem o leitor: %v", err)
	}

	// A troca com a senha vinda no job também tira a antiga do Registro: ela deixou de valer.
	cur, _ := machinebox.Seal(pub, machinebox.PurposeBIOSCurrent, []byte("Digitada#1"))
	comAtual := p
	comAtual.CurrentFrom, comAtual.Current = "", base64.StdEncoding.EncodeToString(cur)
	f, m = &fabricante{nome: "hp", called: true}, &marcador{valor: antiga}
	if det, err := runner(f, m).Run(ctx, 7, comAtual); err != nil || f.atual != "Digitada#1" || m.valor != "" || !strings.Contains(det, "apagada do Registro") {
		t.Errorf("troca com a digitada: %q %v %+v", det, err, m)
	}
	// Sem marcador na máquina, o detalhe não fala dele.
	f, m = &fabricante{nome: "hp", called: true}, &marcador{}
	if det, err := runner(f, m).Run(ctx, 8, comAtual); err != nil || strings.Contains(det, "marcador") {
		t.Errorf("sem marcador: %q %v", det, err)
	}

	for _, s := range []string{nova, antiga, "Digitada#1"} {
		if strings.Contains(log.String(), s) {
			t.Errorf("senha no log: %s", s)
		}
	}
}
