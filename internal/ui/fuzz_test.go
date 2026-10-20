package ui

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

// Aula 20.3: o logotipo chega pelo painel (admin) e, depois, pela API dos agentes até a
// Central. Para quaisquer bytes, CheckLogo não entra em pânico; o que ele aceita é PNG ou
// JPEG dentro dos limites e decodifica de novo sem erro.
func FuzzCheckLogo(f *testing.F) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 3))
	img.Set(1, 1, color.RGBA{200, 160, 40, 255})
	var p, j bytes.Buffer
	_ = png.Encode(&p, img)
	_ = jpeg.Encode(&j, img, nil)
	f.Add(p.Bytes())
	f.Add(j.Bytes())
	f.Add([]byte("\x89PNG\r\n\x1a\n"))
	f.Fuzz(func(t *testing.T, b []byte) {
		ct, w, h, err := CheckLogo(b)
		if err != nil {
			return
		}
		if (ct != "image/png" && ct != "image/jpeg") || w < 1 || h < 1 || w > MaxLogoSide || h > MaxLogoSide || len(b) > MaxLogoSize {
			t.Fatalf("aceito fora dos limites: %s %dx%d, %d bytes", ct, w, h, len(b))
		}
		if _, _, err := image.Decode(bytes.NewReader(b)); err != nil {
			t.Fatalf("aceito, mas não decodifica: %v", err)
		}
	})
}
