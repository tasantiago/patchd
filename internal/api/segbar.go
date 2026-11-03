package api

import (
	"fmt"
	"sort"
	"strings"
)

// Barras segmentadas da Visão geral: um todo (as máquinas, os jobs) dividido em partes, com a
// largura de cada parte proporcional ao valor. Só HTML e CSS (a CSP do painel não tem script nem
// estilo inline): a largura vem das classes .w0 a .w100 (estilo.go), e a dica de cada parte
// aparece com o mouse em cima ou com o foco do teclado.

// segPart é uma parte da barra.
type segPart struct {
	Label, Class, Href string
	N, Pct             int
	W                  int    // a classe de largura (w0..w100)
	Inline             bool   // cabe o rótulo dentro do bloco
	Align              string // de que lado a dica abre: "meio", ou "inicio" e "fim" nas pontas (não sai da página)
	Tip                string // a dica: nome, quantidade e parte do todo
}

// segBar é a barra inteira.
type segBar struct {
	Size  string // "alta" (com rótulos dentro) ou "fina"
	Parts []segPart
	Total int
	Label string // o nome acessível da barra inteira
	W     int    // a largura da barra (w100, ou menos para comparar volumes entre linhas)
}

// segMinInline: abaixo disso (em %), o rótulo não cabe no bloco e fica só na dica.
const segMinInline = 9

// makeSegBar monta a barra. unit: o substantivo da dica ("máquina", "job"). As porcentagens
// somam 100 (maiores restos), para os blocos fecharem a barra.
func makeSegBar(size, unit string, parts []segPart) segBar {
	b := segBar{Size: size, W: 100}
	for _, p := range parts {
		b.Total += p.N
	}
	if b.Total == 0 {
		return b
	}
	// A largura fecha 100% (maiores restos); o número mostrado é o mesmo das outras telas
	// (pct, arredondado para baixo), para a barra e o número grande nunca discordarem.
	type rest struct{ i, r int }
	var rests []rest
	width := make([]int, len(parts))
	sum := 0
	for i := range parts {
		parts[i].Pct = pct(parts[i].N, b.Total)
		width[i] = parts[i].Pct
		sum += width[i]
		rests = append(rests, rest{i, parts[i].N * 100 % b.Total})
	}
	sort.SliceStable(rests, func(a, c int) bool { return rests[a].r > rests[c].r })
	for k := 0; sum < 100 && k < len(rests); k++ {
		if parts[rests[k].i].N > 0 {
			width[rests[k].i]++
			sum++
		}
	}
	for i := range parts {
		parts[i].W = width[i]
	}
	var label []string
	start := 0
	for i := range parts {
		mid := start + parts[i].W/2
		start += parts[i].W
		switch {
		case mid < 20:
			parts[i].Align = "inicio"
		case mid > 80:
			parts[i].Align = "fim"
		default:
			parts[i].Align = "meio"
		}
	}
	for _, p := range parts {
		if p.N == 0 {
			continue // parte em zero não desenha nada
		}
		p.Inline = size == "alta" && p.Pct >= segMinInline
		p.Tip = fmt.Sprintf("%s: %d %s (%d%%)", p.Label, p.N, plural(p.N, unit, unit+"s"), p.Pct)
		b.Parts = append(b.Parts, p)
		label = append(label, p.Tip)
	}
	b.Label = strings.Join(label, "; ")
	return b
}

// Os estados de compliance na barra, na ordem do melhor para o pior (a cor segue o estado).
type ovState struct{ Key, Label, Class string }

var ovStates = []ovState{
	{"em_dia", "Em dia", "st-ok"},
	{"faltando", "Faltando", "st-falta"},
	{"reboot_pendente", "Reinício pendente", "st-reinicio"},
	{"sem_dado_recente", "Sem contato", "st-sem"},
	{"desconhecido", "Desconhecido", "st-desc"},
}

// stateBar: a barra de estados de um conjunto de máquinas. link: a parte vira link para a frota
// filtrada (só na barra da frota inteira: a Frota não filtra por anel nem por sistema).
func stateBar(size string, counts map[string]int, link bool) segBar {
	parts := make([]segPart, 0, len(ovStates))
	for _, s := range ovStates {
		p := segPart{Label: s.Label, Class: s.Class, N: counts[s.Key]}
		if link {
			p.Href = "/painel/parque?estado=" + s.Key
		}
		parts = append(parts, p)
	}
	return makeSegBar(size, "máquina", parts)
}
