package shortcuts

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/tasantiago/patchd/internal/protocol"
)

// AppLink mantém o atalho da Central de software na área de trabalho de todos os usuários
// (C:\Users\Public\Desktop), no agente do Windows, como SYSTEM. O atalho aponta para o
// patchd-app.exe sem argumentos: aberto pelo usuário, a Central mostra a página (ou pede à que
// já está aberta na sessão que mostre).
//
// Não há arquivo de controle: o atalho da Central é qualquer .lnk da pasta que aponta para o
// executável dela (Target). Assim a troca de nome (o nome da Central no painel) apaga o
// antigo, e a desinstalação do agente acha o que tirar sem a pasta de dados. Um .lnk com o
// nome do atalho que aponta para outro lugar é trocado: o nome é o da Central.
type AppLink struct {
	Desktop string // a área de trabalho pública
	Target  string // o patchd-app.exe
	Icon    string // arquivo,índice do ícone; vazio: o do executável (Target,0)
	// Read: o executável para onde o .lnk aponta e o ícone dele (arquivo,índice).
	Read func(path string) (target, icon string, err error)
	// Write cria o .lnk em path apontando para target, com a descrição e o ícone.
	Write func(path, target, description, icon string) error
}

// Estados do atalho da Central.
const (
	AppLinkCreated = "criado"
	AppLinkUpdated = "refeito" // o mesmo atalho, com o ícone novo
	AppLinkCurrent = "em_dia"
)

func (l AppLink) check() error {
	if !filepath.IsAbs(l.Desktop) || !filepath.IsAbs(l.Target) || l.Read == nil || l.Write == nil {
		return errors.New("atalho da Central: pasta, executável ou gravador inválidos")
	}
	if info, err := os.Stat(l.Desktop); err != nil || !info.IsDir() {
		return errors.New("área de trabalho pública indisponível")
	}
	return nil
}

// ours: os .lnk da pasta que apontam para o executável da Central, com o ícone de cada um.
func (l AppLink) ours() (map[string]string, error) {
	entries, err := os.ReadDir(l.Desktop)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.EqualFold(filepath.Ext(e.Name()), ".lnk") || strings.HasPrefix(e.Name(), ".patchd-") {
			continue
		}
		if t, icon, err := l.Read(filepath.Join(l.Desktop, e.Name())); err == nil && samePath(t, l.Target) {
			out[e.Name()] = icon
		}
	}
	return out, nil
}

func samePath(a, b string) bool { return strings.EqualFold(filepath.Clean(a), filepath.Clean(b)) }

// icon: o ícone que o atalho deve ter.
func (l AppLink) icon() string {
	if l.Icon == "" {
		return l.Target + ",0"
	}
	return l.Icon
}

// sameIcon compara "arquivo,índice" sem diferença de maiúsculas no arquivo.
func sameIcon(a, b string) bool {
	fa, ia := protocol.SplitIcon(a)
	fb, ib := protocol.SplitIcon(b)
	return ia == ib && samePath(fa, fb)
}

// Sync deixa um atalho só, com o nome name (sem .lnk). Devolve o estado e os atalhos antigos
// apagados (o nome anterior da Central).
func (l AppLink) Sync(name string) (state string, removed []string, err error) {
	if err := l.check(); err != nil {
		return "", nil, err
	}
	if !ValidLinkName(name) {
		return "", nil, fmt.Errorf("nome do atalho %q inválido", name)
	}
	file := name + ".lnk"
	have, err := l.ours()
	if err != nil {
		return "", nil, err
	}
	names := slices.Sorted(maps.Keys(have))
	state = AppLinkCreated
	for _, f := range names {
		if strings.EqualFold(f, file) {
			if sameIcon(have[f], l.icon()) {
				state = AppLinkCurrent
			} else {
				state = AppLinkUpdated // o atalho de uma versão anterior, com outro ícone
			}
			continue
		}
		if err := os.Remove(filepath.Join(l.Desktop, f)); err != nil {
			return "", removed, fmt.Errorf("apagar o atalho antigo %s: %w", f, err)
		}
		removed = append(removed, strings.TrimSuffix(f, filepath.Ext(f)))
	}
	if state == AppLinkCurrent {
		return state, removed, nil
	}
	if err := l.write(filepath.Join(l.Desktop, file), name); err != nil {
		return "", removed, err
	}
	return state, removed, nil
}

// Remove apaga os atalhos da Central (a Central desligada, o agente desinstalado).
func (l AppLink) Remove() ([]string, error) {
	if err := l.check(); err != nil {
		return nil, err
	}
	have, err := l.ours()
	if err != nil {
		return nil, err
	}
	var removed []string
	var errs []error
	for _, f := range slices.Sorted(maps.Keys(have)) {
		if err := os.Remove(filepath.Join(l.Desktop, f)); err != nil {
			errs = append(errs, err)
			continue
		}
		removed = append(removed, strings.TrimSuffix(f, filepath.Ext(f)))
	}
	return removed, errors.Join(errs...)
}

// write grava num temporário da mesma pasta e troca pelo nome final.
func (l AppLink) write(path, name string) error {
	tmp, err := os.CreateTemp(l.Desktop, ".patchd-*.lnk")
	if err != nil {
		return fmt.Errorf("preparar o atalho: %w", err)
	}
	t := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(t) // o gravador cria o arquivo
	defer os.Remove(t)
	if err := l.Write(t, l.Target, name, l.icon()); err != nil {
		return err
	}
	return os.Rename(t, path)
}

// ValidLinkName: o nome vale como nome de arquivo do Windows (o mesmo critério dos atalhos
// da lista do servidor).
func ValidLinkName(name string) bool { return protocol.ValidDesktopName(name) }
