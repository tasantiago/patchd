package jobs

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestParametrosDoUpdate(t *testing.T) {
	ok := `{"packages":[{"name":"libcurl4t64","min_version":"8.18.0-1ubuntu2.10"},{"name":"openssl-libs","min_version":"1:3.5.8-1.fc44"},{"name":"NetworkManager","min_version":"1.52.0-1.fc44"}]}`
	p, err := ParseUpdateParams(json.RawMessage(ok))
	if err != nil || len(p.Packages) != 3 || strings.Join(p.Names(), ",") != "libcurl4t64,openssl-libs,NetworkManager" {
		t.Fatalf("válido: %+v %v", p, err)
	}
	muitos := make([]Package, MaxPackages+1)
	for i := range muitos {
		muitos[i] = Package{Name: "p" + strings.Repeat("x", i%5) + string(rune('a'+i%26)) + "-" + string(rune('a'+i/26)), MinVersion: "1"}
	}
	grande, _ := json.Marshal(UpdateParams{Packages: muitos})
	for nome, raw := range map[string]string{
		"opção":         `{"packages":[{"name":"-o","min_version":"1"}]}`,
		"espaço":        `{"packages":[{"name":"curl bash","min_version":"1"}]}`,
		"igual":         `{"packages":[{"name":"curl=1","min_version":"1"}]}`,
		"curinga":       `{"packages":[{"name":"curl*","min_version":"1"}]}`,
		"versão":        `{"packages":[{"name":"curl","min_version":"-1"}]}`,
		"sem versão":    `{"packages":[{"name":"curl","min_version":""}]}`,
		"repetido":      `{"packages":[{"name":"curl","min_version":"1"},{"name":"curl","min_version":"2"}]}`,
		"campo novo":    `{"packages":[{"name":"curl","min_version":"1","repo":"http://x"}]}`,
		"vazio":         `{"packages":[]}`,
		"sem lista":     `{}`,
		"mais de 100":   string(grande),
		"não é objeto":  `["curl"]`,
		"comando livre": `{"packages":[{"name":"curl","min_version":"1"}],"command":"rm -rf /"}`,
	} {
		if _, err := ParseUpdateParams(json.RawMessage(raw)); err == nil {
			t.Errorf("%s: aceito", nome)
		}
	}

	// O agente confere os parâmetros junto com a assinatura; o servidor nem assina um update ruim.
	pub, priv, _ := GenerateKey()
	agora := time.Date(2026, 10, 9, 14, 0, 0, 0, time.UTC)
	env, err := Sign(priv, Job{ID: 1, MachineID: "m", Type: TypeUpdate, Params: json.RawMessage(ok), IssuedAt: agora, NotAfter: agora.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if j, err := Verify(pub, env, "m", agora, 0); err != nil || j.Type != TypeUpdate {
		t.Errorf("update válido: %v", err)
	}
	if _, err := Sign(priv, Job{ID: 2, MachineID: "m", Type: TypeUpdate, Params: json.RawMessage(`{"packages":[{"name":"-x","min_version":"1"}]}`), IssuedAt: agora, NotAfter: agora.Add(time.Hour)}); err == nil {
		t.Error("o servidor não assina um update com pacote inválido")
	}
	if _, err := Sign(priv, Job{ID: 3, MachineID: "m", Type: TypeRescan, Params: json.RawMessage(`{"x":1}`), IssuedAt: agora, NotAfter: agora.Add(time.Hour)}); err == nil {
		t.Error("rescan com parâmetros")
	}
}
