package main

import "syscall"

// detachedAttr: sessão própria (setsid). Ao descarregar o serviço, o launchd encerra o
// grupo de processos dele; numa sessão nova, o instalador fica fora desse grupo.
func detachedAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
