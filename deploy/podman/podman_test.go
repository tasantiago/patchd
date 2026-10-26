// Package podman só tem testes: confere que as unidades do Quadlet (Aula 23.1) não se
// afastam do deploy/compose.yaml, que continua sendo a descrição de referência do serviço.
// Uma variável nova no compose sem par no instalar.sh, um segredo novo sem montagem ou outra
// imagem do Postgres fariam o servidor do Podman diferente do servidor do compose em silêncio.
package podman

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// composeVar é uma variável do servidor no compose.yaml e a forma como ela recebe o valor.
type composeVar struct {
	kind string // fixo, padrao (${X:-p}), ausente (${X-p}) ou obrigatoria (${X:?})
	val  string // o valor fixo ou o padrão
}

var (
	reInterp = regexp.MustCompile(`^\$\{([A-Z0-9_]+)(:-|-|:\?)(.*)\}$`)
	reEnvKey = regexp.MustCompile(`^      ([A-Z0-9_]+): ?(.*)$`)
	// O próximo serviço ("  nome:") ou bloco de primeiro nível ("secrets:").
	reNextBlock = regexp.MustCompile(`^( {2})?[a-z]`)
)

// service devolve as linhas de um serviço do compose.yaml (de "  nome:" até o próximo
// serviço ou bloco de primeiro nível).
func service(t *testing.T, name string) []string {
	t.Helper()
	b, err := os.ReadFile("../compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	in := false
	for _, l := range strings.Split(string(b), "\n") {
		if l == "  "+name+":" {
			in = true
			continue
		}
		if in && reNextBlock.MatchString(l) {
			break
		}
		if in {
			out = append(out, l)
		}
	}
	if len(out) == 0 {
		t.Fatalf("serviço %s não encontrado no compose.yaml", name)
	}
	return out
}

// block devolve os itens de um bloco do serviço ("    environment:", "    secrets:"...),
// sem os comentários.
func block(lines []string, name string) []string {
	var out []string
	in := false
	for _, l := range lines {
		if l == "    "+name+":" {
			in = true
			continue
		}
		if !in || strings.HasPrefix(strings.TrimSpace(l), "#") || strings.TrimSpace(l) == "" {
			continue
		}
		if !strings.HasPrefix(l, "      ") {
			break
		}
		out = append(out, l)
	}
	return out
}

func composeServerEnv(t *testing.T) map[string]composeVar {
	t.Helper()
	vars := map[string]composeVar{}
	for _, l := range block(service(t, "server"), "environment") {
		m := reEnvKey.FindStringSubmatch(l)
		if m == nil {
			t.Fatalf("linha de environment fora do formato: %q", l)
		}
		key, v := m[1], m[2]
		if im := reInterp.FindStringSubmatch(v); im != nil {
			if im[1] != key {
				t.Fatalf("%s vem de outra variável (%s): o instalar.sh não sabe fazer isso", key, im[1])
			}
			kind := map[string]string{":-": "padrao", "-": "ausente", ":?": "obrigatoria"}[im[2]]
			vars[key] = composeVar{kind, im[3]}
			continue
		}
		if strings.Contains(v, "$") {
			t.Fatalf("%s: interpolação que o teste não conhece: %q", key, v)
		}
		vars[key] = composeVar{"fixo", strings.Trim(v, `"`)}
	}
	if len(vars) < 20 {
		t.Fatalf("só %d variáveis no environment do servidor: o formato do compose.yaml mudou?", len(vars))
	}
	return vars
}

// runEnv roda o instalar.sh --env numa cópia do deploy/ com o .env dado, sem nenhuma
// variável do shell de quem roda o teste.
func runEnv(t *testing.T, dotenv string) (map[string]string, string, error) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("sem bash")
	}
	dir := t.TempDir()
	for src, dst := range map[string]string{"instalar.sh": "deploy/podman/instalar.sh", "../comum.sh": "deploy/comum.sh"} {
		b, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(dst)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, dst), b, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "deploy/.env"), []byte(dotenv), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bash, filepath.Join(dir, "deploy/podman/instalar.sh"), "--env")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	got := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			t.Fatalf("linha sem = na saída: %q", sc.Text())
		}
		got[k] = v
	}
	return got, stderr.String(), err
}

const publicURL = "https://patchd.exemplo:8443"

var proxies = map[string]bool{"HTTPS_PROXY": true, "HTTP_PROXY": true, "NO_PROXY": true}

func TestEnvSameKeysAsCompose(t *testing.T) {
	want := composeServerEnv(t)
	got, stderr, err := runEnv(t, "PATCHD_PUBLIC_URL="+publicURL+"\n")
	if err != nil {
		t.Fatalf("instalar.sh --env: %v\n%s", err, stderr)
	}
	var missing, extra []string
	for k := range want {
		if _, ok := got[k]; !ok {
			missing = append(missing, k)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok && !proxies[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 || len(extra) > 0 {
		t.Fatalf("variáveis do servidor diferentes do compose.yaml: faltam %v; sobram %v", missing, extra)
	}
}

func TestEnvDefaultsLikeCompose(t *testing.T) {
	want := composeServerEnv(t)
	// Só a obrigatória no .env: cada variável fica com o padrão do compose.
	got, stderr, err := runEnv(t, "PATCHD_PUBLIC_URL="+publicURL+"\n")
	if err != nil {
		t.Fatalf("instalar.sh --env: %v\n%s", err, stderr)
	}
	for k, w := range want {
		exp := w.val
		if w.kind == "obrigatoria" {
			exp = publicURL
		}
		if got[k] != exp {
			t.Errorf("%s (%s) sem nada no .env: %q, o compose daria %q", k, w.kind, got[k], exp)
		}
	}

	// Todas presentes e vazias: ":-" volta ao padrão; "-" fica vazia; o fixo não muda.
	var b strings.Builder
	b.WriteString("PATCHD_PUBLIC_URL=" + publicURL + "\n")
	for k, w := range want {
		if w.kind == "padrao" || w.kind == "ausente" {
			b.WriteString(k + "=\n")
		}
	}
	got, stderr, err = runEnv(t, b.String())
	if err != nil {
		t.Fatalf("instalar.sh --env: %v\n%s", err, stderr)
	}
	for k, w := range want {
		exp := map[string]string{"padrao": w.val, "ausente": "", "fixo": w.val, "obrigatoria": publicURL}[w.kind]
		if got[k] != exp {
			t.Errorf("%s (%s) vazia no .env: %q, o compose daria %q", k, w.kind, got[k], exp)
		}
	}
}

func TestEnvValuesFromDotenv(t *testing.T) {
	want := composeServerEnv(t)
	var b strings.Builder
	b.WriteString("# comentário\nPATCHD_PUBLIC_URL=\"" + publicURL + "\"\n")
	i := 0
	for k, w := range want {
		if w.kind == "obrigatoria" {
			continue
		}
		if w.kind == "fixo" {
			// O .env não muda um valor fixo do compose.
			b.WriteString(k + "=outro-valor\n")
			continue
		}
		v := "valor-de-" + k
		switch i % 3 {
		case 0:
			b.WriteString(k + "=" + v + "\n")
		case 1:
			b.WriteString(k + "=\"" + v + "\"\n")
		default:
			b.WriteString(k + "='" + v + "'\r\n") // aspas simples e fim de linha do Windows
		}
		i++
	}
	b.WriteString("PATCHD_VERSION=v0.0.1\nPATCHD_MOTOR=podman\nHTTPS_PROXY=http://proxy.exemplo:3128\nNO_PROXY=.exemplo\n")
	got, stderr, err := runEnv(t, b.String())
	if err != nil {
		t.Fatalf("instalar.sh --env: %v\n%s", err, stderr)
	}
	for k, w := range want {
		exp := "valor-de-" + k
		switch w.kind {
		case "fixo":
			exp = w.val
		case "obrigatoria":
			exp = publicURL
		}
		if got[k] != exp {
			t.Errorf("%s (%s): %q, esperado %q", k, w.kind, got[k], exp)
		}
	}
	if got["HTTPS_PROXY"] != "http://proxy.exemplo:3128" || got["NO_PROXY"] != ".exemplo" {
		t.Errorf("proxy não repassado: %q %q", got["HTTPS_PROXY"], got["NO_PROXY"])
	}
	if _, ok := got["HTTP_PROXY"]; ok {
		t.Error("HTTP_PROXY ausente no .env apareceu na saída")
	}
	for _, k := range []string{"PATCHD_VERSION", "PATCHD_MOTOR"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s é do deploy, não do servidor, e apareceu na saída", k)
		}
	}
}

func TestEnvRequiresPublicURL(t *testing.T) {
	_, stderr, err := runEnv(t, "PATCHD_LOG_LEVEL=debug\n")
	if err == nil || !strings.Contains(stderr, "PATCHD_PUBLIC_URL") {
		t.Fatalf("sem PATCHD_PUBLIC_URL: err=%v stderr=%q", err, stderr)
	}
}

func readFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// secretMounts devolve os segredos X montados pela unidade em /run/secrets/X.
func secretMounts(unit string) []string {
	re := regexp.MustCompile(`(?m)^Volume=@SECRETS@/([a-z_]+):/run/secrets/([a-z_]+):ro,Z$`)
	var out []string
	for _, m := range re.FindAllStringSubmatch(unit, -1) {
		if m[1] == m[2] {
			out = append(out, m[1])
		}
	}
	sort.Strings(out)
	return out
}

func composeSecrets(t *testing.T, svc string) []string {
	var out []string
	for _, l := range block(service(t, svc), "secrets") {
		out = append(out, strings.TrimPrefix(strings.TrimSpace(l), "- "))
	}
	sort.Strings(out)
	return out
}

func TestSecretsMountedLikeCompose(t *testing.T) {
	for unit, svc := range map[string]string{"patchd-server.container": "server", "patchd-postgres.container": "postgres"} {
		got := strings.Join(secretMounts(readFile(t, unit)), " ")
		want := strings.Join(composeSecrets(t, svc), " ")
		if got != want {
			t.Errorf("%s monta os segredos [%s]; o compose dá ao %s [%s]", unit, got, svc, want)
		}
	}
}

func TestPostgresImageLikeCompose(t *testing.T) {
	var composeImg string
	for _, l := range service(t, "postgres") {
		if v, ok := strings.CutPrefix(l, "    image: "); ok {
			composeImg = v
		}
	}
	m := regexp.MustCompile(`(?m)^Image=(.+)$`).FindStringSubmatch(readFile(t, "patchd-postgres.container"))
	if m == nil || m[1] != composeImg {
		t.Fatalf("imagem do Postgres: unidade %v, compose %q", m, composeImg)
	}
	if strings.Count(m[1], "/") < 2 {
		t.Errorf("imagem %q sem o registro: o Podman do RHEL não completa nomes curtos num serviço", m[1])
	}
}

func TestTemplateMarkersAreFilled(t *testing.T) {
	inst := readFile(t, "instalar.sh")
	filled := map[string]bool{}
	for _, m := range regexp.MustCompile(`s\|@([A-Z_]+)@\|`).FindAllStringSubmatch(inst, -1) {
		filled[m[1]] = true
	}
	files, err := filepath.Glob("patchd*")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		for _, m := range regexp.MustCompile(`@([A-Z_]+)@`).FindAllStringSubmatch(readFile(t, f), -1) {
			if !filled[m[1]] {
				t.Errorf("%s: @%s@ não é trocado pelo instalar.sh", f, m[1])
			}
		}
		if !strings.Contains(inst, f) {
			t.Errorf("%s não é copiado pelo instalar.sh", f)
		}
	}
}

// O preparar-vm.sh (Aula 23.1b) roda como root numa VM: os argumentos são conferidos antes
// de qualquer coisa, e um caminho no /home ou uma porta baixa param ali, sem tocar no sistema.
func TestPrepararVMRecusaArgumentos(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("sem bash")
	}
	for _, args := range [][]string{
		{"-d", "/home/patchd/dados"},
		{"-d", "relativo"},
		{"-d", "/srv/com espaço"},
		{"-p", "443"},
		{"-p", "porta"},
		{"-u", "Maiusculo"},
		{"-u"},
		{"--desconhecida"},
	} {
		cmd := exec.Command(bash, append([]string{"preparar-vm.sh"}, args...)...)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
		out, err := cmd.CombinedOutput()
		ee, ok := err.(*exec.ExitError)
		if !ok || ee.ExitCode() != 2 {
			t.Errorf("preparar-vm.sh %v: %v, esperado exit 2\n%s", args, err, out)
		}
	}
}

// O agente.sh (Aula 23.2) usa a imagem do Go do deploy/Dockerfile: o agente e o servidor saem
// do mesmo Go. A linha FROM tem de continuar no formato que ele lê.
func TestAgenteUsaOGoDoDockerfile(t *testing.T) {
	m := regexp.MustCompile(`(?m)^FROM (docker\.io/library/golang:\S+)`).FindStringSubmatch(readFile(t, "../Dockerfile"))
	if m == nil {
		t.Fatal("deploy/Dockerfile sem FROM docker.io/library/golang:VERSÃO: o agente.sh não acha a imagem do Go")
	}
	if !strings.Contains(readFile(t, "../agente.sh"), `s/^FROM \(docker\.io\/library\/golang:[^ ]*\).*/\1/p`) {
		t.Error("o agente.sh não lê mais a imagem do Go do deploy/Dockerfile")
	}
}

// Fora do Podman, o agente.sh para antes de tudo: no compose, o caminho é o de sempre.
func TestAgenteSoNoPodman(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("sem bash")
	}
	dir := t.TempDir()
	for src, dst := range map[string]string{"../agente.sh": "deploy/agente.sh", "../comum.sh": "deploy/comum.sh"} {
		b, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(dst)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, dst), b, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "deploy/.env"), []byte("PATCHD_MOTOR=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bash, filepath.Join(dir, "deploy/agente.sh"), "v1.0.0", "-key", "/dev/null")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir}
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "scripts/build.sh") {
		t.Fatalf("agente.sh no compose: err=%v\n%s", err, out)
	}
}
