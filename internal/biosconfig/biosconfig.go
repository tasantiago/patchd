// Package biosconfig é a lista fechada das configurações da BIOS que o patchd pode alterar
// (desenho em docs/bios-config.md, seção 3). O servidor a usa para mostrar o seletor no
// painel; o agente, para recusar o que não estiver nela, mesmo vindo num job assinado.
//
// Os nomes mudam de modelo para modelo (a coleta de Wake on LAN já lida com isso), então as
// regras são por nome normalizado. As exclusões vencem qualquer regra, menos os nomes exatos
// que uma regra declara de propósito.
package biosconfig

import (
	"fmt"
	"strings"

	"github.com/tasantiago/patchd/internal/protocol"
)

// Categorias, na ordem em que o painel as mostra.
const (
	CatPower   = "Energia e Wake on LAN"
	CatBoot    = "Boot"
	CatVirtSec = "Virtualização e segurança"
	CatDevices = "Portas e dispositivos"
)

// Categories: a ordem das categorias editáveis.
var Categories = []string{CatPower, CatBoot, CatVirtSec, CatDevices}

// Rule diz que um grupo de configurações pode ser alterado.
type Rule struct {
	Category string
	// Risk: a mudança pode tirar a máquina da rede, desligar teclado e mouse ou mexer na
	// segurança da partida; o painel pede confirmação explícita.
	Risk bool
	Note string // o aviso que o painel mostra junto
	// exact: nomes normalizados aceitos mesmo com uma exclusão no meio (são a configuração
	// inteira, conferida, e não um pedaço de nome).
	exact []string
	// contains: pedaços de nome normalizado; só valem depois das exclusões.
	contains []string
	// denyValues: pedaços de valor normalizado recusados (ex.: ligar o suporte legado).
	denyValues []string
}

// excluded: pedaços de nome que nunca são alterados pelo patchd.
var excluded = []string{
	"password", "pwd", "tpm", "clear", "reset", "restore", "factory", "default",
	"sureadmin", "enhancedbiosauthentication", "physicalpresence", "permanent",
	"downgrade", "surestart", "flash", "firmwareupdate", "biosupdate", "updatebios", "lockbiosversion",
	"bootmode", "legacy", "uefibootorder", "bootorder", "keys", "import", "custom",
	"absolute", "computrace", "lojack",
	// A placa cabeada: desligar tira a máquina do patchd.
	"networkinterfacecontroller", "integratednic", "embeddednic", "onboardnic", "lancontroller", "integratedlan",
}

// rules, na ordem de busca: a primeira que casa decide.
var rules = []Rule{
	{Category: CatPower, contains: []string{
		"wakeonlan", "wakeonwlan", "wakeonusb", "deepsleep", "maximumpowersaving", "maxpowersaving",
		"afterpowerloss", "poweronwhenacdetected", "poweronwhenlidisopened", "wakeunitfromsleep",
	}},
	{Category: CatBoot, contains: []string{
		"fastboot", "usbstorageboot", "usbboot", "networkpxeboot", "pxeboot", "networkboot",
		"startupdelay", "audioalertsduringboot", "numlockonatboot", "promptonmemorysizechange",
	}},
	{Category: CatVirtSec, Risk: true,
		Note:       "muda o que o TPM mede na partida: o BitLocker é suspenso por um reinício antes da gravação",
		exact:      []string{"configurelegacysupportandsecureboot"},
		contains:   []string{"virtualizationtechnology", "vtx", "vtd", "svmcpuvirtualization", "secureboot"},
		denyValues: []string{"legacysupportenable"},
	},
	{Category: CatDevices, Risk: true,
		Note:     "pode tirar a máquina da rede (Wi-Fi) ou desligar teclado e mouse (USB, Bluetooth)",
		contains: []string{"wirelessnetworkdevice", "wlan", "bluetooth", "usbport"},
	},
	{Category: CatDevices, contains: []string{
		"integratedcamera", "camera", "microphone", "internalspeaker", "fingerprint", "mediacardreader",
		"smartcard", "cardreader",
	}},
}

// Norm: minúsculas, sem espaços, hífens, sublinhados, barras, pontos, vírgulas, dois pontos e
// parênteses ("Virtualization Technology (VTx)" → "virtualizationtechnologyvtx").
func Norm(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '-', '_', '/', '\\', '\t', '(', ')', '.', ',', ':':
			return -1
		}
		return r
	}, strings.ToLower(s))
}

// Lookup devolve a regra que permite alterar a configuração, se houver.
func Lookup(name string) (Rule, bool) {
	n := Norm(name)
	if n == "" {
		return Rule{}, false
	}
	for _, r := range rules {
		for _, e := range r.exact {
			if n == e {
				return r, true
			}
		}
	}
	for _, x := range excluded {
		if strings.Contains(n, x) {
			return Rule{}, false
		}
	}
	for _, r := range rules {
		for _, c := range r.contains {
			if strings.Contains(n, c) {
				return r, true
			}
		}
	}
	return Rule{}, false
}

// ValueAllowed diz se o valor pode ser pedido nesta regra (antes da conferência com os
// valores possíveis da máquina).
func (r Rule) ValueAllowed(value string) bool {
	n := Norm(value)
	if n == "" {
		return false
	}
	for _, d := range r.denyValues {
		if strings.Contains(n, d) {
			return false
		}
	}
	return true
}

// Editable diz se a configuração, como a máquina a informou, pode ser alterada pelo patchd:
// está na lista fechada, é de escolha entre valores e não é só leitura na BIOS.
func Editable(s protocol.BIOSSetting) (Rule, bool) {
	if s.Kind != protocol.BIOSKindEnum || s.ReadOnly || len(s.Possible) == 0 {
		return Rule{}, false
	}
	return Lookup(s.Name)
}

// Choices: os valores que o painel oferece (os possíveis da máquina menos os recusados pela
// regra), na ordem da BIOS.
func (r Rule) Choices(s protocol.BIOSSetting) []string {
	var out []string
	for _, v := range s.Possible {
		if r.ValueAllowed(v) {
			out = append(out, v)
		}
	}
	return out
}

// Change é uma alteração pedida: o nome exato da configuração e o valor exato.
type Change struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// MaxChanges: alterações por job. Cada uma é uma chamada ao firmware; um lote pequeno
// limita o estrago de um pedido errado.
const MaxChanges = 20

// CheckChange confere a alteração contra a configuração como a máquina a informou. É a
// mesma conferência no servidor (com o inventário) e no agente (com a leitura da hora).
func CheckChange(s protocol.BIOSSetting, c Change) error {
	r, ok := Editable(s)
	switch {
	case !ok:
		return errNotAllowed(s.Name)
	case !r.ValueAllowed(c.Value):
		return errValue(s.Name, c.Value)
	}
	for _, v := range s.Possible {
		if v == c.Value {
			return nil
		}
	}
	return errValue(s.Name, c.Value)
}

func errNotAllowed(name string) error {
	return fmt.Errorf("a configuração %q não está na lista fechada do patchd (ou é só leitura, ou não é de escolha)", name)
}

func errValue(name, value string) error {
	return fmt.Errorf("o valor %q não é aceito para %q (fora dos valores possíveis da máquina ou recusado pela lista fechada)", value, name)
}
