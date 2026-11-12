package api_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/biospw"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/machinebox"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
)

// TestPadraoDaBIOS (docs/bios-config.md, seção 9): o padrão resolve "Wake on LAN ligado" e
// "boot por USB desligado" com as configurações de cada fabricante, mostra a conformidade e
// aplica em lote, com conferência, motivo e trilha; quem tem senha fora da guarda espera a
// senha em lote, e uma alteração gravada espera o reinício sem ser pedida de novo.
func TestPadraoDaBIOS(t *testing.T) {
	ctx := context.Background()
	const padrao = "/painel/bios/padrao"
	const senhaGuarda = "abcd2345efgh"
	guardaPriv, _ := machinebox.Generate()
	guardaPub, _ := machinebox.Public(guardaPriv)
	_, jobPriv, _ := jobs.GenerateKey()
	caixaPriv, _ := machinebox.Generate()
	caixaPub, _ := machinebox.Public(caixaPriv)
	h, st, log := ambientePaginas(t, api.WithBIOSEscrow(guardaPriv), api.WithJobKey(jobPriv))

	enum := func(name, value string, possible ...string) protocol.BIOSSetting {
		return protocol.BIOSSetting{Name: name, Value: value, Kind: protocol.BIOSKindEnum, Possible: possible}
	}
	hpWake := []string{"Disable", "Boot to Network", "Boot to Hard Drive"}
	rev := 0
	maquina := func(id, nome, iface string, senha bool, settings ...protocol.BIOSSetting) {
		t.Helper()
		rev++
		_, err := st.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: id + string(rune('a'+rev)),
			OS: protocol.OSInfo{Family: "windows", Hostname: nome}, Hardware: &protocol.Hardware{Sources: []string{},
				Firmware: &protocol.Firmware{Interface: iface, AdminPassword: &senha, Settings: settings}}})
		if err != nil {
			t.Fatal(err)
		}
		e := estadoTeste()
		e.BoxKey = machinebox.EncodePublic(caixaPub)
		if err := st.SaveHostState(ctx, id, e, "192.0.2.1"); err != nil {
			t.Fatal(err)
		}
	}
	const a, b, c, d = "aaaaaaaa-000a", "bbbbbbbb-000b", "cccccccc-000c", "dddddddd-000d"
	maquina(a, "pc-a", "hp", false, enum("Wake On LAN", "Disable", hpWake...), enum("S5 Maximum Power Savings", "Enable", "Disable", "Enable"),
		enum("USB Storage Boot", "Enable", "Disable", "Enable"), enum("Fast Boot", "Enable", "Disable", "Enable"))
	maquina(b, "pc-b", "dell", true, enum("WakeOnLan", "LanOnly", "Disabled", "LanOnly", "WlanOnly"), enum("UsbEmu", "Enabled", "Disabled", "Enabled"))
	maquina(c, "pc-c", "lenovo", true, enum("WakeOnLAN", "Disable", "Disable", "ACOnly", "Enable"))
	maquina(d, "pc-d", "hp", false, enum("Wake On LAN", "Boot to Hard Drive", hpWake...))
	// A pc-b já tem a senha da guarda valendo.
	env, _ := biospw.Seal(guardaPub, b, senhaGuarda)
	g, _, err := st.CreateBIOSPassword(ctx, store.BIOSPassword{MachineID: b, Escrow: env, EscrowKeyID: machinebox.KeyID(guardaPub), CreatedAt: time.Now(), CreatedBy: "thyago"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AttachBIOSJob(ctx, g.ID, 999, time.Now()); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.SettleBIOSPassword(ctx, g.ID, 999, store.BIOSActive, "teste", time.Now()); err != nil || !ok {
		t.Fatalf("guarda ativa: %v %v", ok, err)
	}

	if r := pagina(h, "GET", padrao, sessaoTeste, nil); r.Code != http.StatusForbidden {
		t.Errorf("leitura vê o padrão: %d", r.Code)
	}
	pg := pagina(h, "GET", padrao, sessaoAdmin, nil).Body.String()
	for _, quer := range []string{"Senha de administrador pela guarda", "Wake on LAN ligado", "Boot por USB desligado", `id="obj-wol"`, `id="obj-usb"`,
		`href="/painel/bios/padrao?ver=wol.corrigir">1</a>`, `href="/painel/bios/padrao?ver=wol.conforme">2</a>`, `href="/painel/bios/padrao?ver=usb.corrigir">2</a>`,
		`href="/painel/bios/padrao?ver=usb.sem">2</a>`, `href="/painel/bios/lote"`} {
		if !strings.Contains(pg, quer) {
			t.Errorf("tela: faltou %q", quer)
		}
	}
	if pg := pagina(h, "GET", padrao+"?ver=wol.fora", sessaoAdmin, nil).Body.String(); !strings.Contains(pg, "pc-c") || !strings.Contains(pg, "passe antes pela senha em lote") ||
		!strings.Contains(pg, "Ligar pela rede: Desligado") {
		t.Error("a Lenovo com senha fora da guarda: 'não dá agora', com o motivo e o valor atual")
	}
	if pg := pagina(h, "GET", padrao+"?ver=usb.sem", sessaoAdmin, nil).Body.String(); !strings.Contains(pg, "pc-d") || !strings.Contains(pg, "não informa uma configuração") {
		t.Error("o modelo sem a configuração de boot por USB")
	}

	tudo := url.Values{"objetivo": {"wol", "usb"}, "limite": {"50"}}
	confere := func(f url.Values) (string, string) {
		t.Helper()
		pg := pagina(h, "GET", padrao+"?"+f.Encode(), sessaoAdmin, nil).Body.String()
		if m := listaDoLote.FindStringSubmatch(pg); m != nil {
			return pg, m[1]
		}
		return pg, ""
	}
	envia := func(f url.Values, lista, motivo string) string {
		t.Helper()
		g := url.Values{"lista": {lista}, "motivo": {motivo}}
		for k, v := range f {
			g[k] = v
		}
		return local(pagina(h, "POST", padrao, sessaoAdmin, g))
	}
	pg, lista := confere(tudo)
	for _, quer := range []string{"2 máquina(s) neste lote", "<b>Ligar pela rede</b><br><small class=\"muted\">na BIOS: Wake On LAN</small></td><td>Desligado<br><small class=\"muted\">Disable</small></td><td><b>Ligado, iniciando o Windows</b>",
		"<b>Iniciar por pendrive ou HD externo</b><br><small class=\"muted\">na BIOS: UsbEmu</small></td><td>Ligado<br><small class=\"muted\">Enabled</small></td><td><b>Desligado</b>",
		"Economia máxima com o computador desligado: Ligado → Desligado", "Iniciar por pendrive ou HD externo: Ligado → Desligado", "Aplicar o padrão em 2 máquina(s)"} {
		if !strings.Contains(pg, quer) {
			t.Errorf("conferência: faltou %q", quer)
		}
	}
	if strings.Contains(pg, "Fast Boot") {
		t.Error("conferência: uma configuração fora do padrão entrou")
	}
	if l := envia(tudo, lista, ""); !strings.Contains(l, "erro=motivo") {
		t.Errorf("sem motivo: %s", l)
	}
	if l := envia(tudo, strings.Repeat("0", 32), "chamado 4800"); !strings.Contains(l, "erro=mudou") {
		t.Errorf("lista diferente: %s", l)
	}
	if l := local(pagina(h, "POST", padrao, sessaoTeste, url.Values{"objetivo": {"wol"}, "lista": {lista}, "motivo": {"chamado 4800"}})); l != "" {
		t.Errorf("leitura aplica o padrão: %s", l)
	}
	if l := envia(tudo, lista, "chamado 4800, padrão da BIOS"); l != padrao+"?feitas=2&puladas=0" {
		t.Fatalf("aplicar: %s", l)
	}

	job := func(id string) (jobs.BIOSSettingsParams, store.JobRow) {
		t.Helper()
		jl, _ := st.Jobs(ctx, id, 1)
		if len(jl) == 0 {
			t.Fatalf("%s: nenhum job", id)
		}
		j, _, _ := st.JobForMachine(ctx, id, jl[0].ID)
		var payload jobs.Job
		_ = json.Unmarshal([]byte(j.Payload), &payload)
		p, err := jobs.ParseBIOSSettingsParams(payload.Params)
		if err != nil || j.Type != jobs.TypeBIOSSettings {
			t.Fatalf("%s: %v (%s)", id, err, j.Type)
		}
		return p, j
	}
	pa, ja := job(a)
	if pa.Vendor != "hp" || pa.Password != "" || len(pa.Changes) != 3 || pa.Changes[0].Name != "Wake On LAN" || pa.Changes[0].Value != "Boot to Hard Drive" ||
		pa.Changes[1].Value != "Disable" || pa.Changes[2].Name != "USB Storage Boot" {
		t.Errorf("job da pc-a: %+v", pa)
	}
	pb, jb := job(b)
	raw, _ := base64.StdEncoding.DecodeString(pb.Password)
	if pw, err := machinebox.Open(caixaPriv, machinebox.PurposeBIOSConfig, raw); err != nil || string(pw) != senhaGuarda || len(pb.Changes) != 1 ||
		pb.Changes[0].Name != "UsbEmu" || pb.Changes[0].Value != "Disabled" {
		t.Errorf("job da pc-b: %+v %v", pb.Changes, err)
	}
	if jl, _ := st.Jobs(ctx, c, 5); len(jl) != 0 {
		t.Error("a máquina com senha fora da guarda recebeu job")
	}
	ev, _ := st.AuditEvents(ctx, store.AuditFilter{Action: "bios.padrao.lote"})
	if len(ev) != 1 || !strings.Contains(ev[0].Detail, "2 máquina(s): Wake on LAN ligado, Boot por USB desligado") || !strings.Contains(ev[0].Detail, "motivo: chamado 4800, padrão da BIOS") {
		t.Errorf("trilha do lote: %+v", ev)
	}
	cj, _ := st.AuditEvents(ctx, store.AuditFilter{Action: "bios.config.job"})
	if len(cj) != 2 || !strings.Contains(cj[0].Detail, "padrão da BIOS, lote ") {
		t.Errorf("trilha das máquinas: %+v", cj)
	}
	if strings.Contains(log.String(), senhaGuarda) {
		t.Error("senha no log")
	}

	// Com os jobs a caminho, ninguém volta para o lote.
	if _, lista := confere(tudo); lista != "" {
		t.Error("máquina com alteração a caminho voltou para o lote")
	}
	// A pc-a gravou; o inventário ainda mostra o valor antigo: espera o reinício, sem novo pedido.
	if _, err := st.FinishJob(ctx, ja.ID, jobs.StateDone, "agente: 3 gravadas", time.Now()); err != nil {
		t.Fatal(err)
	}
	// A pc-b falhou: sai dos lotes por um dia.
	if _, err := st.FinishJob(ctx, jb.ID, jobs.StateFailed, "o agente informou falha: dell respondeu 1", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, lista := confere(tudo); lista != "" {
		t.Error("a que espera o reinício ou a que falhou voltaram para o lote")
	}
	if pg := pagina(h, "GET", padrao+"?ver=wol.reinicio", sessaoAdmin, nil).Body.String(); !strings.Contains(pg, "pc-a") {
		t.Error("a pc-a não está em 'aguardando reinício'")
	}
	if pg := pagina(h, "GET", padrao+"?ver=usb.fora", sessaoAdmin, nil).Body.String(); !strings.Contains(pg, "pc-b") || !strings.Contains(pg, "a última alteração falhou") {
		t.Error("a pc-b não está em 'não dá agora' com a falha")
	}
	// Depois do reinício, o inventário mostra o padrão: conforme.
	maquina(a, "pc-a", "hp", false, enum("Wake On LAN", "Boot to Hard Drive", hpWake...), enum("S5 Maximum Power Savings", "Disable", "Disable", "Enable"),
		enum("USB Storage Boot", "Disable", "Disable", "Enable"))
	pg = pagina(h, "GET", padrao, sessaoAdmin, nil).Body.String()
	if !strings.Contains(pg, `href="/painel/bios/padrao?ver=wol.conforme">3</a>`) || !strings.Contains(pg, `href="/painel/bios/padrao?ver=usb.conforme">1</a>`) {
		t.Error("depois do reinício a pc-a não conta como conforme")
	}
}
