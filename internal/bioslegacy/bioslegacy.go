// Package bioslegacy trata o marcador que o .bat de senha da BIOS deixou no Registro das HP
// (docs/bios-senha.md, seção 11). O .bat gravava a senha em uso, legível, como a "versão" de
// um programa de mentira (a chave Uninstall\BIOS_password, valor DisplayVersion), para o
// inventário de software de outra ferramenta coletá-la. Duas consequências aqui:
//
//   - o inventário do patchd também lê as chaves Uninstall: sem cuidado, a senha viria para o
//     servidor como versão de programa. Redact e IsMarker tiram a versão dessa entrada, no
//     agente e de novo no servidor (para o agente antigo);
//   - na primeira troca pela guarda, a senha atual de cada máquina é a que está no marcador, e
//     cada máquina tem a sua. O agente a lê na hora do job (Read), só na memória, e a apaga do
//     Registro depois que a BIOS confirma a senha nova (Clear).
//
// O valor nunca vai para o log, o resultado do job ou o servidor.
package bioslegacy

import (
	"strings"

	"github.com/tasantiago/patchd/internal/protocol"
)

const (
	// KeyName: o nome da subchave e o DisplayName do marcador.
	KeyName = "BIOS_password"
	// Path: a chave do marcador, dentro do HKEY_LOCAL_MACHINE (visão de 64 bits).
	Path = `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\` + KeyName
	// Value: o valor com a senha em uso. PendingValue: o que uma versão anterior do .bat usava.
	Value        = "DisplayVersion"
	PendingValue = "PendingPassword"
	// MaxLen: a maior senha de administrador que a BIOS da HP aceita.
	MaxLen = 32
)

// IsMarker diz se a entrada Uninstall é o marcador (pelo nome exibido ou pelo nome da chave).
// O nome é exato: procurar por "password" esconderia a versão de programas de verdade.
func IsMarker(displayName, keyName string) bool {
	return strings.EqualFold(strings.TrimSpace(displayName), KeyName) || strings.EqualFold(strings.TrimSpace(keyName), KeyName)
}

// Redact tira a versão das entradas do marcador e devolve quantas mudou.
func Redact(list []protocol.Software) int {
	n := 0
	for i := range list {
		key := list[i].Source
		if j := strings.LastIndexByte(key, '\\'); j >= 0 {
			key = key[j+1:]
		}
		if IsMarker(list[i].Name, key) && list[i].Version != "" {
			list[i].Version = ""
			n++
		}
	}
	return n
}

// InPath diz se o caminho do Registro passa por uma chave do marcador: a do Uninstall ou a do
// histórico que o .bat guarda à parte (as duas têm senhas legíveis).
func InPath(path string) bool {
	for _, seg := range strings.Split(path, `\`) {
		if strings.EqualFold(strings.TrimSpace(seg), KeyName) {
			return true
		}
	}
	return false
}

// Mentions diz se o texto cita o marcador (a resposta de uma pergunta "programa" de um agente
// antigo pode trazer a senha ao lado do nome).
func Mentions(text string) bool {
	return strings.Contains(strings.ToLower(text), strings.ToLower(KeyName))
}

// Usable diz se o valor do marcador serve de senha atual: de 1 a 32 caracteres ASCII visíveis.
// Fora disso o agente não tenta: senha errada repetida trava a BIOS até reiniciar.
func Usable(pw []byte) bool {
	if len(pw) == 0 || len(pw) > MaxLen {
		return false
	}
	for _, c := range pw {
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}
