package authsig

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// Limites da leitura pela rede: a assinatura de um executável sai em 2 ou 3 pedidos; a de um
// MSI, em uns 10. Acima disso, desiste (o painel pede para preencher à mão).
const (
	remoteBlock    = 64 << 10
	remoteRequests = 40
	remoteFull     = 16 << 20 // o servidor sem Range: lê o arquivo inteiro só até este tamanho
)

// ErrNoRange: o servidor não entrega pedaços do arquivo (Range) e o arquivo é grande.
var ErrNoRange = errors.New("o servidor do instalador não entrega pedaços do arquivo, e baixá-lo inteiro só para ler a assinatura é demais")

// Remote lê o signatário do instalador no endereço, sem baixá-lo: só os pedaços com o
// cabeçalho e a assinatura (pedidos com Range).
func Remote(ctx context.Context, client *http.Client, url, userAgent string) (Signer, error) {
	rr := &rangeReader{ctx: ctx, client: client, url: url, ua: userAgent, blocks: map[int64][]byte{}, size: -1}
	if _, err := rr.block(0); err != nil {
		return Signer{}, err
	}
	return Read(rr, rr.size)
}

type rangeReader struct {
	ctx    context.Context
	client *http.Client
	url    string
	ua     string
	size   int64
	blocks map[int64][]byte
	reqs   int
}

func (r *rangeReader) ReadAt(p []byte, off int64) (int, error) {
	n := 0
	for n < len(p) {
		pos := off + int64(n)
		if r.size >= 0 && pos >= r.size {
			return n, io.EOF
		}
		b, err := r.block(pos / remoteBlock)
		if err != nil {
			return n, err
		}
		in := pos % remoteBlock
		if in >= int64(len(b)) {
			return n, io.EOF
		}
		n += copy(p[n:], b[in:])
	}
	return n, nil
}

// block traz o bloco i (remoteBlock bytes), da memória ou num pedido com Range.
func (r *rangeReader) block(i int64) ([]byte, error) {
	if b, ok := r.blocks[i]; ok {
		return b, nil
	}
	if r.reqs >= remoteRequests {
		return nil, fmt.Errorf("a assinatura não saiu em %d pedidos", remoteRequests)
	}
	r.reqs++
	req, err := http.NewRequestWithContext(r.ctx, http.MethodGet, r.url, nil)
	if err != nil {
		return nil, err
	}
	start := i * remoteBlock
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, start+remoteBlock-1))
	if r.ua != "" {
		req.Header.Set("User-Agent", r.ua)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusPartialContent:
		total, ok := contentRangeTotal(resp.Header.Get("Content-Range"))
		if !ok {
			return nil, errors.New("resposta parcial sem o tamanho do arquivo (Content-Range)")
		}
		r.size = total
		b, err := io.ReadAll(io.LimitReader(resp.Body, remoteBlock))
		if err != nil {
			return nil, err
		}
		r.blocks[i] = b
		return b, nil
	case http.StatusOK:
		// Sem Range: o arquivo inteiro, se for pequeno.
		if resp.ContentLength > remoteFull {
			return nil, ErrNoRange
		}
		all, err := io.ReadAll(io.LimitReader(resp.Body, remoteFull+1))
		if err != nil {
			return nil, err
		}
		if len(all) > remoteFull {
			return nil, ErrNoRange
		}
		r.size = int64(len(all))
		for k := int64(0); k*remoteBlock < int64(len(all)); k++ {
			r.blocks[k] = all[k*remoteBlock : min((k+1)*remoteBlock, int64(len(all)))]
		}
		if b, ok := r.blocks[i]; ok {
			return b, nil
		}
		return nil, io.EOF
	case http.StatusRequestedRangeNotSatisfiable:
		return nil, io.EOF
	}
	return nil, fmt.Errorf("GET %s: %s", r.url, resp.Status)
}

// contentRangeTotal: o total de "bytes 0-65535/1234567".
func contentRangeTotal(h string) (int64, bool) {
	_, total, ok := strings.Cut(h, "/")
	if !ok || total == "*" {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(total), 10, 64)
	return n, err == nil && n > 0
}
