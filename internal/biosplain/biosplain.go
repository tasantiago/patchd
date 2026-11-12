// Package biosplain traduz as configurações da BIOS para linguagem simples, igual para
// qualquer fabricante (docs/bios-config.md, seção 10): o "Wake On LAN" da HP, o "WakeOnLan" da
// Dell e o "WakeOnLAN" da Lenovo viram todos "Ligar pela rede", e "Boot to Hard Drive",
// "LanOnly" e "ACOnly" viram frases que qualquer pessoa entende.
//
// Só traduz o que mostra: o nome e o valor exatos continuam valendo para gravar (o job
// bios_settings leva o valor da BIOS, nunca a tradução), e a tela mostra o original em letra
// pequena. O que não está no dicionário aparece com o nome do fabricante.
package biosplain

import (
	"strings"
)

// Os assuntos, na ordem em que a tela os mostra.
const (
	TopicPower    = "Ligar e desligar"
	TopicBoot     = "Inicialização"
	TopicSecurity = "Segurança"
	TopicDevices  = "Portas e dispositivos"
)

// Topics: a ordem dos assuntos.
var Topics = []string{TopicPower, TopicBoot, TopicSecurity, TopicDevices}

// Chaves das entradas que outras partes do patchd procuram (o resumo da aba BIOS).
const (
	KeyWake       = "ligar-pela-rede"
	KeyDeepSleep  = "economia-desligado"
	KeyPowerLoss  = "queda-de-energia"
	KeyUSBBoot    = "iniciar-por-usb"
	KeyVirt       = "virtualizacao"
	KeySecureBoot = "inicializacao-segura"
)

// Entry é uma configuração traduzida.
type Entry struct {
	Key   string
	Title string // o que a configuração faz, em poucas palavras
	Help  string // uma frase: para que serve, e o que muda na prática
	Topic string
	// values: traduções próprias desta configuração (valor normalizado → texto), antes das
	// gerais.
	values map[string]string
	// exact: nomes normalizados inteiros; contains: pedaços; not: pedaços que tiram a
	// configuração desta entrada.
	exact, contains, not []string
}

// Norm: minúsculas, sem espaços e pontuação ("Wake On LAN" → "wakeonlan"). O mesmo do
// internal/biosconfig.
func Norm(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '-', '_', '/', '\\', '\t', '(', ')', '.', ',', ':':
			return -1
		}
		return r
	}, strings.ToLower(s))
}

var onOff = map[string]string{"on": "Ligar", "poweron": "Ligar", "off": "Ficar desligado", "poweroff": "Ficar desligado",
	"previousstate": "Voltar como estava", "laststate": "Voltar como estava", "lastpowerstate": "Voltar como estava", "last": "Voltar como estava",
	"enable": "Ligar", "enabled": "Ligar", "disable": "Ficar desligado", "disabled": "Ficar desligado"}

// catalog, na ordem de busca: a primeira entrada que casa decide (as mais específicas antes).
var catalog = []Entry{
	// --- Ligar e desligar -------------------------------------------------------------
	{Key: "ligar-pela-rede-dock", Topic: TopicPower, Title: "Ligar pela rede quando estiver na dock",
		Help: "O mesmo que ligar pela rede, usando o cabo de rede da dock (base) do notebook.", contains: []string{"wakeonlandock"}},
	{Key: "ligar-pela-rede-senha", Topic: TopicPower, Title: "Pedir senha ao ligar pela rede",
		Help: "Se o computador pede a senha de ligar quando é acordado pela rede.", contains: []string{"wakeonlanpoweronpassword", "wakeonlanpassword"}},
	{Key: "ligar-pela-rede-bateria", Topic: TopicPower, Title: "Ligar pela rede na bateria",
		Help: "Permite ligar pela rede o notebook fora da tomada (gasta bateria).", contains: []string{"wakeonlanondc", "wakeonlanondcmode", "wakeonlanbattery"}},
	{Key: "ligar-pelo-wifi", Topic: TopicPower, Title: "Ligar pelo Wi-Fi",
		Help: "Permite que a TI ligue o computador à distância pela rede sem fio.", contains: []string{"wakeonwlan", "wakeonwirelesslan"}},
	{Key: KeyWake, Topic: TopicPower, Title: "Ligar pela rede",
		Help:     "Permite que a TI ligue o computador à distância (Wake on LAN), por exemplo para atualizar fora do horário.",
		contains: []string{"wakeonlan"},
		values: map[string]string{"enable": "Ligado", "enabled": "Ligado", "primary": "Ligado", "automatic": "Ligado (automático)",
			"aconly": "Ligado, só na tomada", "acandbattery": "Ligado, na tomada e na bateria"}},
	{Key: KeyDeepSleep, Topic: TopicPower, Title: "Economia máxima com o computador desligado",
		Help:     "Corta a energia da placa de rede quando o computador desliga: economiza, mas impede de ligar pela rede.",
		contains: []string{"deepsleep", "maximumpowersaving", "maxpowersaving"},
		values: map[string]string{"s5": "Ligado, com o computador desligado", "s5only": "Ligado, com o computador desligado",
			"s4ands5": "Ligado, desligado ou hibernando", "s4s5": "Ligado, desligado ou hibernando"}},
	{Key: KeyPowerLoss, Topic: TopicPower, Title: "Quando a energia volta depois de uma queda",
		Help:     "O que o computador faz sozinho quando a energia elétrica volta.",
		contains: []string{"afterpowerloss", "acpwrrcvry", "acpowerrecovery", "restoreonacpowerloss", "acrecovery", "afterpowerfailure"},
		values:   onOff},
	{Key: "ligar-em-horario", Topic: TopicPower, Title: "Ligar sozinho em horário marcado",
		Help: "Liga o computador todo dia (ou em certos dias) num horário definido na BIOS.", exact: []string{"autoon"},
		contains: []string{"autopoweron", "scheduledpoweron", "poweronschedule"}},
	{Key: "ligar-na-tomada", Topic: TopicPower, Title: "Ligar ao conectar na tomada",
		Help: "Liga o computador assim que o carregador ou o cabo de energia é conectado.", contains: []string{"poweronwhenacdetected", "poweronac"}},
	{Key: "ligar-ao-abrir", Topic: TopicPower, Title: "Ligar ao abrir a tampa",
		Help: "Liga o notebook quando a tampa é aberta.", contains: []string{"poweronwhenlidisopened", "lidopen"}},
	{Key: "acordar-usb", Topic: TopicPower, Title: "Acordar pelo teclado ou mouse USB",
		Help: "Tira o computador do modo de espera quando alguém mexe no teclado ou no mouse.", contains: []string{"usbwake", "wakeonusb"}},
	{Key: "bloquear-espera", Topic: TopicPower, Title: "Bloquear o modo de espera",
		Help: "Impede o computador de entrar em espera (suspensão).", contains: []string{"blocksleep"}},

	// --- Inicialização --------------------------------------------------------------------
	{Key: KeyUSBBoot, Topic: TopicBoot, Title: "Iniciar por pendrive ou HD externo",
		Help:  "Permite ligar o computador por um pendrive. Desligado, ninguém inicia outro sistema por USB.",
		exact: []string{"usbemu"}, contains: []string{"usbstorageboot", "usbboot", "bootfromusb"}, not: []string{"nousbboot", "order"}},
	{Key: "teclado-na-bios", Topic: TopicBoot, Title: "Teclado USB na tela da BIOS",
		Help: "Faz o teclado USB funcionar nas telas antes do Windows.", contains: []string{"usbbiossupport"}},
	{Key: "iniciar-pela-rede", Topic: TopicBoot, Title: "Iniciar pela rede",
		Help:     "Permite iniciar o computador pela rede (PXE), usado para instalar o Windows à distância.",
		contains: []string{"networkpxeboot", "pxeboot", "networkboot"}},
	{Key: "suporte-rede-inicio", Topic: TopicBoot, Title: "Rede disponível ao iniciar",
		Help: "Liga a placa de rede antes do Windows; necessária para iniciar pela rede.", contains: []string{"networkstack", "uefinwstack"}},
	{Key: "inicio-rapido", Topic: TopicBoot, Title: "Inicialização rápida",
		Help: "Pula algumas verificações ao ligar, para o computador iniciar mais rápido.", contains: []string{"fastboot", "quickboot"},
		values: map[string]string{"minimal": "Rápida (verifica o mínimo)", "thorough": "Completa (verifica tudo, mais lenta)", "auto": "Automática"}},
	{Key: "espera-inicio", Topic: TopicBoot, Title: "Espera na tela inicial",
		Help: "Quantos segundos a tela do fabricante fica parada ao ligar.", contains: []string{"startupdelay", "postdelay"}},
	{Key: "numlock", Topic: TopicBoot, Title: "Teclado numérico ligado ao iniciar",
		Help: "Liga o Num Lock (teclado numérico) quando o computador inicia.", contains: []string{"numlock"}},
	{Key: "sons-inicio", Topic: TopicBoot, Title: "Sons de aviso ao ligar",
		Help: "Bipes de aviso durante a inicialização.", contains: []string{"audioalertsduringboot"}},
	{Key: "aviso-memoria", Topic: TopicBoot, Title: "Avisar quando a memória mudar",
		Help: "Mostra um aviso ao ligar se a quantidade de memória mudou.", contains: []string{"promptonmemorysizechange"}},
	{Key: "ordem-inicio", Topic: TopicBoot, Title: "Ordem de inicialização",
		Help: "Em que ordem o computador procura um sistema para iniciar.", contains: []string{"bootorder"}},
	{Key: "modo-inicio", Topic: TopicBoot, Title: "Modo de inicialização",
		Help: "Moderno (UEFI) ou antigo (legado). O Windows 11 exige o moderno.", contains: []string{"bootmode"}},

	// --- Segurança ------------------------------------------------------------------------
	{Key: "legado-e-segura", Topic: TopicSecurity, Title: "Modo antigo e inicialização segura",
		Help:  "Combina o modo de inicialização com a inicialização segura (Secure Boot).",
		exact: []string{"configurelegacysupportandsecureboot"},
		values: map[string]string{"legacysupportdisableandsecurebootenable": "Moderno, com inicialização segura",
			"legacysupportdisableandsecurebootdisable": "Moderno, sem inicialização segura",
			"legacysupportenableandsecurebootdisable":  "Antigo (legado), sem inicialização segura"}},
	{Key: "modo-segura", Topic: TopicSecurity, Title: "Modo da inicialização segura",
		Help: "No modo de auditoria a inicialização segura só registra, não bloqueia.", contains: []string{"securebootmode"}},
	{Key: KeySecureBoot, Topic: TopicSecurity, Title: "Inicialização segura (Secure Boot)",
		Help: "Impede o computador de iniciar um sistema não reconhecido.", contains: []string{"secureboot"},
		not: []string{"key", "import", "custom", "reset", "clear", "database"}},
	{Key: "virtualizacao-dispositivos", Topic: TopicSecurity, Title: "Virtualização de dispositivos",
		Help:     "Recurso do processador (VT-d) usado por máquinas virtuais e pela proteção de memória do Windows.",
		contains: []string{"directedio", "vtd", "vtfordirectio"}},
	{Key: KeyVirt, Topic: TopicSecurity, Title: "Virtualização",
		Help:  "Recurso do processador usado por máquinas virtuais e por proteções do Windows (VBS, Credential Guard).",
		exact: []string{"virtualization"}, contains: []string{"virtualizationtechnology", "vtx", "svmcpuvirtualization", "svmmode"},
		not: []string{"basedbiosprotection"}},
	{Key: "chip-seguranca", Topic: TopicSecurity, Title: "Chip de segurança (TPM)",
		Help: "Guarda as chaves do BitLocker e do Windows Hello. O Windows 11 exige.", contains: []string{"tpmdevice", "tpmstate", "securitychip", "tpmsecurity"}},
	{Key: "autenticacao-reforcada", Topic: TopicSecurity, Title: "Autenticação reforçada da BIOS (HP Sure Admin)",
		Help:     "Ligada, a BIOS só aceita mudanças assinadas, e o patchd não consegue alterá-la.",
		contains: []string{"enhancedbiosauthentication", "sureadmin"}},

	// --- Portas e dispositivos --------------------------------------------------------------
	{Key: "placa-rede", Topic: TopicDevices, Title: "Placa de rede com cabo",
		Help:     "Desligada, o computador fica sem rede cabeada (e sai do patchd).",
		contains: []string{"integratednic", "embeddednic", "onboardnic", "lancontroller", "integratedlan", "ethernetlan", "embnic", "networkinterfacecontroller"}},
	{Key: "internet-movel", Topic: TopicDevices, Title: "Internet móvel (chip 4G/5G)",
		Help: "O modem de celular de alguns notebooks.", contains: []string{"wwan", "wirelesswan"}},
	{Key: "wifi", Topic: TopicDevices, Title: "Wi-Fi",
		Help: "A rede sem fio. Desligado, o notebook só tem rede pelo cabo.", contains: []string{"wirelessnetworkdevice", "wlan", "wirelesslan"}},
	{Key: "bluetooth", Topic: TopicDevices, Title: "Bluetooth",
		Help: "Fones, teclados e mouses sem fio.", contains: []string{"bluetooth"}},
	{Key: "portas-usb", Topic: TopicDevices, Title: "Portas USB",
		Help: "Desligadas, pendrives, teclados e mouses USB deixam de funcionar nessas portas.", contains: []string{"usbport"}},
	{Key: "camera", Topic: TopicDevices, Title: "Câmera",
		Help: "A câmera embutida.", contains: []string{"camera"}},
	{Key: "microfone", Topic: TopicDevices, Title: "Microfone",
		Help: "O microfone embutido.", contains: []string{"microphone"}},
	{Key: "alto-falante", Topic: TopicDevices, Title: "Alto-falante",
		Help: "O alto-falante embutido.", contains: []string{"internalspeaker"}},
	{Key: "leitor-digital", Topic: TopicDevices, Title: "Leitor de impressão digital",
		Help: "O sensor de digital para entrar no Windows.", contains: []string{"fingerprint"}},
	{Key: "leitor-smartcard", Topic: TopicDevices, Title: "Leitor de cartão inteligente (smart card)",
		Help: "Leitor de certificado digital em cartão.", contains: []string{"smartcard"}},
	{Key: "leitor-cartao", Topic: TopicDevices, Title: "Leitor de cartão de memória",
		Help: "A entrada para cartão SD.", contains: []string{"mediacardreader", "memorycardslot", "mediacard", "sdcard", "cardreader"}},
}

// Describe devolve a tradução da configuração, se houver.
func Describe(name string) (Entry, bool) {
	n := Norm(name)
	if n == "" {
		return Entry{}, false
	}
	for _, e := range catalog {
		if e.match(n) {
			return e, true
		}
	}
	return Entry{}, false
}

func (e Entry) match(n string) bool {
	for _, x := range e.not {
		if strings.Contains(n, x) {
			return false
		}
	}
	for _, x := range e.exact {
		if n == x {
			return true
		}
	}
	for _, c := range e.contains {
		if strings.Contains(n, c) {
			return true
		}
	}
	return false
}

// generic: as traduções que valem para qualquer configuração.
var generic = map[string]string{
	"enable": "Ligado", "enabled": "Ligado", "on": "Ligado", "yes": "Ligado", "true": "Ligado",
	"disable": "Desligado", "disabled": "Desligado", "off": "Desligado", "no": "Desligado", "false": "Desligado",
	"auto": "Automático", "automatic": "Automático",
	"aconly": "Só na tomada", "acandbattery": "Na tomada e na bateria",
	"lanonly": "Ligado, só pelo cabo", "wlanonly": "Ligado, só pelo Wi-Fi", "lanwlan": "Ligado, pelo cabo e pelo Wi-Fi",
	"lanwithpxeboot":        "Ligado, iniciando pela rede",
	"boottonetwork":         "Ligado, iniciando pela rede",
	"boottoharddrive":       "Ligado, iniciando o Windows",
	"boottonormalbootorder": "Ligado, na ordem normal de inicialização",
	"followbootorder":       "Ligado, na ordem normal de inicialização",
	"hidden":                "Escondido",
	"audit":                 "Só registra (auditoria)", "auditmode": "Só registra (auditoria)",
	"deployed": "Bloqueando (normal)", "deployedmode": "Bloqueando (normal)",
	"uefi": "Moderno (UEFI)", "uefinative": "Moderno (UEFI)", "legacy": "Antigo (legado)",
}

// Value traduz o valor da configuração. Sem tradução, volta o valor como a BIOS escreve.
func Value(name, value string) string {
	v := Norm(value)
	if e, ok := Describe(name); ok {
		if t, ok := e.values[v]; ok {
			return t
		}
	}
	if t, ok := generic[v]; ok {
		return t
	}
	return value
}

// Title: o nome simples da configuração, ou o do fabricante se não houver tradução.
func Title(name string) string {
	if e, ok := Describe(name); ok {
		return e.Title
	}
	return name
}

// Change: uma alteração em linguagem simples ("Ligar pela rede: Ligado, iniciando o Windows").
func Change(name, value string) string {
	return Title(name) + ": " + Value(name, value)
}
