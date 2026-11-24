package platform

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Instalar no perfil do usuário (a Central de software, programas por usuário): o serviço roda
// como SYSTEM e não instala no perfil de ninguém. O instalador conferido é copiado para uma
// pasta que só SYSTEM, os Administradores e o usuário leem (dentro da pasta de dados do agente,
// que o usuário não cria nem troca), e roda com o token da sessão do usuário (o mesmo que o
// Windows deu a ele no logon: sem elevação), com o ambiente dele (o %LOCALAPPDATA% certo).

var sidPattern = regexp.MustCompile(`^S-1-[0-9]+(-[0-9]+){1,14}$`)

// UserSession acha a sessão do usuário: pelo SID (vazio: pela conta, DOMINIO\usuario). Prefere
// a sessão ativa à desconectada. Devolve a sessão e o SID; ErrNoUserSession se não há.
func UserSession(sid, account string) (uint32, string, error) {
	sessions, err := Sessions()
	if err != nil {
		return 0, "", err
	}
	found, foundSID, active := uint32(0), "", false
	for id, isActive := range sessions {
		var tok windows.Token
		if windows.WTSQueryUserToken(id, &tok) != nil {
			continue
		}
		tu, err := tok.GetTokenUser()
		if err != nil {
			tok.Close()
			continue
		}
		s := tu.User.Sid.String()
		match := sid != "" && strings.EqualFold(s, sid)
		if sid == "" && account != "" {
			if acc, dom, _, err := tu.User.Sid.LookupAccount(""); err == nil {
				match = strings.EqualFold(dom+`\`+acc, account)
			}
		}
		tok.Close()
		if match && (foundSID == "" || isActive && !active) {
			found, foundSID, active = id, s, isActive
		}
	}
	if foundSID == "" {
		return 0, "", ErrNoUserSession
	}
	return found, foundSID, nil
}

// StageForUser copia src para uma pasta nova dentro de dir que só SYSTEM, os Administradores e
// o usuário (sid) leem e executam. Devolve o caminho da cópia e a limpeza.
func StageForUser(src, dir, sid string) (string, func(), error) {
	if !sidPattern.MatchString(sid) {
		return "", nil, fmt.Errorf("SID %q inválido", sid)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;GRGX;;;" + sid + ")")
	if err != nil {
		return "", nil, fmt.Errorf("permissões da pasta: %w", err)
	}
	sa := windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(sa))
	var stage string
	for i := 0; ; i++ {
		stage = filepath.Join(dir, fmt.Sprintf("usuario-%d-%d", time.Now().UnixNano(), i))
		p, err := windows.UTF16PtrFromString(stage)
		if err != nil {
			return "", nil, err
		}
		err = windows.CreateDirectory(p, &sa)
		if err == nil {
			break
		}
		if !errors.Is(err, windows.ERROR_ALREADY_EXISTS) || i > 5 {
			return "", nil, fmt.Errorf("criar a pasta do instalador: %w", err)
		}
	}
	cleanup := func() { _ = os.RemoveAll(stage) }
	dst := filepath.Join(stage, filepath.Base(src))
	if err := copyFile(src, dst); err != nil {
		cleanup()
		return "", nil, err
	}
	return dst, cleanup, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// RunAsUser roda o programa na sessão, como o usuário dela, sem janela, e espera terminar (ou o
// ctx: aí o processo é encerrado). Devolve o código de saída.
func RunAsUser(ctx context.Context, session uint32, prog string, args []string) (int, error) {
	var tok windows.Token
	if err := windows.WTSQueryUserToken(session, &tok); err != nil {
		return 0, fmt.Errorf("WTSQueryUserToken da sessão %d: %w", session, err)
	}
	defer tok.Close()
	var env *uint16
	if err := windows.CreateEnvironmentBlock(&env, tok, false); err != nil {
		return 0, fmt.Errorf("ambiente do usuário: %w", err)
	}
	defer windows.DestroyEnvironmentBlock(env)
	parts := []string{windows.EscapeArg(prog)}
	for _, a := range args {
		parts = append(parts, windows.EscapeArg(a))
	}
	app, err := windows.UTF16PtrFromString(prog)
	if err != nil {
		return 0, err
	}
	cmd, err := windows.UTF16PtrFromString(strings.Join(parts, " "))
	if err != nil {
		return 0, err
	}
	desk, err := windows.UTF16PtrFromString(`winsta0\default`)
	if err != nil {
		return 0, err
	}
	si := windows.StartupInfo{Desktop: desk, Flags: windows.STARTF_USESHOWWINDOW, ShowWindow: windows.SW_HIDE}
	si.Cb = uint32(unsafe.Sizeof(si))
	var pi windows.ProcessInformation
	err = windows.CreateProcessAsUser(tok, app, cmd, nil, nil, false, windows.CREATE_UNICODE_ENVIRONMENT|windows.CREATE_NO_WINDOW,
		env, nil, &si, &pi)
	if errors.Is(err, windows.ERROR_ELEVATION_REQUIRED) {
		return 0, errors.New("o instalador pede administrador: não é um instalador por usuário (desmarque \"instala no perfil do usuário\" no programa)")
	}
	if err != nil {
		return 0, fmt.Errorf("iniciar o instalador como o usuário da sessão %d: %w", session, err)
	}
	defer windows.CloseHandle(pi.Process)
	defer windows.CloseHandle(pi.Thread)
	for {
		ev, err := windows.WaitForSingleObject(pi.Process, 1000)
		if err != nil {
			return 0, err
		}
		if ev == windows.WAIT_OBJECT_0 {
			break
		}
		if ctx.Err() != nil {
			_ = windows.TerminateProcess(pi.Process, 1)
			return 0, ctx.Err()
		}
	}
	var code uint32
	if err := windows.GetExitCodeProcess(pi.Process, &code); err != nil {
		return 0, err
	}
	return int(code), nil
}
