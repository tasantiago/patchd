package api

import (
	"sync"
	"time"
)

// credCache guarda por pouco tempo a resposta de MachineByCredential, só para o canal em tempo
// real: o agente refaz esse pedido a cada 45 s, e cada um custava uma consulta ao banco só para
// saber de quem é a credencial (com o parque inteiro, a maior parte das conexões tiradas do
// pool). Os outros pedidos do agente conferem no banco sempre.
//
// Só a resposta positiva é guardada (uma credencial desconhecida vai ao banco toda vez), e por
// credTTL. Aposentar, mesclar ou registrar pelo painel esvazia o cache na hora; pela linha de
// comando (outro processo), a máquina aposentada ainda segura o canal por até credTTL, sem
// conseguir mandar nem receber nada pelas outras rotas.
type credCache struct {
	mu sync.Mutex
	m  map[string]credEntry
}

type credEntry struct {
	id      string
	expires time.Time
}

const (
	credTTL = time.Minute
	// credMax: acima disto o cache recomeça vazio (protege a memória; um parque grande cabe).
	credMax = 100_000
)

func (c *credCache) get(hash []byte, now time.Time) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[string(hash)]
	if !ok || !now.Before(e.expires) {
		return "", false
	}
	return e.id, true
}

func (c *credCache) put(hash []byte, id string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil || len(c.m) >= credMax {
		c.m = make(map[string]credEntry)
	}
	c.m[string(hash)] = credEntry{id: id, expires: now.Add(credTTL)}
}

// reset esvazia o cache (uma credencial deixou de valer).
func (c *credCache) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m = nil
}
