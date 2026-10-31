package scriptexec

import "sync"

// tailBuffer guarda apenas o fim do log, já durante a execução. O byte extra permite
// que clip informe truncamento sem manter todo o log em memória.
type tailBuffer struct {
	mu sync.Mutex
	b  []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	const capacity = MaxOutput + 1
	if len(p) >= capacity {
		b.b = append(b.b[:0], p[len(p)-capacity:]...)
	} else {
		if excess := len(b.b) + len(p) - capacity; excess > 0 {
			copy(b.b, b.b[excess:])
			b.b = b.b[:len(b.b)-excess]
		}
		b.b = append(b.b, p...)
	}
	return n, nil
}

func (b *tailBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.b...)
}
