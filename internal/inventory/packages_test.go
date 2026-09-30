package inventory

import (
	"strings"
	"testing"
)

// Linhas reais do Ubuntu 26.04 do laboratório (dpkg-query 1.23.7), mais estados sintéticos.
const dpkgReal = "ii \tbind9-host\t1:9.20.24-1ubuntu0.3\tamd64\tbind9\t1:9.20.24-1ubuntu0.3\n" +
	"ii \tlibc6\t2.43-2ubuntu2.4\tamd64\tglibc\t2.43-2ubuntu2.4\n" +
	"ii \tlibcares2\t1.34.6-1ubuntu0.1\tamd64\tc-ares\t1.34.6-1ubuntu0.1\n" +
	"ii \tlinux-image-7.0.0-14-generic\t7.0.0-14.14\tamd64\tlinux-signed\t7.0.0-14.14\n" +
	"ii \tlinux-image-7.0.0-34-generic\t7.0.0-34.34\tamd64\tlinux-signed\t7.0.0-34.34\n" +
	"un \tlinux-image-fb-generic-hwe-26.04\t\t\tlinux-image-fb-generic-hwe-26.04\t\n"

func TestParseDpkgQueryReal(t *testing.T) {
	list, err := parseDpkgQuery([]byte(dpkgReal))
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if len(list) != 5 {
		t.Fatalf("esperados 5 instalados (o 'un' fica fora), vieram %d: %+v", len(list), list)
	}
	porNome := map[string]Software{}
	for _, s := range list {
		porNome[s.Name] = s
	}
	c := porNome["libcares2"]
	if c.Version != "1.34.6-1ubuntu0.1" || c.SourcePackage != "c-ares" || c.SourcePackageVersion != "1.34.6-1ubuntu0.1" || c.Arch != "amd64" {
		t.Errorf("libcares2 errado: %+v", c)
	}
	if porNome["bind9-host"].Version != "1:9.20.24-1ubuntu0.3" {
		t.Errorf("a época deveria vir na versão: %+v", porNome["bind9-host"])
	}
	if porNome["libc6"].SourcePackage != "glibc" {
		t.Errorf("fonte do libc6 errado: %+v", porNome["libc6"])
	}
	if _, ok := porNome["linux-image-7.0.0-14-generic"]; !ok {
		t.Error("os dois kernels instalados deveriam aparecer")
	}
	if c.Scope != "machine" || c.Source != dpkgSource || c.Held {
		t.Errorf("escopo, fonte ou hold errados: %+v", c)
	}
}

func TestParseDpkgQueryEstados(t *testing.T) {
	dados := "hi \tpacote-travado\t1.0\tamd64\tpacote-travado\t1.0\n" +
		"rc \tpacote-removido\t2.0\tamd64\tpacote-removido\t2.0\n" +
		"iW \tpacote-gatilho\t3.0\tall\tpacote-gatilho\t3.0\n" +
		"iU \tpacote-quebrado\t4.0\tamd64\tpacote-quebrado\t4.0\n"
	list, err := parseDpkgQuery([]byte(dados))
	if len(list) != 2 || list[0].Name != "pacote-travado" || !list[0].Held || list[1].Name != "pacote-gatilho" {
		t.Errorf("esperados o travado (com hold) e o com gatilho pendente: %+v", list)
	}
	if err == nil || !strings.Contains(err.Error(), "pacote-quebrado") || !strings.Contains(err.Error(), "apt fica travado") {
		t.Errorf("o pacote quebrado deveria virar erro explicando o impacto: %v", err)
	}
}

func TestParseDpkgQueryLinhaInvalida(t *testing.T) {
	list, err := parseDpkgQuery([]byte("lixo sem tabs\nii \tok\t1\tamd64\tok\t1\n"))
	if len(list) != 1 || err == nil || !strings.Contains(err.Error(), "linha 1") {
		t.Errorf("a linha boa deveria entrar e a ruim virar erro: %+v, %v", list, err)
	}
}

// Linhas reais do Fedora 44 (rpm 6.0.2), mais o pseudo-pacote de chave GPG.
const rpmReal = "bash\t(none)\t5.3.9\t3.fc44\tx86_64\tbash-5.3.9-3.fc44.src.rpm\t1787640456\tFedora Project\n" +
	"curl\t(none)\t8.18.0\t8.fc44\tx86_64\tcurl-8.18.0-8.fc44.src.rpm\t1787640459\tFedora Project\n" +
	"openssl-libs\t1\t3.5.7\t2.fc44\tx86_64\topenssl-3.5.7-2.fc44.src.rpm\t1787640457\tFedora Project\n" +
	"tar\t2\t1.35\t8.fc44\tx86_64\ttar-1.35-8.fc44.src.rpm\t1787640460\tFedora Project\n" +
	"gpg-pubkey\t(none)\t36f612dcf27f7d1a48a835e4dbfcf71c6d9f90a6\t6786af3b\t(none)\t(none)\t1787640450\t(none)\n"

func TestParseRPMQueryReal(t *testing.T) {
	list, err := parseRPMQuery([]byte(rpmReal))
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if len(list) != 4 {
		t.Fatalf("esperados 4 pacotes (gpg-pubkey fora), vieram %d: %+v", len(list), list)
	}
	porNome := map[string]Software{}
	for _, s := range list {
		porNome[s.Name] = s
	}
	o := porNome["openssl-libs"]
	if o.Version != "1:3.5.7-2.fc44" || o.SourcePackage != "openssl" || o.SourcePackageVersion != "3.5.7-2.fc44" {
		t.Errorf("openssl-libs errado (época ou fonte): %+v", o)
	}
	if porNome["tar"].Version != "2:1.35-8.fc44" {
		t.Errorf("tar deveria ter época 2: %+v", porNome["tar"])
	}
	b := porNome["bash"]
	if b.Version != "5.3.9-3.fc44" || b.Publisher != "Fedora Project" || b.InstallDate != "2026-08-25" || b.Arch != "x86_64" {
		t.Errorf("bash errado: %+v", b)
	}
	if b.Source != rpmSource || b.Scope != "machine" {
		t.Errorf("fonte ou escopo errados: %+v", b)
	}
}

func TestParseSourceRPM(t *testing.T) {
	casos := []struct{ entrada, nome, versao string }{
		{"openssl-3.5.7-2.fc44.src.rpm", "openssl", "3.5.7-2.fc44"},
		{"python3-dnf-plugins-core-4.9.0-1.fc44.src.rpm", "python3-dnf-plugins-core", "4.9.0-1.fc44"},
		{"(none)", "", ""},
		{"", "", ""},
	}
	for _, c := range casos {
		n, v := parseSourceRPM(c.entrada)
		if n != c.nome || v != c.versao {
			t.Errorf("parseSourceRPM(%q) = (%q, %q), esperado (%q, %q)", c.entrada, n, v, c.nome, c.versao)
		}
	}
}

func TestPackageManagerFor(t *testing.T) {
	casos := []struct{ id, idLike, quer string }{
		{"ubuntu", "debian", "dpkg"},
		{"debian", "", "dpkg"},
		{"linuxmint", "ubuntu debian", "dpkg"},
		{"fedora", "", "rpm"},
		{"rhel", "fedora", "rpm"},
		{"rocky", "rhel centos fedora", "rpm"},
		{"opensuse-leap", "suse opensuse", ""},
		{"", "", ""},
	}
	for _, c := range casos {
		if got := packageManagerFor(c.id, c.idLike); got != c.quer {
			t.Errorf("packageManagerFor(%q, %q) = %q, esperado %q", c.id, c.idLike, got, c.quer)
		}
	}
}
