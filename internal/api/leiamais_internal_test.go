package api

import (
	"strings"
	"testing"
)

func TestLeiaMais(t *testing.T) {
	if v := leiaMais("curto"); v.Long || v.Full != "curto" {
		t.Errorf("curto: %+v", v)
	}
	var jobs []string
	for i := 0; i < 247; i++ {
		jobs = append(jobs, "job 7978 expirado")
	}
	d := "grupo de implantação 1: 247 job(s) sem sucesso (231 expirado, 14 rejeitado, 2 falhou); retome (campaign resume) para aceitar e seguir. Jobs: " + strings.Join(jobs, ", ")
	v := leiaMais(d)
	if !v.Long || v.Full != d || v.Short != "grupo de implantação 1: 247 job(s) sem sucesso (231 expirado, 14 rejeitado, 2 falhou); retome (campaign resume) para aceitar e seguir. …" {
		t.Errorf("longo: %q", v.Short)
	}
	var b strings.Builder
	if err := pages.ExecuteTemplate(&b, "leia-mais", v); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), `<details class="leia-mais"><summary><span class="curto">grupo`) || !strings.Contains(b.String(), `<span class="mais">leia mais</span>`) {
		t.Errorf("html: %.300s", b.String())
	}
}
