// Package document implements Module B: issuing per-recipient PDF copies with
// layered invisible marks, simulating leak transformations, and decoding the
// surviving marks from a recovered PDF, image or text.
package document

import (
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// Font is the single embedded typeface. The PDF writer embeds the same TTF the
// rasterizer draws with, so simulated screenshots match what viewers show.
type Font struct {
	TTF       []byte
	ot        *opentype.Font
	Widths    [256]float64 // advance per 1000 em for WinAnsi bytes 32..126
	Ascent    float64
	Descent   float64
	CapHeight float64
	BBox      [4]float64
}

func LoadFont() *Font {
	ot, err := opentype.Parse(goregular.TTF)
	if err != nil {
		panic(err)
	}
	f := &Font{TTF: goregular.TTF, ot: ot}
	var buf sfnt.Buffer
	upem := float64(ot.UnitsPerEm())
	ppem := fixed.I(int(ot.UnitsPerEm()))
	for c := 32; c < 127; c++ {
		idx, err := ot.GlyphIndex(&buf, rune(c))
		if err != nil {
			continue
		}
		adv, err := ot.GlyphAdvance(&buf, idx, ppem, font.HintingNone)
		if err == nil {
			f.Widths[c] = float64(adv) / 64 * 1000 / upem
		}
	}
	if m, err := ot.Metrics(&buf, ppem, font.HintingNone); err == nil {
		f.Ascent = float64(m.Ascent) / 64 * 1000 / upem
		f.Descent = -float64(m.Descent) / 64 * 1000 / upem
		f.CapHeight = float64(m.CapHeight) / 64 * 1000 / upem
	}
	if b, err := ot.Bounds(&buf, ppem, font.HintingNone); err == nil {
		s := 1000 / upem / 64
		f.BBox = [4]float64{float64(b.Min.X) * s, -float64(b.Max.Y) * s, float64(b.Max.X) * s, -float64(b.Min.Y) * s}
	}
	return f
}

// Width of s in points at the given size, using the PDF widths (no kerning).
func (f *Font) Width(s string, size float64) float64 {
	w := 0.0
	for i := 0; i < len(s); i++ {
		w += f.Widths[s[i]]
	}
	return w * size / 1000
}

func (f *Font) face(sizePx float64) font.Face {
	face, err := opentype.NewFace(f.ot, &opentype.FaceOptions{Size: sizePx, DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		panic(err)
	}
	return face
}
