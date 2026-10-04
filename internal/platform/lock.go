package platform

import "errors"

// ErrLocked: outro processo detém a trava.
var ErrLocked = errors.New("trava em uso por outro processo")

// TryLock tenta obter, sem esperar, a trava exclusiva do arquivo path (criado se não
// existir) e devolve a função que a solta. A trava é do sistema operacional, e não a
// simples existência do arquivo: ela some sozinha quando o processo termina, mesmo numa
// queda, e nunca fica uma trava "esquecida" para alguém apagar à mão.
func TryLock(path string) (unlock func(), err error) { return tryLock(path) }
