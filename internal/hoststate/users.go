package hoststate

import (
	"cmp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/tasantiago/patchd/internal/protocol"
)

// Usuários conectados (Aula 15.6): quem tem sessão aberta na máquina, com o nome de
// exibição. No Windows, as sessões do Terminal Services (console e RDP) e o nome que o
// AD dá à conta; no Linux, o utmp e o GECOS do /etc/passwd.

// Prazos do nome de exibição guardado. O nome muda raramente; a busca no AD custa uma ida
// ao controlador de domínio, que um notebook fora da rede nem alcança.
const (
	nameTTL        = 12 * time.Hour
	nameMissTTL    = time.Hour       // sem nome (conta sem displayName, AD fora de alcance): tenta de novo depois
	nameWait       = 3 * time.Second // o check-in não espera mais que isso pelo nome
	maxNameEntries = 256
)

// nameCache guarda o nome de exibição por conta. Uma busca lenta não segura o check-in:
// depois de nameWait, o estado segue sem o nome e a busca termina em segundo plano, para
// o próximo check-in. Só uma busca por conta de cada vez.
type nameCache struct {
	mu   sync.Mutex
	m    map[string]*cachedName
	wait time.Duration
	now  func() time.Time
}

type cachedName struct {
	name    string
	until   time.Time
	pending bool
}

func newNameCache() *nameCache {
	return &nameCache{m: map[string]*cachedName{}, wait: nameWait, now: time.Now}
}

// get devolve o nome da conta: o guardado, se ainda vale, ou o que resolve achar.
func (c *nameCache) get(account string, resolve func() string) string {
	key := strings.ToLower(account)
	c.mu.Lock()
	e, ok := c.m[key]
	if ok && (e.pending || c.now().Before(e.until)) {
		name := e.name
		c.mu.Unlock()
		return name
	}
	if !ok {
		if len(c.m) >= maxNameEntries {
			clear(c.m) // um servidor de terminais com rodízio de contas não cresce sem fim
		}
		e = &cachedName{}
		c.m[key] = e
	}
	e.pending = true
	c.mu.Unlock()

	done := make(chan string, 1)
	go func() {
		name := resolve()
		c.mu.Lock()
		e.name, e.pending = name, false
		e.until = c.now().Add(nameTTL)
		if name == "" {
			e.until = c.now().Add(nameMissTTL)
		}
		c.mu.Unlock()
		done <- name
	}()
	select {
	case name := <-done:
		return name
	case <-time.After(c.wait):
		c.mu.Lock()
		defer c.mu.Unlock()
		return e.name // o anterior, se havia; a busca segue
	}
}

// names: o cache do processo (o agente coleta o estado a cada check-in).
var names = newNameCache()

// sessionOrder: a ordem dos tipos na lista.
var sessionOrder = map[string]int{protocol.SessionConsole: 0, protocol.SessionRDP: 1, protocol.SessionSSH: 2, protocol.SessionTerminal: 3}

// tidyUsers junta as repetidas (a mesma conta no mesmo tipo de sessão: no Linux, cada
// janela de terminal é uma linha do utmp) e ordena: ativas primeiro, depois console, RDP,
// SSH e terminal, depois a conta. Uma sessão ativa vence a desconectada da mesma conta.
func tidyUsers(in []protocol.LoggedUser) []protocol.LoggedUser {
	out := []protocol.LoggedUser{}
	idx := map[string]int{}
	for _, u := range in {
		k := strings.ToLower(u.Account) + "\x00" + u.Session
		if i, ok := idx[k]; ok {
			out[i].Active = out[i].Active || u.Active
			if out[i].DisplayName == "" {
				out[i].DisplayName = u.DisplayName
			}
			continue
		}
		idx[k] = len(out)
		out = append(out, u)
	}
	slices.SortStableFunc(out, func(a, b protocol.LoggedUser) int {
		if a.Active != b.Active {
			if a.Active {
				return -1
			}
			return 1
		}
		if c := cmp.Compare(sessionOrder[a.Session], sessionOrder[b.Session]); c != 0 {
			return c
		}
		return cmp.Compare(strings.ToLower(a.Account), strings.ToLower(b.Account))
	})
	if len(out) > protocol.MaxStateUsers {
		out = out[:protocol.MaxStateUsers]
	}
	return out
}

// clip corta o texto no limite do protocolo, sem quebrar um caractere, e tira os de controle.
func clip(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if len(s) <= 256 {
		return s
	}
	cut := 256
	for cut > 0 && !utf8Start(s[cut]) {
		cut--
	}
	return s[:cut]
}

func utf8Start(b byte) bool { return b&0xc0 != 0x80 }
