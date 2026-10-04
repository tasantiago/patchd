package credential

import "golang.org/x/sys/windows"

// restrictSDDL: controle total para SYSTEM (SY) e Administradores (BA), e mais ninguém.
// "P" protege a DACL: o arquivo não herda as permissões da pasta. Sem isso, dentro de
// C:\ProgramData o grupo Usuários herdaria leitura, e qualquer usuário da máquina
// leria a credencial.
const restrictSDDL = "D:P(A;;FA;;;SY)(A;;FA;;;BA)"

func restrict(path string) error {
	sd, err := windows.SecurityDescriptorFromString(restrictSDDL)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil)
}
