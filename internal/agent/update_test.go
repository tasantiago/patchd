package agent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tasantiago/patchd/internal/api"
	"github.com/tasantiago/patchd/internal/release"
)

// versaoPublicada monta e publica, numa pasta de versões, a versão v com um executável
// linux/amd64 de conteúdo conteudo, assinada com priv.
func versaoPublicada(t *testing.T, priv ed25519.PrivateKey, v string, conteudo []byte) release.Dir {
	t.Helper()
	d := release.Dir(t.TempDir())
	vdir := filepath.Join(string(d), v)
	_ = os.MkdirAll(vdir, 0o755)
	m := release.Manifest{SchemaVersion: release.ManifestSchemaVersion, Version: v, CreatedAt: time.Now().UTC(),
		Files: []release.File{{OS: "linux", Arch: "amd64", Name: "patchd-agent-linux-amd64",
			SHA256: release.HashBytes(conteudo), Size: int64(len(conteudo))}}}
	data, sig, err := release.Sign(priv, m)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(vdir, release.ManifestFile), data, 0o644)
	_ = os.WriteFile(filepath.Join(vdir, release.SignatureFile), sig, 0o644)
	_ = os.WriteFile(filepath.Join(vdir, "patchd-agent-linux-amd64"), conteudo, 0o644)
	if err := d.Publish(v); err != nil {
		t.Fatal(err)
	}
	return d
}

type instalacoes struct {
	n        atomic.Int32
	conteudo []byte
	caminho  string
}

func updaterDeTeste(t *testing.T, srv *servidor, cred string, pub ed25519.PublicKey, atual string, inst *instalacoes, log io.Writer) *Updater {
	t.Helper()
	return &Updater{
		Client:    NewClient(srv.URL, cred, atual),
		DataDir:   t.TempDir(),
		Current:   atual,
		PublicKey: pub,
		Install: func(bin string) error {
			inst.n.Add(1)
			inst.caminho = bin
			inst.conteudo, _ = os.ReadFile(bin)
			return nil
		},
		Logger: slog.New(slog.NewTextHandler(log, nil)),
		goos:   "linux",
		goarch: "amd64",
		selfTest: func(_ context.Context, bin string) (string, error) {
			return "patchd-agent v0.5.1 (commit teste)\n", nil
		},
	}
}

func TestAtualizacaoPeloCheckin(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	novo := []byte("executável v0.5.1")
	d := versaoPublicada(t, priv, "v0.5.1", novo)
	srv, _, cred := novoServidor(t, api.WithReleases(d, "v0.5.1"))

	var inst instalacoes
	var log bytes.Buffer
	u := updaterDeTeste(t, srv, cred, pub, "v0.5.0", &inst, &log)
	var buscas atomic.Int32
	a := agenteDeTeste(t, srv, cred, &buscas, io.Discard)
	a.Client = NewClient(srv.URL, cred, "v0.5.0")
	a.Update = u.Offer

	if out := a.CheckIn(context.Background()); out != outcomeOK {
		t.Fatalf("ciclo: %v", out)
	}
	if inst.n.Load() != 1 || !bytes.Equal(inst.conteudo, novo) {
		t.Fatalf("o instalador recebe o executável conferido: %d %q\n%s", inst.n.Load(), inst.conteudo, log.String())
	}
	if filepath.Dir(inst.caminho) != filepath.Join(u.DataDir, updateDir) {
		t.Errorf("o executável fica na pasta de dados: %s", inst.caminho)
	}

	// No ciclo seguinte (o serviço ainda não foi trocado), a mesma versão não é baixada de novo.
	if out := a.CheckIn(context.Background()); out != outcomeOK || inst.n.Load() != 1 {
		t.Errorf("tentativa recente não se repete: %v %d", out, inst.n.Load())
	}
	if srv.conta("/api/v1/agent/releases/v0.5.1/patchd-agent-linux-amd64") != 1 {
		t.Errorf("um download só: %d", srv.conta("/api/v1/agent/releases/v0.5.1/patchd-agent-linux-amd64"))
	}

	u.CleanUpdates()
	if _, err := os.Stat(inst.caminho); !errors.Is(err, os.ErrNotExist) {
		t.Error("a limpeza apaga o executável baixado")
	}
	if _, err := os.Stat(filepath.Join(u.DataDir, updateDir, attemptFile)); err != nil {
		t.Error("a limpeza mantém o registro da tentativa")
	}
}

func TestAtualizacaoRecusada(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	novo := []byte("executável v0.5.1")

	casos := []struct {
		nome    string
		estraga func(d release.Dir)
		atual   string
		chave   ed25519.PublicKey
		teste   func(context.Context, string) (string, error)
		quer    string
	}{
		{nome: "manifesto alterado no servidor", atual: "v0.5.0", chave: pub, quer: "assinatura",
			estraga: func(d release.Dir) {
				p := filepath.Join(string(d), "v0.5.1", release.ManifestFile)
				data, _ := os.ReadFile(p)
				_ = os.WriteFile(p, bytes.Replace(data, []byte(`"linux"`), []byte(`"linux" `), 1), 0o644)
			}},
		{nome: "executável trocado no servidor", atual: "v0.5.0", chave: pub, quer: "não confere",
			estraga: func(d release.Dir) { // mesmo tamanho, outro conteúdo
				_ = os.WriteFile(filepath.Join(string(d), "v0.5.1", "patchd-agent-linux-amd64"), []byte("executável v6.6.6"), 0o644)
			}},
		{nome: "executável maior que o do manifesto", atual: "v0.5.0", chave: pub, quer: "limite",
			estraga: func(d release.Dir) {
				_ = os.WriteFile(filepath.Join(string(d), "v0.5.1", "patchd-agent-linux-amd64"), bytes.Repeat([]byte("x"), 1<<20), 0o644)
			}},
		{nome: "outra chave", atual: "v0.5.0", quer: "assinatura",
			chave: func() ed25519.PublicKey { p, _, _ := ed25519.GenerateKey(rand.Reader); return p }()},
		{nome: "versão mais velha (rollback ou replay)", atual: "v0.6.0", chave: pub, quer: "não é mais nova"},
		{nome: "mesma versão", atual: "v0.5.1", chave: pub, quer: "não é mais nova"},
		{nome: "executável não se identifica", atual: "v0.5.0", chave: pub, quer: "não se identificou",
			teste: func(context.Context, string) (string, error) { return "patchd-agent v0.4.0 (x)", nil }},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			d := versaoPublicada(t, priv, "v0.5.1", novo)
			if c.estraga != nil {
				c.estraga(d)
			}
			srv, _, cred := novoServidor(t, api.WithReleases(d, "v0.5.1"))
			var inst instalacoes
			var log bytes.Buffer
			u := updaterDeTeste(t, srv, cred, c.chave, c.atual, &inst, &log)
			if c.teste != nil {
				u.selfTest = c.teste
			}
			u.Offer(context.Background(), "v0.5.1")
			if inst.n.Load() != 0 || !strings.Contains(log.String(), c.quer) {
				t.Errorf("nada instalado, e o log diz %q: instalações=%d\n%s", c.quer, inst.n.Load(), log.String())
			}
			// Nenhum executável recusado fica no disco.
			entries, _ := os.ReadDir(filepath.Join(u.DataDir, updateDir))
			for _, e := range entries {
				if e.Name() != attemptFile {
					t.Errorf("sobrou %s na pasta de atualização", e.Name())
				}
			}
		})
	}
}

func TestServidorNaoOfereceAgenteMaisNovoQueEle(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	d := versaoPublicada(t, priv, "v0.5.1", []byte("x"))

	// Servidor v0.5.0 com o agente v0.5.1 publicado: nada é oferecido nem servido.
	srv, _, cred := novoServidor(t, api.WithReleases(d, "v0.5.0"))
	c := NewClient(srv.URL, cred, "v0.5.0")
	resp, err := c.CheckIn(context.Background(), "")
	if err != nil || resp.AgentUpdate != nil {
		t.Errorf("servidor mais velho que a versão publicada não oferece: %+v %v", resp, err)
	}
	var rej *RejectedError
	if _, err := c.Download(context.Background(), "/api/v1/agent/releases/v0.5.1/manifest.json", io.Discard, 1<<20); !errors.As(err, &rej) || rej.Status != 404 {
		t.Errorf("nem serve os arquivos: %v", err)
	}

	// Servidor atualizado: oferece a quem está em outra versão, e não a quem já está nela.
	srv2, _, cred2 := novoServidor(t, api.WithReleases(d, "v0.5.1"))
	resp, _ = NewClient(srv2.URL, cred2, "v0.5.0").CheckIn(context.Background(), "")
	if resp.AgentUpdate == nil || resp.AgentUpdate.Version != "v0.5.1" {
		t.Errorf("oferta: %+v", resp.AgentUpdate)
	}
	resp, _ = NewClient(srv2.URL, cred2, "v0.5.1").CheckIn(context.Background(), "")
	if resp.AgentUpdate != nil {
		t.Errorf("quem já está na versão não recebe oferta: %+v", resp.AgentUpdate)
	}

	// Sem credencial, nada é baixado.
	if _, err := NewClient(srv2.URL, "patchd_mac_invalida", "v0.5.0").Download(context.Background(),
		"/api/v1/agent/releases/v0.5.1/manifest.json", io.Discard, 1<<20); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("download sem credencial: %v", err)
	}
	// Download maior que o limite: recusado.
	if _, err := NewClient(srv2.URL, cred2, "v0.5.0").Download(context.Background(),
		"/api/v1/agent/releases/v0.5.1/manifest.json", io.Discard, 10); err == nil || !strings.Contains(err.Error(), "limite") {
		t.Errorf("limite de tamanho: %v", err)
	}
}
