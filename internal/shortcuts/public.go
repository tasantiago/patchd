package shortcuts

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/tasantiago/patchd/internal/protocol"
)

// PublicManager mantém os atalhos da área de trabalho de todos os usuários que o servidor
// pede (protocol.DesktopShortcut), no agente do Windows, como SYSTEM. Só mexe no que criou:
// o controle (StatePath, na pasta de dados do agente) guarda o arquivo e o SHA-256 de cada
// atalho; um atalho que alguém alterou não é apagado.
//
// Para cada atalho da lista: com o executável no disco, o atalho é criado (ou refeito, se a
// regra mudou); sem o executável (o programa não está instalado, ou saiu), o atalho que o
// patchd criou é apagado. Um arquivo com o mesmo nome que o patchd não criou (o atalho que o
// próprio instalador pôs na área de trabalho pública) é trocado pelo da regra: é o que o
// admin pediu, com o nome e o ícone dele.
type PublicManager struct {
	Desktop   string // a área de trabalho pública (C:\Users\Public\Desktop)
	StatePath string
	// Expand troca a variável do começo do caminho (%ProgramFiles%) pela da máquina.
	Expand func(path string) (string, error)
	// Write cria o atalho em path (com a extensão .lnk), apontando para target, com o ícone
	// icon (vazio: o do executável) e o índice dele.
	Write func(path string, s protocol.DesktopShortcut, target, icon string, index int) error
}

// Estados de cada atalho no resultado.
const (
	PublicCreated   = "criado"
	PublicCurrent   = "em_dia"
	PublicRemoved   = "removido"
	PublicNoProgram = "sem_programa" // o executável não está no disco: nada a fazer
	PublicKept      = "mantido"      // alterado por alguém: o patchd deixou de controlar
	PublicError     = "erro"
)

// PublicResult é o que aconteceu com um atalho.
type PublicResult struct {
	App, Name, State, Detail string
}

type publicEntry struct {
	App, File, SHA256, Spec string
}

type publicState struct {
	Version int
	Entries map[string]publicEntry // o nome do arquivo em minúsculas
}

func publicFile(s protocol.DesktopShortcut) string { return s.Name + ".lnk" }

func (m *PublicManager) load() publicState {
	st := publicState{Version: 1, Entries: map[string]publicEntry{}}
	data, err := os.ReadFile(m.StatePath)
	if err != nil || len(data) > 256<<10 {
		return st
	}
	var old publicState
	if json.Unmarshal(data, &old) != nil || old.Version != 1 || old.Entries == nil {
		return st // ilegível: começa do zero, sem apagar nada (não se sabe o que é do patchd)
	}
	for k, e := range old.Entries {
		if safeFile(e.File) && strings.HasSuffix(strings.ToLower(e.File), ".lnk") && strings.ToLower(e.File) == k {
			st.Entries[k] = e
		}
	}
	return st
}

func (m *PublicManager) save(st publicState) error {
	if err := os.MkdirAll(filepath.Dir(m.StatePath), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(m.StatePath), ".atalhos-publicos-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0o600); err == nil {
		_, err = f.Write(b)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, m.StatePath)
}

// regular: o caminho é um arquivo comum.
func regular(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// Apply aplica a lista e devolve o que aconteceu com cada atalho. Uma lista que não confere
// não muda nada.
func (m *PublicManager) Apply(list protocol.DesktopShortcuts) ([]PublicResult, error) {
	if err := list.Check(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(m.Desktop) || !filepath.IsAbs(m.StatePath) || m.Write == nil || m.Expand == nil {
		return nil, errors.New("área de trabalho pública ou gravador de atalhos inválidos")
	}
	if info, err := os.Stat(m.Desktop); err != nil || !info.IsDir() {
		return nil, errors.New("área de trabalho pública indisponível")
	}
	st := m.load()
	var out []PublicResult
	type want struct {
		s            protocol.DesktopShortcut
		target, icon string
		index        int
	}
	desired := map[string]want{}
	for _, s := range list.Items {
		target, err := m.Expand(s.Target)
		if err != nil {
			out = append(out, PublicResult{App: s.App, Name: s.Name, State: PublicError, Detail: err.Error()})
			continue
		}
		if !regular(target) {
			out = append(out, PublicResult{App: s.App, Name: s.Name, State: PublicNoProgram})
			continue
		}
		w := want{s: s, target: target}
		if s.Icon != "" {
			file, index := protocol.SplitIcon(s.Icon)
			icon, err := m.Expand(file)
			if err == nil && regular(icon) {
				w.icon, w.index = icon, index
			} // o ícone que não existe fica o do executável
		}
		desired[strings.ToLower(publicFile(s))] = w
	}
	// O que o patchd criou e não vale mais: sai, se ninguém mexeu.
	keys := make([]string, 0, len(st.Entries))
	for k := range st.Entries {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if _, ok := desired[k]; ok {
			continue
		}
		e := st.Entries[k]
		delete(st.Entries, k)
		path := filepath.Join(m.Desktop, e.File)
		hash, err := fileHash(path)
		name := strings.TrimSuffix(e.File, filepath.Ext(e.File))
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil || hash != e.SHA256:
			out = append(out, PublicResult{App: e.App, Name: name, State: PublicKept, Detail: "alterado por outra pessoa: fica"})
		default:
			if err := os.Remove(path); err != nil {
				st.Entries[k] = e // tenta de novo no próximo ciclo
				out = append(out, PublicResult{App: e.App, Name: name, State: PublicError, Detail: "não foi possível apagar: " + err.Error()})
				continue
			}
			out = append(out, PublicResult{App: e.App, Name: name, State: PublicRemoved})
		}
	}
	// Os que valem: em dia, ou criados de novo.
	names := make([]string, 0, len(desired))
	for k := range desired {
		names = append(names, k)
	}
	slices.Sort(names)
	for _, k := range names {
		w := desired[k]
		file := publicFile(w.s)
		path := filepath.Join(m.Desktop, file)
		spec := w.s.Fingerprint()
		if e, ok := st.Entries[k]; ok && e.Spec == spec {
			if hash, err := fileHash(path); err == nil && hash == e.SHA256 {
				out = append(out, PublicResult{App: w.s.App, Name: w.s.Name, State: PublicCurrent})
				continue
			}
		}
		hash, err := m.write(path, w.s, w.target, w.icon, w.index)
		if err != nil {
			out = append(out, PublicResult{App: w.s.App, Name: w.s.Name, State: PublicError, Detail: short(err.Error())})
			continue
		}
		st.Entries[k] = publicEntry{App: w.s.App, File: file, SHA256: hash, Spec: spec}
		out = append(out, PublicResult{App: w.s.App, Name: w.s.Name, State: PublicCreated})
	}
	return out, m.save(st)
}

// write grava num temporário da mesma pasta e troca pelo nome final.
func (m *PublicManager) write(path string, s protocol.DesktopShortcut, target, icon string, index int) (string, error) {
	tmp, err := os.CreateTemp(m.Desktop, ".patchd-*.lnk")
	if err != nil {
		return "", fmt.Errorf("preparar o atalho: %w", err)
	}
	name := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(name) // o gravador cria o arquivo
	defer os.Remove(name)
	if err := m.Write(name, s, target, icon, index); err != nil {
		return "", err
	}
	hash, err := fileHash(name)
	if err != nil {
		return "", err
	}
	if err := os.Rename(name, path); err != nil {
		return "", err
	}
	return hash, nil
}
