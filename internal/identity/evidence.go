package identity

import (
	"regexp"
	"strings"

	"github.com/tasantiago/patchd/internal/protocol"
)

// Keys são as evidências normalizadas que o servidor compara entre máquinas. Valor vazio
// = evidência ausente ou descartada (genérica, ilegível): nunca entra em comparação.
type Keys struct {
	InstallID   string // identidade da instalação do SO
	HardwareKey string // UUID do hardware em forma comparável (ver UUIDKey)
	Serial      string // número de série, em maiúsculas
}

// Strong diz se há ao menos uma evidência forte. Serial sozinho não basta: há fabricantes
// que repetem o mesmo serial "válido" em lotes inteiros.
func (k Keys) Strong() bool { return k.InstallID != "" || k.HardwareKey != "" }

// Relações entre uma máquina que acabou de se registrar e uma já conhecida.
const (
	// Mesma instalação e o mesmo hardware: o agente foi reinstalado ou a credencial se perdeu.
	RelationReenrollment = "reenrollment"
	// Mesma instalação em OUTRO hardware: imagem clonada sem gerar nova identidade
	// (Windows sem sysprep, Linux com o mesmo /etc/machine-id).
	RelationClone = "clone"
	// Mesmo hardware, instalação diferente: SO reinstalado (formatação) ou dual boot.
	RelationSameHardware = "same_hardware"
)

// Relation compara duas máquinas pelas evidências normalizadas. Devolve false quando
// não há relação. Nenhuma relação funde máquinas: ela só gera um alerta para o
// administrador decidir (RF-24), porque evidências são declarações que o servidor não
// consegue verificar.
func Relation(nova, antiga Keys) (string, bool) {
	sameInstall := nova.InstallID != "" && nova.InstallID == antiga.InstallID
	uuidDiffers := nova.HardwareKey != "" && antiga.HardwareKey != "" && nova.HardwareKey != antiga.HardwareKey
	sameHardware := !uuidDiffers &&
		((nova.HardwareKey != "" && nova.HardwareKey == antiga.HardwareKey) ||
			(nova.Serial != "" && nova.Serial == antiga.Serial))

	switch {
	case sameInstall && uuidDiffers:
		return RelationClone, true
	case sameInstall:
		return RelationReenrollment, true
	case sameHardware:
		return RelationSameHardware, true
	default:
		return "", false
	}
}

// Normalize converte as evidências declaradas em chaves comparáveis e diz o que foi
// descartado, e por quê (para o log: o descarte é decisão do servidor, e o valor cru
// continua guardado para ser reavaliado se a regra mudar).
func Normalize(e protocol.IdentityEvidence) (Keys, []string) {
	var k Keys
	var discarded []string

	if v, ok := normalizeInstallID(e.InstallID); ok {
		k.InstallID = v
	} else if strings.TrimSpace(e.InstallID) != "" {
		discarded = append(discarded, "install_id genérico ou inválido: "+e.InstallID)
	}
	if v, ok := NormalizeUUID(e.HardwareUUID); ok {
		k.HardwareKey = UUIDKey(v)
	} else if strings.TrimSpace(e.HardwareUUID) != "" {
		discarded = append(discarded, "hardware_uuid genérico ou inválido: "+e.HardwareUUID)
	}
	if v, ok := normalizeSerial(e.Serial); ok {
		k.Serial = v
	} else if strings.TrimSpace(e.Serial) != "" {
		discarded = append(discarded, "serial genérico: "+e.Serial)
	}
	return k, discarded
}

var hexOnly = regexp.MustCompile(`^[0-9a-f]+$`)

// normalizeInstallID: MachineGuid ("{...}" ou não) e /etc/machine-id (32 hex). Descarta o
// "uninitialized" que o systemd grava em imagens preparadas para clonagem, e só zeros.
func normalizeInstallID(s string) (string, bool) {
	s = strings.ToLower(strings.Trim(strings.TrimSpace(s), "{}"))
	compact := strings.ReplaceAll(s, "-", "")
	if s == "" || s == "uninitialized" || strings.Trim(compact, "0") == "" {
		return "", false
	}
	return s, true
}

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// genericUUIDs: valores de placa-mãe sem UUID gravado. Só zeros e só F são os comuns; o
// terceiro é o padrão de BIOS AMI que aparece em placas genéricas (nas duas ordens de bytes).
var genericUUIDs = map[string]bool{
	"00000000-0000-0000-0000-000000000000": true,
	"ffffffff-ffff-ffff-ffff-ffffffffffff": true,
	"03000200-0400-0500-0006-000700080009": true,
	"00020003-0004-0005-0006-000700080009": true,
}

// NormalizeUUID devolve o UUID em minúsculas, sem chaves, se tiver o formato e não for genérico.
func NormalizeUUID(s string) (string, bool) {
	s = strings.ToLower(strings.Trim(strings.TrimSpace(s), "{}"))
	if !uuidPattern.MatchString(s) || genericUUIDs[s] {
		return "", false
	}
	return s, true
}

// swapUUID inverte a ordem dos bytes dos três primeiros campos. O SMBIOS grava esses
// campos em little-endian a partir da versão 2.6; leitores antigos, ou firmwares que
// não seguem a regra, mostram os mesmos bytes na outra ordem.
func swapUUID(u string) string {
	rev := func(h string) string {
		var b strings.Builder
		for i := len(h) - 2; i >= 0; i -= 2 {
			b.WriteString(h[i : i+2])
		}
		return b.String()
	}
	return rev(u[0:8]) + "-" + rev(u[9:13]) + "-" + rev(u[14:18]) + u[18:]
}

// UUIDKey é a forma comparável do UUID: a menor (em ordem de texto) entre ele e a versão
// com os três primeiros campos invertidos. Assim o mesmo hardware lido no Windows e no
// Linux casa mesmo se um deles usar a outra ordem de bytes. Dois equipamentos distintos
// cujos UUIDs diferem só por essa inversão são, na prática, impossíveis.
func UUIDKey(normalized string) string {
	if s := swapUUID(normalized); s < normalized {
		return s
	}
	return normalized
}

// genericSerials: textos que fabricantes e hipervisores deixam no lugar do número de série.
var genericSerials = map[string]bool{
	"TO BE FILLED BY O.E.M.": true, "DEFAULT STRING": true, "SYSTEM SERIAL NUMBER": true,
	"CHASSIS SERIAL NUMBER": true, "NOT SPECIFIED": true, "NOT APPLICABLE": true,
	"NONE": true, "N/A": true, "NA": true, "INVALID": true, "UNKNOWN": true,
	"OEM": true, "O.E.M.": true, "SERIAL": true, "SERIALNUMBER": true,
	"123456789": true, "0123456789": true, "1234567890": true,
}

// normalizeSerial: maiúsculas e espaços simples; descarta a lista genérica, valores com
// menos de 4 caracteres e valores de um caractere só repetido ("00000000", "XXXX").
func normalizeSerial(s string) (string, bool) {
	s = strings.ToUpper(strings.Join(strings.Fields(s), " "))
	if len(s) < 4 || genericSerials[s] || strings.Trim(s, s[:1]) == "" {
		return "", false
	}
	return s, true
}

// NormalizeMAC devolve o MAC em minúsculas com dois-pontos, se for um endereço de fábrica.
// Descarta multicast, só zeros e endereços administrados localmente (bit 0x02 do primeiro
// byte), que são os aleatórios do Wi-Fi e os de interfaces virtuais (Docker, VPN, Hyper-V).
func NormalizeMAC(s string) (string, bool) {
	s = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), "-", ":"))
	parts := strings.Split(s, ":")
	if len(parts) != 6 {
		return "", false
	}
	for _, p := range parts {
		if len(p) != 2 || !hexOnly.MatchString(p) {
			return "", false
		}
	}
	var first byte
	for i := 0; i < 2; i++ {
		c := parts[0][i]
		var v byte
		if c >= 'a' {
			v = c - 'a' + 10
		} else {
			v = c - '0'
		}
		first = first<<4 | v
	}
	if first&0x01 != 0 || first&0x02 != 0 || s == "00:00:00:00:00:00" {
		return "", false
	}
	return s, true
}
