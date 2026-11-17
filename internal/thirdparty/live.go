package thirdparty

import "sync/atomic"

// Live é o manifesto que vale agora no servidor: o embutido, o local e os programas incluídos
// pelo painel (guardados no banco). Quem inclui ou exclui um programa troca o manifesto inteiro
// (Set); quem lê pega o retrato do momento (Get), sem trava e sem ver uma troca pela metade.
// O painel, o autoatendimento, a padronização, a avaliação do parque, as campanhas e o
// catálogo leem daqui: o programa incluído vale na hora, sem reiniciar o servidor.
type Live struct {
	p   atomic.Pointer[Manifest]
	gen atomic.Uint64
}

// NewLive começa com m.
func NewLive(m Manifest) *Live {
	l := &Live{}
	l.Set(m)
	return l
}

// Get devolve o manifesto de agora (vazio se nunca foi definido).
func (l *Live) Get() Manifest {
	if m := l.p.Load(); m != nil {
		return *m
	}
	return Manifest{}
}

// Set troca o manifesto inteiro.
func (l *Live) Set(m Manifest) {
	l.p.Store(&m)
	l.gen.Add(1)
}

// Gen conta as trocas: as avaliações guardadas com outra geração estão velhas.
func (l *Live) Gen() uint64 { return l.gen.Load() }

// For: as regras de um SO no manifesto de agora.
func (l *Live) For(osID string) []App { return l.Get().For(osID) }
