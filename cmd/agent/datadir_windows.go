package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/tasantiago/patchd/internal/credential"
)

// Pasta de dados do agente no Windows (Aula 4.3; refeita na Aula 20.6, S7). É nela que os
// instaladores esperam a hora de rodar como SYSTEM: quem escreve nela vira SYSTEM.
//
// O que a versão anterior deixava passar: C:\ProgramData deixa qualquer usuário criar pastas.
// Quem criasse C:\ProgramData\patchd antes do install ficava dono dela, e o dono de um objeto
// sempre pode trocar a DACL dele, por mais protegida que esteja. Bastava esperar o install e
// devolver a si mesmo o controle total. Agora:
//
//   - a pasta nova nasce já protegida (CreateDirectory com o descritor: sem a janela entre
//     criar e proteger, em que ela herda as permissões de C:\ProgramData);
//   - a pasta não pode ser link nem junção (recusa);
//   - o dono passa a ser Administradores, e tudo dentro volta a herdar só da pasta (dono
//     Administradores, sem permissões próprias); links e junções lá dentro são apagados.
//     Cada correção vai para note, e daí para o log.

// dataDirSDDL: dono Administradores (O:BA); controle total para SYSTEM e Administradores,
// herdado por tudo dentro (OI arquivos, CI pastas); DACL protegida (P): não herda de
// C:\ProgramData, onde o grupo Usuários tem leitura e cria pastas.
const dataDirSDDL = "O:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"

// restrictedSDDL: a credencial (credential.json) tem DACL própria (Aula 4.3), a mesma do pacote credential.
const restrictedSDDL = "O:BAD:P(A;;FA;;;SY)(A;;FA;;;BA)"

func protectDataDir(dir string, note func(string)) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil { // #nosec G301 -- C:\ProgramData já existe; a ACL é a do Windows
		return err
	}
	sd, err := windows.SecurityDescriptorFromString(dataDirSDDL)
	if err != nil {
		return err
	}
	switch fi, err := os.Lstat(dir); {
	case errors.Is(err, fs.ErrNotExist):
		sa := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
		if err := windows.CreateDirectory(windows.StringToUTF16Ptr(dir), sa); err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			return fmt.Errorf("criar %s: %w", dir, err)
		}
	case err != nil:
		return err
	case fi.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 || !fi.IsDir():
		return fmt.Errorf("%s não é uma pasta comum (link, junção ou arquivo): confira quem a criou, remova e rode o install de novo", dir)
	}

	// Trocar o dono de algo que outra conta criou pede os privilégios de tomar posse e de
	// restaurar (o SYSTEM e o administrador elevado têm, desligados).
	enablePrivileges("SeTakeOwnershipPrivilege", "SeRestorePrivilege")
	if err := secure(dir, sd, true, note, "."); err != nil {
		return err
	}
	credSD, err := windows.SecurityDescriptorFromString(restrictedSDDL)
	if err != nil {
		return err
	}
	inherit, err := windows.SecurityDescriptorFromString("O:BAD:")
	if err != nil {
		return err
	}
	// A pasta já está protegida (dono Administradores, só SYSTEM e Administradores): daqui
	// em diante ninguém mais troca nada lá dentro durante a varredura.
	// #nosec G122 -- a pasta foi protegida acima; o Windows não tem o os.Root para ACL
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == dir {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if d.Type()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
			if err := os.Remove(p); err != nil {
				return fmt.Errorf("apagar o link %s: %w", rel, err)
			}
			note("link ou junção apagado: " + rel)
			return nil
		}
		if rel == credential.FileName {
			return secure(p, credSD, true, note, rel)
		}
		return secure(p, inherit, false, note, rel)
	})
}

// secure aplica o dono e a DACL de sd. protected: a DACL é só a de sd; senão, a DACL própria
// fica vazia e vale só o que vem da pasta (UNPROTECTED).
func secure(path string, sd *windows.SECURITY_DESCRIPTOR, protected bool, note func(string), rel string) error {
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if dacl == nil {
		// DACL nula no Windows é "todos com controle total": nunca.
		return errors.New("descritor sem DACL")
	}
	if cur, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION); err == nil {
		if o, _, err := cur.Owner(); err == nil && !trustedOwner(o) {
			note(fmt.Sprintf("dono trocado para Administradores: %s (era %s)", rel, o.String()))
		}
	}
	info := windows.SECURITY_INFORMATION(windows.OWNER_SECURITY_INFORMATION | windows.DACL_SECURITY_INFORMATION)
	if protected {
		info |= windows.PROTECTED_DACL_SECURITY_INFORMATION
	} else {
		info |= windows.UNPROTECTED_DACL_SECURITY_INFORMATION
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, info, owner, nil, dacl, nil); err != nil {
		return fmt.Errorf("proteger %s: %w", rel, err)
	}
	return nil
}

// trustedOwner: SYSTEM e Administradores. Uma conta de administrador que rodou o install
// também aparece aqui como "outra": a troca para Administradores é inofensiva nesse caso.
func trustedOwner(s *windows.SID) bool {
	return s.IsWellKnown(windows.WinLocalSystemSid) || s.IsWellKnown(windows.WinBuiltinAdministratorsSid)
}

// enablePrivileges liga, no token do processo, os privilégios que ele já tem.
func enablePrivileges(names ...string) {
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &tok); err != nil {
		return
	}
	defer tok.Close()
	for _, n := range names {
		var luid windows.LUID
		if err := windows.LookupPrivilegeValue(nil, windows.StringToUTF16Ptr(n), &luid); err != nil {
			continue
		}
		tp := windows.Tokenprivileges{PrivilegeCount: 1}
		tp.Privileges[0] = windows.LUIDAndAttributes{Luid: luid, Attributes: windows.SE_PRIVILEGE_ENABLED}
		_ = windows.AdjustTokenPrivileges(tok, false, &tp, 0, nil, nil)
	}
}
