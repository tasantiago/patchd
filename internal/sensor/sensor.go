// Package sensor responde as perguntas ao parque (jobs.TypeQuery) no agente: um catálogo
// fechado de leituras (arquivo, processo, serviço, registro, programa). Nada aqui escreve,
// executa programa do job ou sai da máquina; o argumento já veio conferido (jobs.QueryParams).
package sensor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"strings"

	"github.com/tasantiago/patchd/internal/jobs"
	"github.com/tasantiago/patchd/internal/protocol"
)

// NotApplicable: o sensor não existe neste sistema.
const NotApplicable = "não se aplica neste sistema"

// maxHash: até este tamanho, o arquivo vai com o SHA-256 (ler 2 GB por pergunta, não).
const maxHash = 100 << 20

// Process: um processo em execução.
type Process struct {
	Name    string
	Session uint32
}

// Sensors são as leituras que dependem do sistema; nil: não se aplica.
type Sensors struct {
	Processes func() ([]Process, error)
	Software  func(ctx context.Context) ([]protocol.Software, error)
	// Service: o estado e o início do serviço; found false: não existe.
	Service func(ctx context.Context, name string) (state, start string, found bool, err error)
	// Registry: o valor (já em texto); keyFound/valueFound dizem o que faltou.
	Registry func(path, name string) (value string, keyFound, valueFound bool, err error)
	// FileVersion: a versão do arquivo (Windows); "" sem versão.
	FileVersion func(path string) string
	// Expand troca %ProgramFiles% e as outras pastas conhecidas pelo caminho real.
	Expand func(path string) string
	// CaseInsensitive: nomes de processo sem diferença de maiúsculas (Windows).
	CaseInsensitive bool
}

// Answer responde a pergunta.
func (s Sensors) Answer(ctx context.Context, p jobs.QueryParams) jobs.QueryAnswer {
	var a jobs.QueryAnswer
	switch p.Sensor {
	case jobs.SensorFile:
		a = s.file(p.Arg)
	case jobs.SensorProcess:
		a = s.process(p.Arg)
	case jobs.SensorService:
		a = s.service(ctx, p.Arg)
	case jobs.SensorRegistry:
		a = s.registry(p.Arg, p.Name)
	case jobs.SensorProgram:
		a = s.program(ctx, p.Arg)
	default:
		a = jobs.QueryAnswer{Value: "sensor desconhecido"}
	}
	return a.Clip()
}

func (s Sensors) file(arg string) jobs.QueryAnswer {
	path := arg
	if s.Expand != nil {
		path = s.Expand(arg)
	}
	if strings.Contains(path, "%") {
		return jobs.QueryAnswer{Value: NotApplicable, Extra: "pasta desconhecida neste sistema: " + arg}
	}
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return jobs.QueryAnswer{Value: "não existe"}
	case err != nil:
		return jobs.QueryAnswer{Value: "erro ao ler", Extra: err.Error()}
	case info.IsDir():
		return jobs.QueryAnswer{Value: "é uma pasta"}
	}
	extra := []string{fmt.Sprintf("%d bytes", info.Size()), "modificado em " + info.ModTime().UTC().Format("2006-01-02 15:04") + " UTC"}
	if info.Size() <= maxHash {
		if sum, err := hashFile(path); err == nil {
			extra = append(extra, "sha256 "+sum)
		}
	}
	value := "existe"
	if s.FileVersion != nil {
		if v := s.FileVersion(path); v != "" {
			value = v
		}
	}
	return jobs.QueryAnswer{Value: value, Extra: strings.Join(extra, "; ")}
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path) // #nosec G304 -- pergunta assinada, caminho conferido; só leitura, para o hash
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, maxHash+1)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (s Sensors) process(name string) jobs.QueryAnswer {
	if s.Processes == nil {
		return jobs.QueryAnswer{Value: NotApplicable}
	}
	list, err := s.Processes()
	if err != nil {
		return jobs.QueryAnswer{Value: "erro ao listar", Extra: err.Error()}
	}
	n := 0
	var sessions []string
	for _, p := range list {
		if p.Name == name || s.CaseInsensitive && strings.EqualFold(p.Name, name) {
			n++
			if ss := fmt.Sprint(p.Session); !slices.Contains(sessions, ss) {
				sessions = append(sessions, ss)
			}
		}
	}
	if n == 0 {
		return jobs.QueryAnswer{Value: "não está rodando"}
	}
	slices.Sort(sessions)
	extra := fmt.Sprintf("%d processo(s)", n)
	if s.CaseInsensitive { // sessões só fazem sentido no Windows
		extra += "; sessão " + strings.Join(sessions, ", ")
	}
	return jobs.QueryAnswer{Value: "rodando", Extra: extra}
}

func (s Sensors) service(ctx context.Context, name string) jobs.QueryAnswer {
	if s.Service == nil {
		return jobs.QueryAnswer{Value: NotApplicable}
	}
	state, start, found, err := s.Service(ctx, name)
	switch {
	case err != nil:
		return jobs.QueryAnswer{Value: "erro ao consultar", Extra: err.Error()}
	case !found:
		return jobs.QueryAnswer{Value: "não existe"}
	}
	return jobs.QueryAnswer{Value: state, Extra: start}
}

func (s Sensors) registry(arg, name string) jobs.QueryAnswer {
	if s.Registry == nil {
		return jobs.QueryAnswer{Value: NotApplicable}
	}
	v, keyFound, valueFound, err := s.Registry(jobs.RegistryPath(arg), name)
	switch {
	case err != nil:
		return jobs.QueryAnswer{Value: "erro ao ler", Extra: err.Error()}
	case !keyFound:
		return jobs.QueryAnswer{Value: "chave não existe"}
	case !valueFound:
		return jobs.QueryAnswer{Value: "valor não existe"}
	case jobs.SecretName(name):
		return jobs.QueryAnswer{Value: "existe (conteúdo oculto: o nome parece de senha)"}
	}
	return jobs.QueryAnswer{Value: v}
}

func (s Sensors) program(ctx context.Context, needle string) jobs.QueryAnswer {
	if s.Software == nil {
		return jobs.QueryAnswer{Value: NotApplicable}
	}
	list, err := s.Software(ctx)
	if err != nil {
		return jobs.QueryAnswer{Value: "erro ao listar", Extra: err.Error()}
	}
	n := strings.ToLower(needle)
	var versions, names []string
	for _, sw := range list {
		if !strings.Contains(strings.ToLower(sw.Name), n) {
			continue
		}
		v := sw.Version
		if v == "" {
			v = "sem versão"
		}
		if !slices.Contains(versions, v) {
			versions = append(versions, v)
		}
		if len(names) < 10 {
			names = append(names, strings.TrimSpace(sw.Name+" "+sw.Version))
		}
	}
	if len(versions) == 0 {
		return jobs.QueryAnswer{Value: "não instalado"}
	}
	slices.Sort(versions)
	return jobs.QueryAnswer{Value: strings.Join(versions, ", "), Extra: strings.Join(names, "; ")}
}
