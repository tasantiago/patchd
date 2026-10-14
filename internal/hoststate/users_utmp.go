package hoststate

import (
	"bytes"
	"encoding/binary"
	"strings"

	"github.com/tasantiago/patchd/internal/protocol"
)

// O utmp do glibc no Linux de 64 bits (x86-64 e arm64): registros de 384 bytes.
// Deslocamentos de struct utmp em <bits/utmp.h>; ut_tv usa inteiros de 32 bits.
const (
	utmpSize    = 384
	utmpUser    = 7 // USER_PROCESS
	utmpOffPID  = 4
	utmpOffLine = 8
	utmpOffUser = 44
	utmpOffHost = 76
	utmpLenLine = 32
	utmpLenUser = 32
	utmpLenHost = 256
)

// utmpEntry é uma linha de USER_PROCESS do utmp.
type utmpEntry struct {
	PID  int32
	Line string // tty1, pts/0, :0, seat0
	User string
	Host string // de onde veio a sessão remota; ":0" numa sessão gráfica local
}

// parseUtmp lê os registros de sessão de usuário. Registros cortados no fim são ignorados.
func parseUtmp(b []byte) []utmpEntry {
	var out []utmpEntry
	for ; len(b) >= utmpSize; b = b[utmpSize:] {
		r := b[:utmpSize]
		if binary.LittleEndian.Uint16(r[0:2]) != utmpUser {
			continue
		}
		e := utmpEntry{
			PID:  int32(binary.LittleEndian.Uint32(r[utmpOffPID:])),
			Line: cstr(r[utmpOffLine : utmpOffLine+utmpLenLine]),
			User: cstr(r[utmpOffUser : utmpOffUser+utmpLenUser]),
			Host: cstr(r[utmpOffHost : utmpOffHost+utmpLenHost]),
		}
		if e.User != "" {
			out = append(out, e)
		}
	}
	return out
}

func cstr(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

// utmpSession: o tipo da sessão pela linha e pelo host.
//   - host remoto (não começa com ":"): ssh;
//   - tty1..ttyN, :0, seat0, ou host ":0": console;
//   - pts sem host: terminal (uma janela de terminal, ou um su/sudo -i).
func utmpSession(e utmpEntry) string {
	switch {
	case e.Host != "" && !strings.HasPrefix(e.Host, ":"):
		return protocol.SessionSSH
	case strings.HasPrefix(e.Line, "tty"), strings.HasPrefix(e.Line, ":"), strings.HasPrefix(e.Line, "seat"), strings.HasPrefix(e.Host, ":"):
		return protocol.SessionConsole
	}
	return protocol.SessionTerminal
}

// utmpUsers monta a lista: só as sessões cujo processo ainda existe (o utmp guarda a
// linha de uma sessão que caiu sem fechar).
func utmpUsers(entries []utmpEntry, alive func(pid int32) bool, gecos func(user string) string) []protocol.LoggedUser {
	var out []protocol.LoggedUser
	for _, e := range entries {
		if e.PID > 0 && !alive(e.PID) {
			continue
		}
		out = append(out, protocol.LoggedUser{Account: clip(e.User), DisplayName: clip(gecos(e.User)), Session: utmpSession(e), Active: true})
	}
	return tidyUsers(out)
}
