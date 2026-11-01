//go:build !windows

package shortcuts

import "errors"

func UserManager()(*Manager,error){return nil,errors.New("atalhos de padronização disponíveis no Windows")}
