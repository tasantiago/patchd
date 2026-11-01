// Package shortcuts mantém apenas os atalhos pertencentes ao patchd na sessão do usuário.
package shortcuts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/tasantiago/patchd/internal/protocol"
)

type Writer func(string, protocol.StandardShortcut) error
type Manager struct {
	Desktop, StatePath string
	Write              Writer
}
type owned struct{ File, SHA256, Spec string }
type manifest struct {
	Version int
	Entries map[string]owned
}

func fileHash(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return "", errors.New("atalho não é um arquivo regular pequeno")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, 64<<10+1)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func shortcutFile(s protocol.StandardShortcut) string {
	if runtime.GOOS == "linux" {
		return s.Name + ".desktop"
	}
	if runtime.GOOS == "darwin" {
		if s.Kind == "site" {
			return s.Name + ".webloc"
		}
		return s.Name + ".fileloc"
	}
	if s.Kind == "site" {
		return s.Name + ".url"
	}
	return s.Name + ".lnk"
}
func safeFile(name string) bool {
	return name != "" && filepath.Base(name) == name && !strings.ContainsAny(name, `/\:`) && (strings.HasSuffix(name, ".url") || strings.HasSuffix(name, ".lnk") || strings.HasSuffix(name, ".desktop") || strings.HasSuffix(name, ".webloc") || strings.HasSuffix(name, ".fileloc"))
}
func short(s string) string {
	r := []rune(strings.ReplaceAll(strings.ReplaceAll(s, "\r", " "), "\n", " "))
	if len(r) > 200 {
		return string(r[:200])
	}
	return string(r)
}

func (m *Manager) Apply(plan protocol.StandardizationPlan) (protocol.StandardizationResult, error) {
	result := protocol.StandardizationResult{Revision: plan.Revision, Items: []protocol.StandardShortcutResult{}}
	if err := plan.Check(); err != nil {
		return result, err
	}
	if !filepath.IsAbs(m.Desktop) || !filepath.IsAbs(m.StatePath) || m.Write == nil {
		return result, errors.New("diretórios ou gravador de atalhos inválidos")
	}
	if info, err := os.Stat(m.Desktop); err != nil || !info.IsDir() {
		return result, errors.New("área de trabalho indisponível")
	}
	old := manifest{Version: 1, Entries: map[string]owned{}}
	if data, err := os.ReadFile(m.StatePath); err == nil {
		if len(data) > 128<<10 {
			return result, errors.New("controle de atalhos excede o limite")
		}
		if err := json.Unmarshal(data, &old); err != nil || old.Version != 1 || old.Entries == nil {
			return result, errors.New("controle de atalhos ilegível; arquivos existentes foram preservados")
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return result, err
	}
	next := manifest{Version: 1, Entries: map[string]owned{}}
	desired := map[string]protocol.StandardShortcut{}
	for _, s := range plan.Shortcuts {
		desired[strings.ToLower(shortcutFile(s))] = s
	}
	keys := []string{}
	for key := range old.Entries {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		entry := old.Entries[key]
		if !safeFile(entry.File) {
			return result, errors.New("controle de atalhos contém caminho inválido")
		}
		if _, keep := desired[key]; keep {
			continue
		}
		path := filepath.Join(m.Desktop, entry.File)
		hash, err := fileHash(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil || hash != entry.SHA256 {
			result.Items = append(result.Items, protocol.StandardShortcutResult{Name: strings.TrimSuffix(entry.File, filepath.Ext(entry.File)), State: "conflito", Detail: "Atalho alterado pelo usuário foi preservado."})
			continue
		}
		if err := os.Remove(path); err != nil {
			result.Items = append(result.Items, protocol.StandardShortcutResult{Name: strings.TrimSuffix(entry.File, filepath.Ext(entry.File)), State: "conflito", Detail: "Não foi possível remover o atalho gerenciado."})
			next.Entries[key] = entry
			continue
		}
		result.Items = append(result.Items, protocol.StandardShortcutResult{Name: strings.TrimSuffix(entry.File, filepath.Ext(entry.File)), State: "removido"})
	}
	for _, s := range plan.Shortcuts {
		file := shortcutFile(s)
		key := strings.ToLower(file)
		path := filepath.Join(m.Desktop, file)
		entry, managed := old.Entries[key]
		item := protocol.StandardShortcutResult{Name: s.Name, State: "conflito"}
		hash, err := fileHash(path)
		if err == nil && (!managed || hash != entry.SHA256) {
			item.Detail = "Já existe um arquivo pessoal ou um atalho modificado com esse nome."
			result.Items = append(result.Items, item)
			continue
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			item.Detail = "Destino não é um atalho gerenciado acessível."
			result.Items = append(result.Items, item)
			continue
		}
		if managed && err == nil && entry.Spec == s.Fingerprint() {
			next.Entries[key] = entry
			item.State = "em_dia"
			result.Items = append(result.Items, item)
			continue
		}
		tmp, err := os.CreateTemp(m.Desktop, ".patchd-*"+filepath.Ext(file))
		if err != nil {
			item.Detail = "Não foi possível preparar o atalho."
			result.Items = append(result.Items, item)
			continue
		}
		tmpName := tmp.Name()
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		err = m.Write(tmpName, s)
		newHash := ""
		if err == nil {
			newHash, err = fileHash(tmpName)
		}
		if err == nil {
			err = os.Rename(tmpName, path)
		}
		_ = os.Remove(tmpName)
		if err != nil {
			item.Detail = short(fmt.Sprintf("Não foi possível aplicar: %v", err))
			result.Items = append(result.Items, item)
			if managed {
				next.Entries[key] = entry
			}
			continue
		}
		next.Entries[key] = owned{File: file, SHA256: newHash, Spec: s.Fingerprint()}
		item.State = "aplicado"
		result.Items = append(result.Items, item)
	}
	if err := os.MkdirAll(filepath.Dir(m.StatePath), 0700); err != nil {
		return result, err
	}
	b, err := json.Marshal(next)
	if err != nil {
		return result, err
	}
	f, err := os.CreateTemp(filepath.Dir(m.StatePath), ".atalhos-*")
	if err != nil {
		return result, err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return result, err
	}
	return result, os.Rename(tmp, m.StatePath)
}
