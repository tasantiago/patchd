package tpinstall

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tasantiago/patchd/internal/installomator"
	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/thirdparty"
)

func appMac(nome, versao, escopo string) protocol.Software {
	return protocol.Software{Name: nome, Version: versao, Scope: escopo}
}

// O plano do Mac: a versão aprovada é o alvo; o Installomator atualiza a cópia de
// /Applications; duas cópias, só na pasta do usuário ou sem aprovação ficam fora.
func TestPlanoDoMac(t *testing.T) {
	m, _ := thirdparty.Default()
	alvo := func(app string) string {
		return map[string]string{"google-chrome": "155.0.8059.40", "mozilla-firefox": "157.0.1"}[app]
	}
	casos := []struct {
		sw    []protocol.Software
		state string
	}{
		{[]protocol.Software{appMac("Google Chrome", "154.0.8037.93", "machine")}, StateUpdate},
		{[]protocol.Software{appMac("Google Chrome", "155.0.8059.40", "machine")}, StateCurrent},
		{[]protocol.Software{appMac("Google Chrome", "156.0", "machine")}, StateCurrent},
		{[]protocol.Software{appMac("Google Chrome", "154.0", "user")}, StateUserOnly},
		{[]protocol.Software{appMac("Google Chrome", "154.0", "machine"), appMac("Google Chrome", "153.0", "machine")}, StateDuplicate},
		{[]protocol.Software{appMac("Google Chrome", "Auto", "machine")}, StateBadVersion},
		{[]protocol.Software{appMac("Visual Studio Code", "1.130.0", "machine")}, StateNoPackage}, // sem versão aprovada
	}
	for _, c := range casos {
		ds := PlanMac(c.sw, m, alvo)
		if len(ds) != 1 || ds[0].State != c.state {
			t.Errorf("%+v: %+v", c.sw, ds)
		}
	}
	d := PlanMac([]protocol.Software{appMac("Google Chrome", "154.0.8037.93", "machine")}, m, alvo)[0]
	if d.Target != "155.0.8059.40" || d.TargetVersion() != "155.0.8059.40" || !strings.Contains(d.Reason, "googlechromepkg") {
		t.Errorf("decisão: %+v", d)
	}
	p, err := MacJobParams(d, MacScript{SHA256: strings.Repeat("a", 64), Size: 677099})
	if err != nil || p.Installer != jobs.InstallerInstallomator || len(p.Args) != 1 || p.Args[0] != "googlechromepkg" || p.Version != "155.0.8059.40" {
		t.Errorf("job: %+v %v", p, err)
	}
	// O Windows não vê as regras do Mac, e o Mac não vê as do Windows.
	if ds := Plan([]protocol.Software{appMac("Google Chrome", "154.0", "machine")}, m, nil, "x64"); len(ds) != 0 {
		t.Errorf("plano do Windows com app do Mac: %+v", ds)
	}
	if _, err := MacJobParams(Decision{State: StateCurrent}, MacScript{}); err == nil {
		t.Error("job de decisão em dia")
	}
}

// A campanha nos Macs: o agente tem de anunciar o Installomator, e o script tem de conferir
// no cache; sem instalador do Windows guardado, a versão fica pronta só pelos Macs.
func TestCampanhaNoMac(t *testing.T) {
	ctx := context.Background()
	m, _ := thirdparty.Default()
	st := store.NewMemory()
	dir := t.TempDir()
	conteudo := []byte("#!/bin/zsh\n# Installomator de teste\n")
	h := sha256.Sum256(conteudo)
	script := installomator.Script{Version: "10.10", SHA256: hex.EncodeToString(h[:])}
	c := Campaigner{Store: st, Manifest: m, Dir: dir, Installomator: &script}
	mac := func(id string, sw ...protocol.Software) {
		st.SaveInventory(ctx, id, protocol.InventoryReport{SchemaVersion: 1, Hash: id, OS: protocol.OSInfo{ID: "macos", Arch: "arm64"}, Software: sw})
	}
	mac("mac-velho", appMac("Google Chrome", "154.0.8037.93", "machine"))
	mac("mac-novo", appMac("Google Chrome", "155.0.8059.40", "machine"))
	mac("mac-antigo-agente", appMac("Google Chrome", "154.0.8037.93", "machine"))
	for _, id := range []string{"mac-velho", "mac-novo"} {
		st.SetMachineCapabilities(ctx, id, []string{protocol.CapabilityJobs, protocol.CapabilityInstallomator})
	}
	st.ApproveThirdParty(ctx, store.ThirdPartyApproval{AppID: "google-chrome", Version: "155.0.8059.40", ApprovedBy: "ana"})

	// Sem o script no cache: a versão não fica pronta, e a máquina diz por quê.
	if r, err := c.Ready(ctx, "google-chrome", "155.0.8059.40"); err != nil || !strings.Contains(r, "Installomator 10.10 não está no cache") {
		t.Errorf("sem script: %q %v", r, err)
	}
	if p, skip, _ := c.Job(ctx, "mac-velho", "google-chrome", "155.0.8059.40"); p != nil || !strings.Contains(skip, "não está no cache") {
		t.Errorf("sem script, a máquina: %+v %q", p, skip)
	}
	if err := os.WriteFile(filepath.Join(dir, installomator.FileName(script.SHA256)), conteudo, 0o640); err != nil {
		t.Fatal(err)
	}
	if r, err := c.Ready(ctx, "google-chrome", "155.0.8059.40"); err != nil || r != "" {
		t.Errorf("pronta: %q %v", r, err)
	}
	p, skip, err := c.Job(ctx, "mac-velho", "google-chrome", "155.0.8059.40")
	if err != nil || p == nil || skip != "" || p.Installer != jobs.InstallerInstallomator || p.SHA256 != script.SHA256 ||
		p.Size != int64(len(conteudo)) || p.Args[0] != "googlechromepkg" {
		t.Fatalf("job: %+v %q %v", p, skip, err)
	}
	for id, want := range map[string]string{"mac-novo": "em dia", "mac-antigo-agente": "atualize o agente"} {
		if p, skip, err := c.Job(ctx, id, "google-chrome", "155.0.8059.40"); err != nil || p != nil || !strings.Contains(skip, want) {
			t.Errorf("%s: %+v %q %v; esperava %q", id, p, skip, err, want)
		}
	}
	// Programa sem regra do Installomator no Mac.
	mac("mac-7zip", appMac("7-Zip", "26.02", "machine"))
	st.SetMachineCapabilities(ctx, "mac-7zip", []string{protocol.CapabilityInstallomator})
	if _, skip, _ := c.Job(ctx, "mac-7zip", "7zip", "26.04"); skip != "sem regra do Installomator para o macOS" {
		t.Errorf("sem regra: %q", skip)
	}
	// O script adulterado no cache derruba a versão de novo.
	os.WriteFile(filepath.Join(dir, installomator.FileName(script.SHA256)), []byte("trocado"), 0o640)
	if r, _ := c.Ready(ctx, "google-chrome", "155.0.8059.40"); !strings.Contains(r, "não confere") {
		t.Errorf("adulterado: %q", r)
	}
	// Servidor sem o Installomator configurado.
	if p, skip, _ := (Campaigner{Store: st, Manifest: m, Dir: dir}).Job(ctx, "mac-velho", "google-chrome", "155.0.8059.40"); p != nil ||
		!strings.Contains(skip, "sem o Installomator configurado") {
		t.Errorf("sem configuração: %q", skip)
	}
	// A versão aprovada entra na lista da campanha de todos os programas.
	if l, _ := c.Approved(ctx); len(l) != 1 || l[0].App != "google-chrome" {
		t.Errorf("aprovados: %+v", l)
	}
}

// A conferência do job pelo inventário (RF-08) reconhece o programa pela regra do sistema
// da máquina: no Mac, a do macOS.
func TestEntradasPorSistema(t *testing.T) {
	m, _ := thirdparty.Default()
	sw := []protocol.Software{appMac("Google Chrome", "155.0.8059.40", "machine")}
	mac, _ := Entries("macos", sw, m, "google-chrome")
	if len(mac) != 1 || mac[0].Kind != KindApp || mac[0].Installed != "155.0.8059.40" {
		t.Errorf("macOS: %+v", mac)
	}
	if win, _ := Entries("windows", sw, m, "google-chrome"); len(win) != 0 {
		t.Errorf("a regra do Windows reconheceu o app do Mac: %+v", win) // a do Windows exige o fabricante Google LLC
	}
}
