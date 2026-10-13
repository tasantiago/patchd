package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type contratoPacotes interface {
	SaveThirdPartyPackage(ctx context.Context, p ThirdPartyPackage) (bool, error)
	ThirdPartyPackages(ctx context.Context) ([]ThirdPartyPackage, error)
}

func testarPacotes(t *testing.T, st contratoPacotes) {
	ctx := context.Background()
	if l, err := st.ThirdPartyPackages(ctx); err != nil || len(l) != 0 {
		t.Fatalf("vazio: %v %v", l, err)
	}
	zip := ThirdPartyPackage{AppID: "7zip", WingetID: "7zip.7zip", Version: "26.04", Architecture: "x64", Type: "exe", Scope: "machine",
		URL: "https://www.7-zip.org/a/7z2604-x64.exe", SHA256: strings.Repeat("d5", 32), Size: 1700000, Arguments: "/S"}
	if ok, err := st.SaveThirdPartyPackage(ctx, zip); err != nil || !ok {
		t.Fatalf("guardar: %t %v", ok, err)
	}
	// O mesmo de novo: não é novo, não é erro.
	if ok, err := st.SaveThirdPartyPackage(ctx, zip); err != nil || ok {
		t.Errorf("de novo: %t %v", ok, err)
	}
	time.Sleep(10 * time.Millisecond)
	chrome := ThirdPartyPackage{AppID: "google-chrome", WingetID: "Google.Chrome", Version: "155.0.8059.40", Architecture: "x64", Type: "wix",
		Scope: "machine", URL: "https://dl.google.com/x.msi", SHA256: strings.Repeat("32", 32), Size: 140 << 20,
		Arguments: "/quiet /norestart", ReturnCodes: map[int]string{3010: "rebootRequiredToFinish", 1618: "installInProgress"}}
	if ok, err := st.SaveThirdPartyPackage(ctx, chrome); err != nil || !ok {
		t.Fatalf("chrome: %t %v", ok, err)
	}
	// A mesma versão com outro binário: recusado, e o guardado não muda.
	outro := zip
	outro.SHA256 = strings.Repeat("ee", 32)
	_, err := st.SaveThirdPartyPackage(ctx, outro)
	var pc *PackageChangedError
	if !errors.As(err, &pc) || pc.Stored.SHA256 != zip.SHA256 || !strings.Contains(err.Error(), "a mesma versão com outro binário não é aceita") {
		t.Fatalf("outro binário: %v", err)
	}
	// Outro tipo da mesma versão é outro pacote (o MSI do 7-Zip).
	msi := zip
	msi.Type, msi.SHA256, msi.Arguments = "wix", strings.Repeat("0b", 32), "/quiet /norestart"
	if ok, err := st.SaveThirdPartyPackage(ctx, msi); err != nil || !ok {
		t.Errorf("o MSI: %t %v", ok, err)
	}
	l, err := st.ThirdPartyPackages(ctx)
	if err != nil || len(l) != 3 || l[0].AppID != "7zip" || l[2].AppID != "google-chrome" {
		t.Fatalf("lista: %+v %v", l, err)
	}
	c := l[2]
	if c.SHA256 != chrome.SHA256 || c.ReturnCodes[3010] != "rebootRequiredToFinish" || c.Size != 140<<20 || c.FetchedAt.IsZero() || !c.MSI() {
		t.Errorf("chrome lido: %+v", c)
	}
	for _, p := range l {
		if p.AppID == "7zip" && p.Type == "exe" && (p.SHA256 != zip.SHA256 || len(p.ReturnCodes) != 0) {
			t.Errorf("7zip lido: %+v", p)
		}
	}
}

func TestPacotesMemoria(t *testing.T) { testarPacotes(t, NewMemory()) }

func TestPacotesPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarPacotes(t, NewPostgres(pool))
}
