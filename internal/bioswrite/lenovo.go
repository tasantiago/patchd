package bioswrite

import (
	"errors"
	"strings"
)

// A interface da Lenovo (docs/bios-config.md, seção 8), em root\WMI. Cada método recebe uma
// linha de texto e devolve texto:
//   - senha: Lenovo_SetBiosPassword.SetBiosPassword("pap,atual,nova,ascii,us;");
//   - configuração: Lenovo_SetBiosSetting.SetBiosSetting("Nome,Valor,senha,ascii,us;") e, no
//     fim, Lenovo_SaveBiosSettings.SaveBiosSettings("senha,ascii,us;"): sem o salvar, nada vale.
//
// Sem senha de supervisor, a senha sai da linha ("Nome,Valor;") e o salvar vai sem parâmetro.
// Vírgula e ponto e vírgula separam os campos: não podem aparecer em nome, valor ou senha.

// lenovoEncoding: a senha em ASCII, teclado americano (o alfabeto da guarda só tem letras
// minúsculas e números).
const lenovoEncoding = "ascii,us"

var errLenovoSeparator = errors.New("vírgula ou ponto e vírgula no nome, no valor ou na senha: a interface da Lenovo usa os dois como separador")

func lenovoSafe(parts ...string) bool {
	for _, p := range parts {
		if strings.ContainsAny(p, ",;") {
			return false
		}
	}
	return true
}

// lenovoPasswordLine: o parâmetro do SetBiosPassword (supervisor, "pap").
func lenovoPasswordLine(newPW, current []byte) (string, error) {
	if !lenovoSafe(string(newPW), string(current)) {
		return "", errLenovoSeparator
	}
	return "pap," + string(current) + "," + string(newPW) + "," + lenovoEncoding + ";", nil
}

// lenovoSettingLine: o parâmetro do SetBiosSetting.
func lenovoSettingLine(name, value string, password []byte) (string, error) {
	if !lenovoSafe(name, value, string(password)) {
		return "", errLenovoSeparator
	}
	if len(password) == 0 {
		return name + "," + value + ";", nil
	}
	return name + "," + value + "," + string(password) + "," + lenovoEncoding + ";", nil
}

// lenovoSaveLine: o parâmetro do SaveBiosSettings ("" = sem parâmetro).
func lenovoSaveLine(password []byte) (string, error) {
	if len(password) == 0 {
		return "", nil
	}
	if !lenovoSafe(string(password)) {
		return "", errLenovoSeparator
	}
	return string(password) + "," + lenovoEncoding + ";", nil
}

// lenovoReply traduz o retorno da Lenovo.
func lenovoReply(ret string) Reply {
	ret = strings.TrimSpace(ret)
	r := Reply{Code: ret}
	switch strings.ToLower(ret) {
	case "success":
		r.OK = true
	case "not supported":
		r.Text = "não suportado neste modelo"
	case "invalid parameter":
		r.Text = "parâmetro inválido"
	case "access denied":
		r.Text, r.WrongPassword = "acesso negado: a senha não confere (ou, sem senha definida, este modelo não aceita a primeira senha de supervisor pelo WMI). Não repita", true
	case "system busy":
		r.Text = "a BIOS estava ocupada (há alterações pendentes de reinício?); nada foi gravado"
	default:
		r.Text, r.Uncertain = "retorno desconhecido", true
	}
	return r
}
