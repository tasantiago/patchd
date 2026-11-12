package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
	"github.com/tasantiago/patchd/internal/wucache"
)

// WinCacheFetcher traz do cache do servidor os arquivos de uma atualização do Windows
// (docs/cache-windows.md): pede ao servidor, espera ele baixar da Microsoft (a primeira máquina
// que precisa espera; as outras já encontram pronto), baixa com retomada e confere o SHA-256
// que o servidor anunciou. O Windows Update confere de novo ao receber (CopyToCache).
type WinCacheFetcher struct {
	Client  *Client
	Dir     string        // na pasta de dados do agente (só SYSTEM e administradores)
	Poll    time.Duration // de quanto em quanto tempo pergunta enquanto o servidor baixa (0: 15 s)
	MaxWait time.Duration // quanto espera o servidor baixar (0: 45 min); depois, o job é adiado
	// MaxSize: o maior arquivo aceito (0: o do servidor, wucache.DefaultMaxSize).
	MaxSize int64
}

// winCacheKeep: o arquivo baixado fica na máquina este tempo (um job repetido não baixa de
// novo); depois sai na próxima vez que o cache for usado.
const winCacheKeep = 7 * 24 * time.Hour

func (f WinCacheFetcher) poll() time.Duration {
	if f.Poll > 0 {
		return f.Poll
	}
	return 15 * time.Second
}

func (f WinCacheFetcher) maxWait() time.Duration {
	if f.MaxWait > 0 {
		return f.MaxWait
	}
	return 45 * time.Minute
}

func (f WinCacheFetcher) maxSize() int64 {
	if f.MaxSize > 0 {
		return f.MaxSize
	}
	return wucache.DefaultMaxSize
}

// Fetch devolve, para cada endereço, o arquivo local conferido.
func (f WinCacheFetcher) Fetch(ctx context.Context, urls []string) (map[string]string, error) {
	var list []string
	for _, u := range urls {
		if !slices.Contains(list, u) {
			list = append(list, u)
		}
	}
	if len(list) == 0 || len(list) > wucache.MaxURLs {
		return nil, fmt.Errorf("de 1 a %d arquivos por atualização (vieram %d)", wucache.MaxURLs, len(list))
	}
	if err := os.MkdirAll(f.Dir, 0o700); err != nil {
		return nil, err
	}
	f.prune()
	files, err := f.waitReady(ctx, list)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(files))
	for _, file := range files {
		p, err := f.download(ctx, file)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file.Key, err)
		}
		out[file.URL] = p
	}
	// O mapa sai pelo endereço pedido (o servidor devolve o mesmo, na mesma ordem).
	for i, u := range list {
		if _, ok := out[u]; !ok && i < len(files) {
			out[u] = out[files[i].URL]
		}
	}
	return out, nil
}

// waitReady pergunta ao servidor até todos os arquivos ficarem prontos.
func (f WinCacheFetcher) waitReady(ctx context.Context, urls []string) ([]protocol.WinCacheFile, error) {
	deadline := time.Now().Add(f.maxWait())
	for {
		var resp protocol.WinCacheResponse
		if _, err := f.Client.post(ctx, protocol.WinCachePath, protocol.WinCacheRequest{URLs: urls}, &resp); err != nil {
			return nil, fmt.Errorf("o cache do servidor não respondeu: %w", err)
		}
		if len(resp.Files) != len(urls) {
			return nil, fmt.Errorf("o servidor respondeu %d arquivos para %d pedidos", len(resp.Files), len(urls))
		}
		var waiting []string
		for _, file := range resp.Files {
			switch file.State {
			case wucache.StateReady:
				if !wucache.ValidKey(file.Key) || len(file.SHA256) != 64 || file.Size <= 0 || file.Size > f.maxSize() {
					return nil, fmt.Errorf("o servidor anunciou %q fora do formato", file.Key)
				}
			case wucache.StateFetching:
				w := file.Key
				if file.Size > 0 {
					w += fmt.Sprintf(" (%d%%)", file.Done*100/file.Size)
				}
				waiting = append(waiting, w)
			case wucache.StateRefused:
				return nil, fmt.Errorf("%w: o servidor não guarda %s: %s", jobs.ErrRefused, file.URL, file.Detail)
			default:
				return nil, fmt.Errorf("o servidor não conseguiu baixar %s da Microsoft: %s", file.Key, file.Detail)
			}
		}
		if len(waiting) == 0 {
			return resp.Files, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%w: o servidor ainda está baixando da Microsoft (%s)", jobs.ErrRetry, strings.Join(waiting, ", "))
		}
		t := time.NewTimer(f.poll())
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-t.C:
		}
	}
}

// download baixa um arquivo pronto (ou reaproveita o da última vez) e confere o SHA-256.
func (f WinCacheFetcher) download(ctx context.Context, file protocol.WinCacheFile) (string, error) {
	final := filepath.Join(f.Dir, file.Key)
	if sum, n, err := hashFile(final); err == nil && sum == file.SHA256 && n == file.Size {
		now := time.Now()
		_ = os.Chtimes(final, now, now)
		return final, nil
	}
	part := final + ".part"
	out, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) // #nosec G304 -- chave validada (wucache.ValidKey)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	n, err := f.Client.Download(ctx, protocol.WinCachePath+"/"+file.Key, io.MultiWriter(out, h), file.Size)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(part)
		return "", fmt.Errorf("download do servidor: %w", err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); n != file.Size || got != file.SHA256 {
		_ = os.Remove(part)
		return "", fmt.Errorf("o arquivo que chegou não é o anunciado (%d de %d bytes, SHA-256 %s)", n, file.Size, got)
	}
	if err := os.Rename(part, final); err != nil {
		_ = os.Remove(part)
		return "", err
	}
	return final, nil
}

func hashFile(p string) (string, int64, error) {
	f, err := os.Open(p) // #nosec G304 -- pasta do próprio agente
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, err
}

// prune tira da máquina os arquivos com mais de winCacheKeep.
func (f WinCacheFetcher) prune() {
	entries, err := os.ReadDir(f.Dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > winCacheKeep {
			if err := os.Remove(filepath.Join(f.Dir, e.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
				continue
			}
		}
	}
}
