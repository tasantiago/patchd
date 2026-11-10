// Package biosstd é o padrão da BIOS (docs/bios-config.md, seção 9): objetivos que valem para
// qualquer marca e modelo ("Wake on LAN ligado", "boot por USB desligado"), resolvidos para
// cada máquina a partir das configurações que a própria BIOS informou no inventário.
//
// Nada aqui adivinha nome de configuração: o patchd só propõe mudar o que a máquina mostrou
// que tem, com um valor que ela mostrou que aceita, e cada mudança passa de novo pela lista
// fechada (internal/biosconfig) no servidor e no agente. Um modelo que não expõe a
// configuração fica marcado como tal, sem tentativa às cegas.
package biosstd

import (
	"fmt"
	"strings"

	"github.com/tasantiago/patchd/internal/biosconfig"
	"github.com/tasantiago/patchd/internal/protocol"
)

// Os objetivos do padrão.
const (
	GoalWake    = "wol" // Wake on LAN ligado no firmware, sem a economia profunda que o impede
	GoalUSBBoot = "usb" // boot por dispositivo de armazenamento USB desligado
)

// Goals: os objetivos, na ordem da tela.
var Goals = []string{GoalWake, GoalUSBBoot}

// GoalLabel: o nome de cada objetivo.
var GoalLabel = map[string]string{
	GoalWake:    "Wake on LAN ligado",
	GoalUSBBoot: "Boot por USB desligado",
}

// O resultado de um objetivo numa máquina.
const (
	StateOK      = "conforme" // já está como o padrão pede
	StateFix     = "corrigir" // há o que mudar, e o patchd pode
	StateMissing = "sem"      // este modelo não informa uma configuração deste objetivo
	StateStuck   = "travada"  // a configuração existe, mas não pode ser alterada pelo patchd
)

// NamePattern: os nomes normalizados (biosconfig.Norm) das configurações que interessam ao
// padrão, como expressão regular. O servidor a usa para trazer do banco só essas.
const NamePattern = `wakeonlan|deepsleep|maximumpowersaving|maxpowersaving|usbstorageboot|usbboot|bootfromusb|^usbemu$`

// NormStrip: os caracteres que biosconfig.Norm tira do nome (para a mesma normalização em SQL).
const NormStrip = " -_/\\\t().,:"

// Finding: o que o padrão diz de um objetivo numa máquina.
type Finding struct {
	Goal    string
	State   string
	Changes []biosconfig.Change // o que mudar (StateFix)
	// Evidence: as configurações que pesaram, "nome: valor", como a máquina informou.
	Evidence []string
	Note     string // o motivo, em StateMissing e StateStuck
}

var (
	offValues = map[string]bool{"disable": true, "disabled": true, "off": true, "no": true}
	// wakeOn: os valores que ligam o WoL, do preferido ao último recurso. Os que mantêm o boot
	// normal vêm antes; "Boot to Network" (a máquina acorda e tenta o PXE) só entra se não
	// houver outro.
	// No notebook Lenovo, "ACOnly" vem antes de "Enable": acordar só na tomada não gasta bateria.
	wakeOn = []string{"boottoharddrive", "boottonormalbootorder", "followbootorder", "lanonly", "aconly", "enable", "enabled", "on",
		"primary", "automatic", "acandbattery", "lanwlan", "lanwithpxeboot", "boottonetwork"}
	deepOn = map[string]bool{"enable": true, "enabled": true, "s5only": true, "s5": true, "s4ands5": true, "s4s5": true}
)

func isWakeOn(v string) bool {
	for _, w := range wakeOn {
		if v == w {
			return true
		}
	}
	return false
}

// wakeName: a configuração é o Wake on LAN da placa cabeada (não o do Wi-Fi nem o da dock, e
// não a política de senha do WoL).
func wakeName(n string) bool {
	return strings.Contains(n, "wakeonlan") && !strings.Contains(n, "wlan") && !strings.Contains(n, "dock") &&
		!strings.Contains(n, "password") && !strings.Contains(n, "policy")
}

func deepName(n string) bool {
	return strings.Contains(n, "deepsleep") || strings.Contains(n, "maximumpowersaving") || strings.Contains(n, "maxpowersaving")
}

// usbBootName: a configuração liga ou desliga o boot por armazenamento USB. A UsbEmu é a
// "Enable USB Boot Support" da Dell. Ficam de fora a UsbEmuNoUsbBoot dos modelos antigos (o
// sentido é o contrário) e a ordem de boot (é lista, não escolha).
func usbBootName(n string) bool {
	if strings.Contains(n, "nousbboot") || strings.Contains(n, "order") {
		return false
	}
	return n == "usbemu" || strings.Contains(n, "usbstorageboot") || strings.Contains(n, "usbboot") || strings.Contains(n, "bootfromusb")
}

// Evaluate resolve um objetivo com as configurações da máquina.
func Evaluate(goal string, settings []protocol.BIOSSetting) Finding {
	switch goal {
	case GoalWake:
		return evalWake(settings)
	case GoalUSBBoot:
		return evalUSBBoot(settings)
	}
	return Finding{Goal: goal, State: StateMissing, Note: "objetivo desconhecido"}
}

// pick devolve o primeiro valor possível da máquina que está em want (na ordem de want) e que
// a lista fechada aceita para esta configuração.
func pick(s protocol.BIOSSetting, want []string) (string, bool) {
	for _, w := range want {
		for _, v := range s.Possible {
			if biosconfig.Norm(v) == w && biosconfig.CheckChange(s, biosconfig.Change{Name: s.Name, Value: v}) == nil {
				return v, true
			}
		}
	}
	return "", false
}

var offOrder = []string{"disable", "disabled", "off", "no"}

func stuckNote(s protocol.BIOSSetting) string {
	switch {
	case s.ReadOnly:
		return fmt.Sprintf("%q é só leitura nesta BIOS", s.Name)
	case s.Kind != protocol.BIOSKindEnum || len(s.Possible) == 0:
		return fmt.Sprintf("%q não informa os valores possíveis", s.Name)
	}
	return fmt.Sprintf("%q não tem, entre os valores possíveis, um que o patchd reconheça e possa gravar", s.Name)
}

func evalWake(settings []protocol.BIOSSetting) Finding {
	f := Finding{Goal: GoalWake}
	found, stuck := false, ""
	for _, s := range settings {
		n, v := biosconfig.Norm(s.Name), biosconfig.Norm(s.Value)
		switch {
		case wakeName(n) && (isWakeOn(v) || offValues[v] || v == "wlanonly"):
			found = true
			f.Evidence = append(f.Evidence, s.Name+": "+s.Value)
			if isWakeOn(v) {
				continue
			}
			want := wakeOn
			if v == "wlanonly" { // Dell: só o Wi-Fi acorda; o padrão liga o cabo sem tirar o Wi-Fi
				want = append([]string{"lanwlan"}, wakeOn...)
			}
			if to, ok := pick(s, want); ok {
				f.Changes = append(f.Changes, biosconfig.Change{Name: s.Name, Value: to})
			} else if stuck == "" {
				stuck = stuckNote(s)
			}
		case deepName(n) && deepOn[v]:
			// A economia profunda do desligado tira a energia da placa: o WoL não acorda.
			f.Evidence = append(f.Evidence, s.Name+": "+s.Value)
			if to, ok := pick(s, offOrder); ok {
				f.Changes = append(f.Changes, biosconfig.Change{Name: s.Name, Value: to})
			} else if stuck == "" {
				stuck = stuckNote(s)
			}
		}
	}
	return f.close(found, stuck, "este modelo não informa uma configuração de Wake on LAN")
}

func evalUSBBoot(settings []protocol.BIOSSetting) Finding {
	f := Finding{Goal: GoalUSBBoot}
	found, stuck := false, ""
	for _, s := range settings {
		n, v := biosconfig.Norm(s.Name), biosconfig.Norm(s.Value)
		on := v == "enable" || v == "enabled" || v == "on" || v == "yes"
		if !usbBootName(n) || (!on && !offValues[v]) {
			continue
		}
		found = true
		f.Evidence = append(f.Evidence, s.Name+": "+s.Value)
		if !on {
			continue
		}
		if to, ok := pick(s, offOrder); ok {
			f.Changes = append(f.Changes, biosconfig.Change{Name: s.Name, Value: to})
		} else if stuck == "" {
			stuck = stuckNote(s)
		}
	}
	return f.close(found, stuck, "este modelo não informa uma configuração que ligue ou desligue o boot por USB")
}

// close decide o estado. Uma configuração travada vence: o objetivo não fecha só com as outras.
func (f Finding) close(found bool, stuck, missing string) Finding {
	switch {
	case stuck != "":
		f.State, f.Note, f.Changes = StateStuck, stuck, nil
	case len(f.Changes) > 0:
		f.State = StateFix
	case found:
		f.State = StateOK
	default:
		f.State, f.Note = StateMissing, missing
	}
	return f
}
