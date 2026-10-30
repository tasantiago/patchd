package bioswrite

import "strconv"

// A interface da Dell (docs/bios-config.md, seção 8), nos modelos com o provedor WMI de fábrica:
//   - senha: root\dcim\sysman\wmisecurity, SecurityInterface.SetNewPassword(SecType,
//     SecHndCount, SecHandle, NameId "Admin", OldPassword, NewPassword);
//   - configuração: root\dcim\sysman\biosattributes, BIOSAttributeInterface.SetAttribute(SecType,
//     SecHndCount, SecHandle, AttributeName, AttributeValue).
//
// Com senha em uso, SecType 1 e o "handle" é a própria senha em bytes; sem senha, SecType 0 e
// handle vazio. As duas devolvem Status.

// dellCodes: os Status da Dell.
var dellCodes = map[uint32]struct {
	text      string
	uncertain bool
}{
	1: {"falhou (sem detalhe do firmware)", true},
	2: {"parâmetro inválido", false},
	3: {"acesso negado: a senha não confere. Não repita: a BIOS pode travar novas tentativas", false},
	4: {"não suportado neste modelo", false},
	5: {"erro de memória no firmware", true},
	6: {"erro de protocolo", true},
}

func dellReply(code uint32) Reply {
	r := Reply{Code: strconv.FormatUint(uint64(code), 10), OK: code == 0}
	if r.OK {
		return r
	}
	c, known := dellCodes[code]
	if !known {
		c.text, c.uncertain = "código desconhecido", true
	}
	r.Text, r.Uncertain, r.WrongPassword = c.text, c.uncertain, code == 3
	return r
}

// dellSecurity: SecType, SecHndCount e SecHandle para a senha em uso. O handle vazio vai com
// um byte (o WMI não aceita lista vazia) e a contagem zero.
func dellSecurity(password []byte) (uint32, uint32, []byte) {
	if len(password) == 0 {
		return 0, 0, []byte{0}
	}
	return 1, uint32(len(password)), append([]byte(nil), password...) // #nosec G115 -- senha curta, conferida antes
}
