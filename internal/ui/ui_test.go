package ui

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

func imagem(t *testing.T, w, h int, jpg bool) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.NRGBA{R: 200, A: 255})
	var b bytes.Buffer
	var err error
	if jpg {
		err = jpeg.Encode(&b, img, nil)
	} else {
		err = png.Encode(&b, img)
	}
	if err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// Aula 19.1: o logotipo conferido pelos bytes (o servidor no envio; o agente e a Central de novo).
func TestCheckLogo(t *testing.T) {
	if ct, w, h, err := CheckLogo(imagem(t, 64, 32, false)); err != nil || ct != "image/png" || w != 64 || h != 32 {
		t.Errorf("png: %s %dx%d %v", ct, w, h, err)
	}
	if ct, _, _, err := CheckLogo(imagem(t, 10, 10, true)); err != nil || ct != "image/jpeg" {
		t.Errorf("jpeg: %s %v", ct, err)
	}
	for nome, c := range map[string]struct {
		b    []byte
		erro string
	}{
		"vazio":    {nil, "vazio"},
		"gif":      {[]byte("GIF89a...."), "só PNG ou JPEG"},
		"svg":      {[]byte(`<svg onload="x"/>`), "só PNG ou JPEG"},
		"falso":    {[]byte("\x89PNG\r\n\x1a\n<script>"), "corrompida"},
		"lado":     {imagem(t, MaxLogoSide+1, 4, false), "pixels"},
		"truncado": {imagem(t, 64, 64, false)[:70], "corrompida"},
		"grande":   {append(imagem(t, 4, 4, false), make([]byte, MaxLogoSize)...), "limite"},
	} {
		if _, _, _, err := CheckLogo(c.b); err == nil || !strings.Contains(err.Error(), c.erro) {
			t.Errorf("%s: %v", nome, err)
		}
	}
}
