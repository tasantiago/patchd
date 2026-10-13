package winget

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// O winget-pkgs publica os manifestos em YAML, e o patchd não tem biblioteca de YAML. Este
// leitor cobre só o subconjunto que os manifestos de instalador usam, medido numa amostra
// de 1.509 manifestos reais (Aula 14.1):
//
//   - mapas e listas em bloco, inclusive a lista no mesmo recuo da chave ("Commands:\n- 7z");
//   - valores simples, entre aspas simples ('Let''s') e duplas, com escapes ("C:\\Temp");
//   - comentários, inclusive depois do valor ("InstallerType: exe # InstallShield");
//   - blocos | e > (raros: notas de versão), listas [a, b] e mapas { k: v } de uma linha;
//   - BOM, CRLF e o "---" do início.
//
// O que fica de fora é recusado com erro, em vez de adivinhado: âncoras (&, *), tags (!),
// valor simples em mais de uma linha, tabulação no recuo, mais de um documento.
//
// Todo valor sai como texto: a versão 26.10 não pode virar o número 26.1. O resultado é
// map[string]any, []any e string.

type yline struct {
	num    int // linha no arquivo, a partir de 1
	indent int
	text   string // sem o recuo; com comentário, se houver
}

type yparser struct {
	raw   []string // as linhas como estão (para os blocos | e >)
	lines []yline  // as linhas com conteúdo
	pos   int
}

func parseYAML(data []byte) (any, error) {
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("yaml: o arquivo não é UTF-8")
	}
	s := strings.TrimPrefix(string(data), "\ufeff")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	p := &yparser{raw: strings.Split(s, "\n")}
	for i, l := range p.raw {
		trimmed := strings.TrimLeft(l, " ")
		if strings.HasPrefix(trimmed, "\t") {
			return nil, fmt.Errorf("yaml: linha %d: tabulação no recuo", i+1)
		}
		trimmed = strings.TrimRight(trimmed, " \t")
		if trimmed == "" || trimmed[0] == '#' {
			continue
		}
		p.lines = append(p.lines, yline{num: i + 1, indent: len(l) - len(strings.TrimLeft(l, " ")), text: trimmed})
	}
	if len(p.lines) > 0 && p.lines[0].text == "---" && p.lines[0].indent == 0 {
		p.pos++
	}
	if p.pos >= len(p.lines) {
		return nil, fmt.Errorf("yaml: documento vazio")
	}
	if p.lines[p.pos].indent != 0 {
		return nil, fmt.Errorf("yaml: linha %d: o documento começa recuado", p.lines[p.pos].num)
	}
	v, err := p.node(0)
	if err != nil {
		return nil, err
	}
	if p.pos < len(p.lines) {
		l := p.lines[p.pos]
		if l.text == "---" || l.text == "..." {
			return nil, fmt.Errorf("yaml: linha %d: mais de um documento", l.num)
		}
		return nil, fmt.Errorf("yaml: linha %d: recuo inesperado", l.num)
	}
	return v, nil
}

func isSeqItem(text string) bool { return text == "-" || strings.HasPrefix(text, "- ") }

// node lê o mapa ou a lista que começa na linha atual, com esse recuo.
func (p *yparser) node(indent int) (any, error) {
	if isSeqItem(p.lines[p.pos].text) {
		return p.seq(indent)
	}
	return p.mapping(indent)
}

func (p *yparser) mapping(indent int) (map[string]any, error) {
	out := map[string]any{}
	for p.pos < len(p.lines) {
		l := p.lines[p.pos]
		if l.indent < indent {
			break
		}
		if l.indent > indent {
			return nil, fmt.Errorf("yaml: linha %d: recuo inesperado (valor simples em mais de uma linha não é aceito)", l.num)
		}
		if isSeqItem(l.text) {
			break // a lista que segue a chave no mesmo recuo é tratada por quem a abriu
		}
		if l.indent == 0 && (l.text == "---" || l.text == "...") {
			break // fim do documento: parseYAML recusa o que vier depois
		}
		key, rest, ok := splitKey(l.text)
		if !ok {
			return nil, fmt.Errorf("yaml: linha %d: esperava \"chave: valor\"", l.num)
		}
		if _, dup := out[key]; dup {
			return nil, fmt.Errorf("yaml: linha %d: chave %q repetida", l.num, key)
		}
		p.pos++
		v, err := p.value(l, indent, rest)
		if err != nil {
			return nil, err
		}
		out[key] = v
	}
	return out, nil
}

// value lê o valor de uma chave (ou de um item de lista): na mesma linha, num bloco | ou >,
// ou nas linhas seguintes.
func (p *yparser) value(l yline, indent int, rest string) (any, error) {
	if h := strings.TrimSpace(stripComment(rest)); h != "" && (h[0] == '|' || h[0] == '>') {
		return p.blockScalar(l, indent, h)
	}
	if rest != "" {
		return inline(rest, l.num)
	}
	if p.pos < len(p.lines) {
		next := p.lines[p.pos]
		if next.indent > indent || (next.indent == indent && isSeqItem(next.text)) {
			return p.node(next.indent)
		}
	}
	return "", nil // chave sem valor
}

func (p *yparser) seq(indent int) ([]any, error) {
	out := []any{}
	for p.pos < len(p.lines) {
		l := p.lines[p.pos]
		if l.indent != indent || !isSeqItem(l.text) {
			if l.indent > indent {
				return nil, fmt.Errorf("yaml: linha %d: recuo inesperado", l.num)
			}
			break
		}
		item := strings.TrimPrefix(strings.TrimPrefix(l.text, "-"), " ")
		item = strings.TrimLeft(item, " ")
		if item == "" {
			// "-" sozinho: o item vem nas linhas seguintes, mais recuado.
			p.pos++
			if p.pos >= len(p.lines) || p.lines[p.pos].indent <= indent {
				out = append(out, "")
				continue
			}
			v, err := p.node(p.lines[p.pos].indent)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
			continue
		}
		if _, _, isMap := splitKey(item); isMap {
			// "- Architecture: x64": um mapa cujas chaves ficam no recuo do texto depois do
			// traço. A linha passa a ser a primeira chave desse mapa.
			col := indent + (len(l.text) - len(item))
			p.lines[p.pos] = yline{num: l.num, indent: col, text: item}
			v, err := p.mapping(col)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
			continue
		}
		p.pos++
		if h := strings.TrimSpace(stripComment(item)); h != "" && (h[0] == '|' || h[0] == '>') {
			v, err := p.blockScalar(l, indent, h)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
			continue
		}
		v, err := inline(item, l.num)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// splitKey separa "chave: resto". A chave é simples (os manifestos não usam chave entre
// aspas); o separador é ":" seguido de espaço ou do fim da linha.
func splitKey(text string) (key, rest string, ok bool) {
	if text == "" || !isKeyStart(text[0]) {
		return "", "", false
	}
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c == ' ' || c == '#' {
			return "", "", false // "texto com espaço: ..." não é chave do winget
		}
		if c == ':' && (i+1 == len(text) || text[i+1] == ' ') {
			if i == 0 {
				return "", "", false
			}
			return text[:i], strings.TrimSpace(text[i+1:]), true
		}
	}
	return "", "", false
}

// isKeyStart: as chaves do winget são palavras (InstallerType, Architecture).
func isKeyStart(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
}

// stripComment tira o comentário de um valor simples (" #" fora de aspas). Valores entre
// aspas são tratados por inline.
func stripComment(s string) string {
	if s == "" || s[0] == '"' || s[0] == '\'' {
		return s
	}
	if s[0] == '#' {
		return ""
	}
	if i := strings.Index(s, " #"); i >= 0 {
		return strings.TrimRight(s[:i], " ")
	}
	return s
}

// inline lê um valor de uma linha: entre aspas, lista [a, b], mapa { k: v } ou simples.
func inline(s string, num int) (any, error) {
	switch s[0] {
	case '"', '\'':
		v, rest, err := quoted(s, num)
		if err != nil {
			return nil, err
		}
		if r := strings.TrimSpace(rest); r != "" && r[0] != '#' {
			return nil, fmt.Errorf("yaml: linha %d: texto depois das aspas", num)
		}
		return v, nil
	case '[', '{':
		return flow(stripComment(s), num)
	case '&', '*':
		return nil, fmt.Errorf("yaml: linha %d: âncoras e referências (& e *) não são aceitas", num)
	case '!':
		return nil, fmt.Errorf("yaml: linha %d: tags (!) não são aceitas", num)
	}
	return stripComment(s), nil
}

// quoted lê um texto entre aspas e devolve o que sobra da linha.
func quoted(s string, num int) (string, string, error) {
	q := s[0]
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		c := s[i]
		if q == '\'' {
			if c == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					b.WriteByte('\'')
					i++
					continue
				}
				return b.String(), s[i+1:], nil
			}
			b.WriteByte(c)
			continue
		}
		switch c {
		case '"':
			return b.String(), s[i+1:], nil
		case '\\':
			if i+1 >= len(s) {
				return "", "", fmt.Errorf("yaml: linha %d: escape no fim da linha", num)
			}
			i++
			switch e := s[i]; e {
			case '\\', '"', '/':
				b.WriteByte(e)
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case '0':
				b.WriteByte(0)
			case ' ':
				b.WriteByte(' ')
			case 'u', 'U', 'x':
				n := map[byte]int{'x': 2, 'u': 4, 'U': 8}[e]
				if i+n >= len(s) {
					return "", "", fmt.Errorf("yaml: linha %d: escape \\%c incompleto", num, e)
				}
				r, err := strconv.ParseUint(s[i+1:i+1+n], 16, 32)
				if err != nil {
					return "", "", fmt.Errorf("yaml: linha %d: escape \\%c inválido", num, e)
				}
				b.WriteRune(rune(r))
				i += n
			default:
				return "", "", fmt.Errorf("yaml: linha %d: escape \\%c desconhecido", num, e)
			}
		default:
			b.WriteByte(c)
		}
	}
	return "", "", fmt.Errorf("yaml: linha %d: aspas sem fechamento (texto em mais de uma linha não é aceito)", num)
}

// flow lê [a, b] e { k: v, k2: v2 } de uma linha, com itens simples ou entre aspas.
func flow(s string, num int) (any, error) {
	open, closing := s[0], map[byte]byte{'[': ']', '{': '}'}[s[0]]
	if s[len(s)-1] != closing {
		return nil, fmt.Errorf("yaml: linha %d: %c sem fechamento na mesma linha", num, open)
	}
	body := strings.TrimSpace(s[1 : len(s)-1])
	var items []string
	for body != "" {
		var item string
		if body[0] == '"' || body[0] == '\'' {
			v, rest, err := quoted(body, num)
			if err != nil {
				return nil, err
			}
			item, body = "\x00"+v, strings.TrimSpace(rest) // \x00: já sem aspas
		} else if i := strings.IndexByte(body, ','); i >= 0 {
			item, body = strings.TrimSpace(body[:i]), body[i:]
		} else {
			item, body = body, ""
		}
		if strings.ContainsAny(item[:min(1, len(item))], "[]{}&*!") {
			return nil, fmt.Errorf("yaml: linha %d: estrutura dentro de %c...%c não é aceita", num, open, closing)
		}
		items = append(items, item)
		body = strings.TrimSpace(body)
		if body != "" {
			if body[0] != ',' {
				return nil, fmt.Errorf("yaml: linha %d: esperava vírgula em %c...%c", num, open, closing)
			}
			body = strings.TrimSpace(body[1:])
		}
	}
	unq := func(s string) string { return strings.TrimPrefix(s, "\x00") }
	if open == '[' {
		out := make([]any, 0, len(items))
		for _, it := range items {
			out = append(out, unq(it))
		}
		return out, nil
	}
	out := map[string]any{}
	for _, it := range items {
		k, v, ok := strings.Cut(it, ":")
		if !ok || strings.HasPrefix(it, "\x00") {
			return nil, fmt.Errorf("yaml: linha %d: item %q do mapa sem \"chave: valor\"", num, it)
		}
		v = strings.TrimSpace(v)
		if v != "" && (v[0] == '"' || v[0] == '\'') {
			q, rest, err := quoted(v, num)
			if err != nil || strings.TrimSpace(rest) != "" {
				return nil, fmt.Errorf("yaml: linha %d: valor %q inválido", num, v)
			}
			v = q
		}
		out[strings.TrimSpace(k)] = v
	}
	return out, nil
}

// blockScalar lê um bloco | (literal) ou > (dobrado), com o indicador de final - ou +.
// O conteúdo são as linhas seguintes mais recuadas que a chave, ou em branco.
func (p *yparser) blockScalar(l yline, indent int, header string) (string, error) {
	style, chomp := header[0], header[1:]
	if chomp != "" && chomp != "-" && chomp != "+" {
		return "", fmt.Errorf("yaml: linha %d: bloco %q não é aceito (só |, |-, |+, >, >- e >+)", l.num, header)
	}
	var body []string
	start := l.num // índice em raw da linha seguinte (num é 1-based)
	blockIndent := -1
	i := start
	for ; i < len(p.raw); i++ {
		r := p.raw[i]
		if strings.TrimSpace(r) == "" {
			body = append(body, "")
			continue
		}
		ind := len(r) - len(strings.TrimLeft(r, " "))
		if ind <= indent {
			break
		}
		if blockIndent < 0 {
			blockIndent = ind
		}
		if ind < blockIndent {
			return "", fmt.Errorf("yaml: linha %d: recuo menor que o da primeira linha do bloco", i+1)
		}
		body = append(body, strings.TrimRight(r[blockIndent:], " "))
	}
	// As linhas de conteúdo consumidas saem da lista de linhas com conteúdo.
	for p.pos < len(p.lines) && p.lines[p.pos].num <= i {
		p.pos++
	}
	// Linhas em branco do fim: separadas, para o indicador de final.
	trail := 0
	for len(body) > 0 && body[len(body)-1] == "" {
		body = body[:len(body)-1]
		trail++
	}
	var text string
	if style == '|' {
		text = strings.Join(body, "\n")
	} else {
		var b strings.Builder
		// Linhas seguidas viram uma, separadas por espaço; cada linha em branco vira uma
		// quebra.
		for j, line := range body {
			switch {
			case line == "":
				b.WriteByte('\n')
			case j > 0 && body[j-1] != "":
				b.WriteByte(' ')
			}
			b.WriteString(line)
		}
		text = b.String()
	}
	switch {
	case len(body) == 0:
		return "", nil
	case chomp == "-":
		return text, nil
	case chomp == "+":
		return text + strings.Repeat("\n", trail+1), nil
	}
	return text + "\n", nil
}
