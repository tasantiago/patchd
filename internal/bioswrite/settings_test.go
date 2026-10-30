package bioswrite

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/biosconfig"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/machinebox"
	"github.com/tasantiago/patchd/internal/protocol"
)

// biosFalsa: um SettingsVendor de mentira, com as configurações e os códigos por nome.
type biosFalsa struct {
	sure     bool
	settings []protocol.BIOSSetting
	codes    map[string]uint32 // código por configuração (ausente: 0)
	quebra   map[string]bool   // a chamada quebra no meio
	gravadas []string
	senhas   []string
}

func (b *biosFalsa) Name() string                            { return "hp" }
func (b *biosFalsa) SureAdmin(context.Context) (bool, error) { return b.sure, nil }
func (b *biosFalsa) Settings(context.Context) ([]protocol.BIOSSetting, error) {
	return b.settings, nil
}
func (b *biosFalsa) SetSetting(_ context.Context, name, value string, pw []byte) (uint32, bool, error) {
	b.gravadas = append(b.gravadas, name+"="+value)
	b.senhas = append(b.senhas, string(pw))
	if b.quebra[name] {
		return 0, true, errors.New("RPC_E_DISCONNECTED")
	}
	return b.codes[name], true, nil
}

// bitlockerFalso conta as chamadas.
type bitlockerFalso struct {
	on                  bool
	suspendErr          error
	suspensos, religado int
}

func (b *bitlockerFalso) Protected(context.Context) (bool, error) { return b.on, nil }
func (b *bitlockerFalso) SuspendOneReboot(context.Context) error {
	if b.suspendErr != nil {
		return b.suspendErr
	}
	b.suspensos++
	return nil
}
func (b *bitlockerFalso) Resume(context.Context) error { b.religado++; return nil }

func enumSetting(name, value string, possible ...string) protocol.BIOSSetting {
	return protocol.BIOSSetting{Name: name, Kind: protocol.BIOSKindEnum, Value: value, Possible: possible}
}

func TestBIOSSettings(t *testing.T) {
	priv, _ := machinebox.Generate()
	pub, _ := machinebox.Public(priv)
	kid := machinebox.KeyID(pub)
	const senha = "abcd2345efgh"
	sealed := func(purpose string) string {
		b, _ := machinebox.Seal(pub, purpose, []byte(senha))
		return base64.StdEncoding.EncodeToString(b)
	}
	base := func() []protocol.BIOSSetting {
		return []protocol.BIOSSetting{
			enumSetting("Wake On LAN", "Disable", "Disable", "Boot to Network", "Boot to Hard Drive"),
			enumSetting("Fast Boot", "Enable", "Disable", "Enable"),
			enumSetting("Virtualization Technology (VTx)", "Disable", "Disable", "Enable"),
			enumSetting("TPM State", "Enable", "Disable", "Enable"),
		}
	}
	var log bytes.Buffer
	run := func(b *biosFalsa, bl *bitlockerFalso, p jobs.BIOSSettingsParams) (string, error) {
		r := SettingsRunner{BoxKeyID: kid, Logger: slog.New(slog.NewTextHandler(&log, nil)), BitLocker: bl,
			Open:   func(purpose string, s []byte) ([]byte, error) { return machinebox.Open(priv, purpose, s) },
			Detect: func(context.Context) (SettingsVendor, error) { return b, nil }}
		return r.Run(context.Background(), 7, p)
	}
	params := func(ch ...biosconfig.Change) jobs.BIOSSettingsParams {
		return jobs.BIOSSettingsParams{Vendor: "hp", BoxKey: kid, Password: sealed(machinebox.PurposeBIOSConfig), Changes: ch}
	}
	wol := biosconfig.Change{Name: "Wake On LAN", Value: "Boot to Hard Drive"}
	vtx := biosconfig.Change{Name: "Virtualization Technology (VTx)", Value: "Enable"}
	fast := biosconfig.Change{Name: "Fast Boot", Value: "Enable"} // já está assim

	t.Run("grava, suspende o BitLocker e usa a senha", func(t *testing.T) {
		b, bl := &biosFalsa{settings: base()}, &bitlockerFalso{on: true}
		detail, err := run(b, bl, params(wol, vtx, fast))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(b.gravadas, ";") != "Wake On LAN=Boot to Hard Drive;Virtualization Technology (VTx)=Enable" {
			t.Fatalf("gravadas: %v", b.gravadas)
		}
		if b.senhas[0] != senha || bl.suspensos != 1 || bl.religado != 0 {
			t.Fatalf("senha %q, suspensos %d, religado %d", b.senhas[0], bl.suspensos, bl.religado)
		}
		for _, want := range []string{"Wake On LAN: gravada (Disable → Boot to Hard Drive)", "Fast Boot: já estava assim",
			"valem no próximo reinício", "BitLocker suspenso"} {
			if !strings.Contains(detail, want) {
				t.Errorf("detalhe sem %q: %s", want, detail)
			}
		}
		if strings.Contains(detail, senha) || strings.Contains(log.String(), senha) {
			t.Fatal("a senha vazou no detalhe ou no log")
		}
	})

	t.Run("sem BitLocker ligado não suspende", func(t *testing.T) {
		b, bl := &biosFalsa{settings: base()}, &bitlockerFalso{}
		if _, err := run(b, bl, params(wol)); err != nil || bl.suspensos != 0 {
			t.Fatalf("err %v, suspensos %d", err, bl.suspensos)
		}
	})

	t.Run("nada a mudar: nem BitLocker nem chamada", func(t *testing.T) {
		b, bl := &biosFalsa{settings: base()}, &bitlockerFalso{on: true}
		detail, err := run(b, bl, params(fast))
		if err != nil || len(b.gravadas) != 0 || bl.suspensos != 0 || !strings.Contains(detail, "já estava assim") {
			t.Fatalf("err %v, gravadas %v, suspensos %d: %s", err, b.gravadas, bl.suspensos, detail)
		}
	})

	t.Run("senha errada para tudo e religa o BitLocker", func(t *testing.T) {
		b, bl := &biosFalsa{settings: base(), codes: map[string]uint32{"Wake On LAN": 6}}, &bitlockerFalso{on: true}
		_, err := run(b, bl, params(wol, vtx))
		if err == nil || errors.Is(err, jobs.ErrRefused) {
			t.Fatalf("quer falha (não recusa): %v", err)
		}
		if len(b.gravadas) != 1 {
			t.Fatalf("depois do código 6 não pode haver outra chamada: %v", b.gravadas)
		}
		if !strings.Contains(err.Error(), "não tentada") || bl.religado != 1 {
			t.Fatalf("religado %d: %v", bl.religado, err)
		}
	})

	t.Run("código definitivo numa não impede as outras", func(t *testing.T) {
		b, bl := &biosFalsa{settings: base(), codes: map[string]uint32{"Wake On LAN": 5}}, &bitlockerFalso{on: true}
		_, err := run(b, bl, params(wol, vtx))
		if err == nil || len(b.gravadas) != 2 || bl.religado != 0 {
			t.Fatalf("err %v, gravadas %v, religado %d", err, b.gravadas, bl.religado)
		}
	})

	t.Run("quebra no meio para e deixa o BitLocker suspenso", func(t *testing.T) {
		b, bl := &biosFalsa{settings: base(), quebra: map[string]bool{"Wake On LAN": true}}, &bitlockerFalso{on: true}
		_, err := run(b, bl, params(wol, vtx))
		if err == nil || len(b.gravadas) != 1 || bl.religado != 0 || !strings.Contains(err.Error(), "resultado incerto") {
			t.Fatalf("err %v, gravadas %v, religado %d", err, b.gravadas, bl.religado)
		}
	})

	// Recusas: a BIOS não é tocada (nenhuma chamada, nenhuma suspensão).
	recusas := []struct {
		desc string
		b    *biosFalsa
		bl   *bitlockerFalso
		p    jobs.BIOSSettingsParams
	}{
		{"outra caixa", &biosFalsa{settings: base()}, &bitlockerFalso{on: true},
			func() jobs.BIOSSettingsParams { p := params(wol); p.BoxKey = "000000000000"; return p }()},
		{"envelope com o rótulo da senha", &biosFalsa{settings: base()}, &bitlockerFalso{on: true},
			func() jobs.BIOSSettingsParams {
				p := params(wol)
				p.Password = sealed(machinebox.PurposeBIOSCurrent)
				return p
			}()},
		{"Sure Admin", &biosFalsa{settings: base(), sure: true}, &bitlockerFalso{on: true}, params(wol)},
		{"BitLocker não suspende", &biosFalsa{settings: base()}, &bitlockerFalso{on: true, suspendErr: errors.New("acesso negado")}, params(wol)},
	}
	for _, c := range recusas {
		t.Run("recusa: "+c.desc, func(t *testing.T) {
			_, err := run(c.b, c.bl, c.p)
			if !errors.Is(err, jobs.ErrRefused) || len(c.b.gravadas) != 0 || c.bl.suspensos != 0 {
				t.Fatalf("err %v, gravadas %v, suspensos %d", err, c.b.gravadas, c.bl.suspensos)
			}
		})
	}

	t.Run("fora da lista ou fora dos possíveis da hora: recusada pelo agente", func(t *testing.T) {
		// Um job assinado com uma configuração que a BIOS de agora mostra só leitura, e outra
		// com valor que a máquina não aceita mais: nenhuma chamada.
		settings := base()
		settings[0].ReadOnly = true
		b, bl := &biosFalsa{settings: settings}, &bitlockerFalso{on: true}
		_, err := run(b, bl, params(wol, biosconfig.Change{Name: "Fast Boot", Value: "Turbo"}))
		if err == nil || len(b.gravadas) != 0 || bl.suspensos != 0 || !strings.Contains(err.Error(), "recusada pelo agente") {
			t.Fatalf("err %v, gravadas %v, suspensos %d", err, b.gravadas, bl.suspensos)
		}
	})

	t.Run("BIOS sem senha: só o prefixo vai na chamada", func(t *testing.T) {
		b, bl := &biosFalsa{settings: base()}, &bitlockerFalso{}
		p := params(wol)
		p.Password = ""
		if _, err := run(b, bl, p); err != nil || b.senhas[0] != "" {
			t.Fatalf("err %v, senha %q", err, b.senhas[0])
		}
	})
}

func TestHPEnumSettings(t *testing.T) {
	got := hpEnumSettings([]map[string]any{
		{"Name": "Wake On LAN", "Path": `\Advanced`, "CurrentValue": " Disable ", "PossibleValues": []any{"Disable", " ", "Boot to Network"}, "IsReadOnly": uint32(0)},
		{"Name": "Wake On LAN", "CurrentValue": "x"},
		{"Name": "Secure Boot", "CurrentValue": "Enable", "PossibleValues": []any{"Disable", "Enable"}, "IsReadOnly": int32(1)},
	})
	if len(got) != 2 || got[0].Value != "Disable" || len(got[0].Possible) != 2 || got[0].ReadOnly || !got[1].ReadOnly {
		t.Fatalf("%+v", got)
	}
}
