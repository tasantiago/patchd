package store

import (
	"context"
	"testing"
)

type contratoFontesInstalador interface {
	InstallerSources(ctx context.Context) ([]InstallerSource, error)
	SaveInstallerSource(ctx context.Context, s InstallerSource) error
	DeleteInstallerSource(ctx context.Context, app string) (bool, error)
}

// A fonte do instalador: grava, troca, lista por programa e tira; a fora do formato é recusada.
func testarFontesInstalador(t *testing.T, st contratoFontesInstalador) {
	ctx := context.Background()
	ff := InstallerSource{AppID: "mozilla-firefox", Kind: SourceURL, URLTemplate: "https://x/{versao}/setup.msi", Type: "msi",
		ChecksumURL: "https://x/{versao}/SHA256SUMS", AutoFetch: true, UpdatedBy: "ana"}
	if err := st.SaveInstallerSource(ctx, ff); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveInstallerSource(ctx, InstallerSource{AppID: "7zip", Kind: SourceWinget, AutoFetch: true, UpdatedBy: "ana"}); err != nil {
		t.Fatal(err)
	}
	ff.Type, ff.UpdatedBy = "nullsoft", "bia"
	if err := st.SaveInstallerSource(ctx, ff); err != nil {
		t.Fatal(err)
	}
	l, err := st.InstallerSources(ctx)
	if err != nil || len(l) != 2 || l[0].AppID != "7zip" || l[1].Type != "nullsoft" || l[1].UpdatedBy != "bia" || !l[1].AutoFetch ||
		l[1].ChecksumURL != ff.ChecksumURL || l[1].UpdatedAt.IsZero() {
		t.Fatalf("lista: %+v %v", l, err)
	}
	for _, ruim := range []InstallerSource{{Kind: SourceURL, UpdatedBy: "a"}, {AppID: "x", Kind: "ftp", UpdatedBy: "a"},
		{AppID: "x", Kind: SourceURL, Type: "msi", UpdatedBy: "a"}, {AppID: "x", Kind: SourceWinget}} {
		if err := st.SaveInstallerSource(ctx, ruim); err == nil {
			t.Errorf("aceita: %+v", ruim)
		}
	}
	if ok, err := st.DeleteInstallerSource(ctx, "7zip"); !ok || err != nil {
		t.Errorf("tirar: %v %v", ok, err)
	}
	if ok, _ := st.DeleteInstallerSource(ctx, "7zip"); ok {
		t.Error("tirar de novo")
	}
	if l, _ := st.InstallerSources(ctx); len(l) != 1 {
		t.Errorf("depois de tirar: %+v", l)
	}
}

func TestFontesDoInstaladorMemoria(t *testing.T) { testarFontesInstalador(t, NewMemory()) }

func TestFontesDoInstaladorPostgres(t *testing.T) {
	pool := bancoDeTeste(t)
	if err := Migrate(context.Background(), pool, silencioso()); err != nil {
		t.Fatal(err)
	}
	testarFontesInstalador(t, NewPostgres(pool))
}
