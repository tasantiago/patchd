package repocache

import (
	"net/url"
	"strings"
	"testing"
)

// Aula 20.3: o metalink do Fedora diz de quais espelhos o cache baixa. Para qualquer XML,
// nada de pânico; e cada espelho aceito é uma base https, terminada em "/", sem query nem
// fragmento, à qual o cache só acrescenta o caminho do arquivo.
func FuzzMetalink(f *testing.F) {
	h := strings.Repeat("a", 64)
	f.Add(`<metalink xmlns="http://www.metalinker.org/"><files><file name="repomd.xml"><timestamp>1791163517</timestamp>
<verification><hash type="sha256">` + h + `</hash></verification><alternates><alternate><verification><hash type="sha256">` +
		strings.Repeat("b", 64) + `</hash></verification></alternate></alternates><resources>
<url protocol="https">https://espelho/fedora/linux/updates/44/Everything/x86_64/repodata/repomd.xml</url></resources></file></files></metalink>`)
	f.Add(`<metalink><files><file name="repomd.xml"><verification><hash type="sha256">` + h +
		`</hash></verification><resources><url>https://e/repodata/repomd.xml?x=1</url><url>https://e/repodata/repomd.xml#</url>` +
		`<url>https://e/repodata/repomd.xml?</url><url>https://u@e/repodata/repomd.xml</url><url>https://e/b/repodata/repomd.xml</url>` +
		`</resources></file></files></metalink>`)
	f.Fuzz(func(t *testing.T, x string) {
		info, err := parseMetalink([]byte(x), false)
		if err != nil {
			return
		}
		if !info.allowed[info.current] || len(info.mirrors) == 0 {
			t.Fatalf("aceito sem o SHA atual ou sem espelhos: %+v", info)
		}
		for _, m := range info.mirrors {
			u, err := url.Parse(m)
			if err != nil || u.Scheme != "https" || u.Host == "" || !strings.HasSuffix(m, "/") || u.RawQuery != "" || u.Fragment != "" {
				t.Fatalf("espelho fora do formato: %q", m)
			}
			if r, err := url.Parse(m + "repodata/repomd.xml"); err != nil || r.Host != u.Host {
				t.Fatalf("o caminho acrescentado muda o servidor: %q", m)
			}
		}
	})
}
