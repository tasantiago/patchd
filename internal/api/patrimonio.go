package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/tasantiago/patchd/internal/panel"
	"github.com/tasantiago/patchd/internal/patrimonio"
	"github.com/tasantiago/patchd/internal/store"
	"github.com/tasantiago/patchd/internal/texto"
)

// Patrimônio (Aula 23.6): a localização física dos equipamentos vem de um CSV do sistema de
// patrimônio, importado em Configuração › Patrimônio. Cada importação substitui a anterior;
// o Inventário liga a máquina ao bem pelos dígitos do nome (internal/patrimonio).

// assetCache: o patrimônio na memória, pelo tombo. O Inventário lê a cada página; o banco só
// é lido de novo depois de uma importação (ou de reiniciar o servidor).
type assetCache struct {
	mu      sync.Mutex
	loaded  bool
	byTombo map[string]store.AssetLocation
}

func (a *api) assetMap(ctx context.Context) (map[string]store.AssetLocation, error) {
	a.assets.mu.Lock()
	defer a.assets.mu.Unlock()
	if a.assets.loaded {
		return a.assets.byTombo, nil
	}
	list, err := a.store.AssetLocations(ctx)
	if err != nil {
		return nil, err
	}
	m := make(map[string]store.AssetLocation, len(list))
	for _, l := range list {
		m[l.Tombo] = l
	}
	a.assets.byTombo, a.assets.loaded = m, true
	return m, nil
}

func (a *api) assetsChanged() {
	a.assets.mu.Lock()
	a.assets.loaded, a.assets.byTombo = false, nil
	a.assets.mu.Unlock()
}

// assetMaxBody: o arquivo e a moldura do multipart.
const assetMaxBody = patrimonio.MaxBytes + 64<<10

// assetUnmatchedShown: quantas máquinas sem tombo a aba lista.
const assetUnmatchedShown = 40

type assetImportRow struct {
	At, By, File              string
	Kept, Skipped, Duplicates int
}

type assetView struct {
	MaxMB             int
	Imports           []assetImportRow
	Assets            int // bens no patrimônio
	Machines, Matched int // máquinas no inventário; com tombo ligado
	AssetsWithMachine int // bens com máquina
	Unmatched         []equipLink
	UnmatchedMore     int
}

type equipLink struct{ ID, Name string }

// assetsTab monta a aba Patrimônio.
func (a *api) assetsTab(r *http.Request, v *settingsView) error {
	av := &assetView{MaxMB: patrimonio.MaxBytes >> 20}
	imps, err := a.store.AssetImports(r.Context(), 5)
	if err != nil {
		return err
	}
	for _, i := range imps {
		av.Imports = append(av.Imports, assetImportRow{At: a.stamp(i.At), By: i.By, File: dash(i.File), Kept: i.Kept,
			Skipped: i.Skipped, Duplicates: i.Duplicates})
	}
	assets, err := a.assetMap(r.Context())
	if err != nil {
		return err
	}
	av.Assets = len(assets)
	rows, err := a.store.InventoryList(r.Context())
	if err != nil {
		return err
	}
	all := buildEquip(rows, a.now())
	withAssets(all, assets)
	av.Machines = len(all)
	used := map[string]bool{}
	var unmatched []equipLink
	for _, e := range all {
		if e.Asset.Tombo != "" {
			av.Matched++
			used[e.Asset.Tombo] = true
			continue
		}
		unmatched = append(unmatched, equipLink{ID: e.ID, Name: dash(e.Name)})
	}
	av.AssetsWithMachine = len(used)
	slices.SortFunc(unmatched, func(x, y equipLink) int { return strings.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name)) })
	if len(unmatched) > assetUnmatchedShown {
		av.UnmatchedMore = len(unmatched) - assetUnmatchedShown
		unmatched = unmatched[:assetUnmatchedShown]
	}
	av.Unmatched = unmatched
	v.Assets = av
	return nil
}

// importAssetsAction: POST /painel/configuracao/patrimonio (multipart, campo arquivo).
func (a *api) importAssetsAction(w http.ResponseWriter, r *http.Request, s panel.Session) {
	back := func(q string) { http.Redirect(w, r, "/painel/configuracao?aba=patrimonio&"+q, http.StatusSeeOther) }
	fail := func(motivo string) { back("erro=patrimonio&motivo=" + url.QueryEscape(motivo)) }
	r.Body = http.MaxBytesReader(w, r.Body, assetMaxBody)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			fail(fmt.Sprintf("o arquivo passa de %d MB", patrimonio.MaxBytes>>20))
			return
		}
		fail("formulário inválido")
		return
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()
	f, hdr, err := r.FormFile("arquivo")
	if err != nil {
		fail("escolha o arquivo CSV")
		return
	}
	defer f.Close()
	res, err := patrimonio.Parse(f)
	if err != nil {
		fail(err.Error())
		return
	}
	if len(res.Items) == 0 {
		fail("nenhuma linha com tombo válido; a localização em uso não mudou")
		return
	}
	list := make([]store.AssetLocation, 0, len(res.Items))
	for _, it := range res.Items {
		list = append(list, store.AssetLocation(it))
	}
	name := texto.Clip(filepath.Base(strings.ReplaceAll(hdr.Filename, `\`, "/")), 200)
	if err := a.store.ReplaceAssetLocations(r.Context(), list, store.AssetImport{By: s.Username, File: name,
		Skipped: res.Skipped, Duplicates: res.Duplicates}); err != nil {
		a.internalError(w, r, "gravar o patrimônio", err)
		return
	}
	a.assetsChanged()
	linked := 0
	if assets, err := a.assetMap(r.Context()); err == nil {
		if rows, err := a.store.InventoryList(r.Context()); err == nil {
			all := buildEquip(rows, a.now())
			withAssets(all, assets)
			for _, e := range all {
				if e.Asset.Tombo != "" {
					linked++
				}
			}
		}
	}
	detail := fmt.Sprintf("%d bens · %d linhas sem tombo · %d repetidos · %d máquinas ligadas", len(list), res.Skipped, res.Duplicates, linked)
	a.audit(r, s, "patrimonio.importar", name, detail)
	a.logger.Info("patrimônio importado", "file", name, "assets", len(list), "skipped", res.Skipped, "duplicates", res.Duplicates,
		"linked", linked, "converted_cp1252", res.Converted, "username", s.Username, "request_id", RequestID(r.Context()))
	q := url.Values{"importado": {strconv.Itoa(len(list))}, "ligadas": {strconv.Itoa(linked)}}
	if res.Skipped > 0 {
		var lines []string
		for _, l := range res.SkippedAt {
			lines = append(lines, strconv.Itoa(l))
		}
		q.Set("ignoradas", strconv.Itoa(res.Skipped))
		q.Set("linhas", strings.Join(lines, ","))
	}
	if res.Duplicates > 0 {
		q.Set("repetidos", strconv.Itoa(res.Duplicates))
	}
	back(q.Encode())
}

// assetsFlash: as mensagens da aba depois de importar. Só números vêm da URL; o motivo do
// erro é curto e vai escapado no template.
func assetsFlash(q url.Values, v *pageBase) {
	num := func(k string) int { n, _ := strconv.Atoi(q.Get(k)); return max(n, 0) }
	switch {
	case q.Has("importado"):
		v.Flash = fmt.Sprintf("Patrimônio importado: %d bens, %d máquina(s) do inventário ligadas pelo tombo. A localização anterior foi substituída.",
			num("importado"), num("ligadas"))
		if n := num("ignoradas"); n > 0 {
			var lines []string
			for _, p := range strings.Split(q.Get("linhas"), ",") {
				if l, err := strconv.Atoi(p); err == nil && l > 0 && len(lines) < 10 {
					lines = append(lines, strconv.Itoa(l))
				}
			}
			v.Flash += fmt.Sprintf(" %d linha(s) sem tombo válido ficaram de fora", n)
			if len(lines) > 0 {
				v.Flash += " (linhas " + strings.Join(lines, ", ") + ")"
			}
			v.Flash += "."
		}
		if n := num("repetidos"); n > 0 {
			v.Flash += fmt.Sprintf(" %d tombo(s) repetido(s): valeu a última linha de cada.", n)
		}
	case q.Get("erro") == "patrimonio":
		v.Error = "O patrimônio não foi importado: " + shortError(q.Get("motivo"), 200) + "."
	}
}
