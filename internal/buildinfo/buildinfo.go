// Package buildinfo expõe a identificação do binário: a versão injetada no build
// via -ldflags e as informações de controle de versão que o próprio Go grava no executável.
package buildinfo

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// Version é definida no build com:
//
//	-ldflags "-X github.com/tasantiago/patchd/internal/buildinfo.Version=v0.1.0"
//
// Precisa ser uma variável string de pacote, sem inicialização por função, para o -X funcionar.
var Version = "dev"

// Commit pode ser injetado com -X quando o build acontece sem a pasta .git
// (por exemplo, dentro do docker build). Se o Go encontrar o commit no git, o do git prevalece.
var Commit = ""

// Info reúne o que identifica um binário do patchd.
type Info struct {
	Version   string // versão de release (ldflags) ou a que o Go derivou do git
	Commit    string // hash do commit usado no build
	CommitAt  string // data do commit, em RFC 3339
	Modified  bool   // havia alterações não commitadas no momento do build
	GoVersion string // versão do Go usada no build
	OS        string // GOOS do binário
	Arch      string // GOARCH do binário
}

// Get monta a Info a partir das variáveis injetadas e dos metadados que o Go grava no executável.
func Get() Info {
	info := Info{
		Version:   Version,
		Commit:    "desconhecido",
		GoVersion: runtime.Version(),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
	}
	if Commit != "" {
		info.Commit = Commit
	}

	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return info
	}

	// Sem versão via ldflags, usa a que o Go derivou do git (quando houver).
	if info.Version == "dev" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		info.Version = bi.Main.Version
	}

	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			info.Commit = s.Value
		case "vcs.time":
			info.CommitAt = s.Value
		case "vcs.modified":
			info.Modified = s.Value == "true"
		}
	}
	return info
}

// String formata a Info em uma linha, para logs e para a saída de versão.
// Receptor por valor: assim tanto Info quanto *Info implementam fmt.Stringer.
func (i Info) String() string {
	commit := i.Commit
	if len(commit) > 12 {
		commit = commit[:12]
	}
	if i.Modified {
		commit += "+alterado"
	}
	return fmt.Sprintf("%s (commit %s, %s, %s/%s)", i.Version, commit, i.GoVersion, i.OS, i.Arch)
}
