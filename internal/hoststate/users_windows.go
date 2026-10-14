package hoststate

import (
	"context"
	"errors"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/tasantiago/patchd/internal/protocol"
)

// loggedUsers lista as sessões do Terminal Services com usuário: a do console e as de
// Área de Trabalho Remota, ativas ou desconectadas. A sessão 0 (serviços) e as sem
// usuário (a tela de login) ficam de fora. O agente roda como LocalSystem, que pode pedir
// o token de qualquer sessão (WTSQueryUserToken); a conta sai do SID do token.
func loggedUsers(ctx context.Context) ([]protocol.LoggedUser, error) {
	var list *windows.WTS_SESSION_INFO
	var n uint32
	// Handle 0 = WTS_CURRENT_SERVER_HANDLE: esta máquina.
	if err := windows.WTSEnumerateSessions(0, 0, 1, &list, &n); err != nil {
		return nil, errors.New("WTSEnumerateSessions: " + err.Error())
	}
	defer windows.WTSFreeMemory(uintptr(unsafe.Pointer(list)))
	computer, _ := windows.ComputerName()
	var out []protocol.LoggedUser
	for _, s := range unsafe.Slice(list, n) {
		if s.SessionID == 0 || (s.State != windows.WTSActive && s.State != windows.WTSDisconnected) {
			continue
		}
		account, domain, ok := sessionAccount(s.SessionID)
		if !ok {
			continue
		}
		station := windows.UTF16PtrToString(s.WindowStationName)
		u := protocol.LoggedUser{Account: clip(domain + `\` + account), Session: windowsSession(station), Active: s.State == windows.WTSActive}
		u.DisplayName = clip(names.get(u.Account, func() string { return windowsDisplayName(domain, account, computer) }))
		out = append(out, u)
	}
	return tidyUsers(out), nil
}

// sessionAccount: a conta dona da sessão, pelo token. Sem token, não há usuário.
func sessionAccount(id uint32) (account, domain string, ok bool) {
	var tok windows.Token
	if err := windows.WTSQueryUserToken(id, &tok); err != nil {
		return "", "", false
	}
	defer tok.Close()
	tu, err := tok.GetTokenUser()
	if err != nil {
		return "", "", false
	}
	account, domain, _, err = tu.User.Sid.LookupAccount("")
	return account, domain, err == nil && account != ""
}

// windowsSession: "Console" é a tela da máquina; "RDP-Tcp#N", a Área de Trabalho Remota.
// A desconectada perde o nome da estação: fica como console, que é onde ela volta a
// aparecer na maioria dos casos (a troca rápida de usuário).
func windowsSession(station string) string {
	if strings.HasPrefix(strings.ToLower(station), "rdp-") {
		return protocol.SessionRDP
	}
	return protocol.SessionConsole
}

// windowsDisplayName: conta local, o "Nome completo" da conta (NetUserGetInfo); conta do
// domínio, o displayName do AD (TranslateName, que pergunta ao controlador de domínio com
// a conta da máquina).
func windowsDisplayName(domain, account, computer string) string {
	if computer != "" && strings.EqualFold(domain, computer) {
		return localFullName(account)
	}
	return translateDisplayName(domain + `\` + account)
}

func translateDisplayName(sam string) string {
	in, err := windows.UTF16PtrFromString(sam)
	if err != nil {
		return ""
	}
	n := uint32(256)
	for range 2 {
		buf := make([]uint16, n)
		err = windows.TranslateName(in, windows.NameSamCompatible, windows.NameDisplay, &buf[0], &n)
		if err == nil {
			return windows.UTF16ToString(buf)
		}
		if !errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) || n == 0 || n > 4096 {
			return ""
		}
	}
	return ""
}

func localFullName(account string) string {
	name, err := windows.UTF16PtrFromString(account)
	if err != nil {
		return ""
	}
	var b *byte
	if err := windows.NetUserGetInfo(nil, name, 10, &b); err != nil {
		return ""
	}
	defer windows.NetApiBufferFree(b)
	return windows.UTF16PtrToString((*windows.UserInfo10)(unsafe.Pointer(b)).FullName)
}
