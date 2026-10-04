package main

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// detachedAttr: sem console e num grupo de processos próprio. O SCM, ao parar o serviço,
// encerra só o processo do serviço; o instalador continua.
func detachedAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP, HideWindow: true}
}
