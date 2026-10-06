package kev

import "strings"

// PackageRule liga um produto do KEV aos pacotes fonte do Fedora que o empacotam.
//
// Existe porque o Bodhi quase nunca diz qual CVE um update corrige (Aula 6.2, parte 3:
// 39% a 45% dos updates de segurança sem CVE nenhuma; 10 CVEs do KEV no Fedora contra 148
// no Ubuntu). Com a regra, um update de segurança de um pacote que tem CVE no KEV pode ser
// apontado como o que provavelmente a corrige, sempre marcado como "inferido".
//
// A tabela é curta e revisada à mão, a partir dos nomes reais do KEV (vendorProject e
// product). Produto que não existe como pacote no Fedora (Zimbra, GitLab, Jenkins) fica de
// fora. Conferir cada pacote no catálogo do Fedora antes de acrescentar uma regra.
type PackageRule struct {
	Vendor  string // vendorProject, exato
	Product string // product: começa com este texto (vazio: qualquer produto do fabricante)
	Not     string // product não pode conter este texto (vazio: sem exclusão)
	Package string // pacote fonte do Fedora
}

// FedoraRules é a tabela de produtos do KEV para pacotes fonte do Fedora.
var FedoraRules = []PackageRule{
	{Vendor: "Google", Product: "Chromium", Package: "chromium"},
	{Vendor: "Google", Product: "Chrome", Not: "Android", Package: "chromium"},
	{Vendor: "Mozilla", Product: "Firefox", Package: "firefox"},
	{Vendor: "Mozilla", Product: "Multiple Products", Package: "firefox"},
	{Vendor: "Mozilla", Product: "Thunderbird", Package: "thunderbird"},
	{Vendor: "Mozilla", Product: "Firefox and Thunderbird", Package: "thunderbird"},
	{Vendor: "Mozilla", Product: "Firefox, Firefox ESR, and Thunderbird", Package: "thunderbird"},
	{Vendor: "Mozilla", Product: "Multiple Products", Package: "thunderbird"},
	{Vendor: "Linux", Product: "Kernel", Package: "kernel"},
	{Vendor: "Apache", Product: "HTTP Server", Package: "httpd"},
	{Vendor: "Apache", Product: "Log4j", Package: "log4j"},
	{Vendor: "Artifex", Product: "Ghostscript", Package: "ghostscript"},
	{Vendor: "Exim", Package: "exim"},
	{Vendor: "GNU", Product: "Bourne-Again Shell", Package: "bash"},
	{Vendor: "GNU", Product: "GNU Bash", Package: "bash"},
	{Vendor: "GNU", Product: "GNU C Library", Package: "glibc"},
	{Vendor: "Git", Product: "Git", Package: "git"},
	{Vendor: "ImageMagick", Package: "ImageMagick"},
	{Vendor: "OpenSSL", Product: "OpenSSL", Package: "openssl"},
	{Vendor: "Red Hat", Product: "Polkit", Package: "polkit"},
	{Vendor: "Samba", Product: "Samba", Package: "samba"},
	{Vendor: "Sudo", Product: "Sudo", Package: "sudo"},
	{Vendor: "WebKitGTK", Package: "webkitgtk"},
	{Vendor: "Roundcube", Package: "roundcubemail"},
	{Vendor: "Perl", Product: "Exiftool", Package: "perl-Image-ExifTool"},
}

// Match diz se a regra vale para o fabricante e o produto do KEV.
func (r PackageRule) Match(vendor, product string) bool {
	if vendor != r.Vendor {
		return false
	}
	p := strings.TrimSpace(product)
	if r.Product != "" && !strings.HasPrefix(p, r.Product) {
		return false
	}
	return r.Not == "" || !strings.Contains(p, r.Not)
}

// FedoraPackages devolve os pacotes fonte do Fedora de um item do KEV, sem repetir.
func FedoraPackages(vendor, product string) []string {
	var out []string
	seen := map[string]bool{}
	for _, r := range FedoraRules {
		if r.Match(vendor, product) && !seen[r.Package] {
			seen[r.Package] = true
			out = append(out, r.Package)
		}
	}
	return out
}
