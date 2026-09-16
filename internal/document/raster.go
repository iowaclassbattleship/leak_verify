package document

import (
	"image"
	"image/color"
	"image/draw"
	"math"
	"math/rand/v2"

	"golang.org/x/image/math/fixed"
)

// Render rasterizes a page content stream at `scale` pixels per point, the
// way a PDF viewer would display this demo's files.
func (f *Font) Render(pc PageContent, scale float64) *image.Gray {
	w, h := int(math.Round(PageW*scale)), int(math.Round(PageH*scale))
	img := image.NewGray(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	for _, r := range pc.Rects {
		x0, x1 := int(math.Round(r.X*scale)), int(math.Round((r.X+r.W)*scale))
		y0, y1 := int(math.Round((PageH-r.Y-r.H)*scale)), int(math.Round((PageH-r.Y)*scale))
		v := uint8(math.Round(math.Max(0, math.Min(1, r.Gray)) * 255))
		draw.Draw(img, image.Rect(x0, y0, x1, y1), image.NewUniform(color.Gray{v}), image.Point{}, draw.Src)
	}
	for _, d := range pc.Images {
		drawImage(img, d, scale)
	}
	for _, run := range pc.Runs {
		face := f.face(run.Size * scale)
		dot := fixed.Point26_6{X: fixed.Int26_6(run.X * scale * 64), Y: fixed.Int26_6((PageH - run.Y) * scale * 64)}
		for i := 0; i < len(run.Text); i++ {
			c := run.Text[i]
			if dr, mask, maskp, _, ok := face.Glyph(dot, rune(c)); ok {
				draw.DrawMask(img, dr, image.Black, image.Point{}, mask, maskp, draw.Over)
			}
			dot.X += fixed.Int26_6(math.Round(f.Widths[c] / 1000 * run.Size * scale * 64))
		}
		face.Close()
	}
	return img
}

// drawImage resamples an image XObject bilinearly onto its placement,
// optionally with the Multiply blend mode.
func drawImage(dst *image.Gray, d ImageDraw, scale float64) {
	src := d.Img
	if src == nil || d.W <= 0 || d.H <= 0 {
		return
	}
	b := dst.Bounds()
	x0, x1 := max(int(math.Floor(d.X*scale)), 0), min(int(math.Ceil((d.X+d.W)*scale)), b.Dx())
	top := PageH - d.Y - d.H
	y0, y1 := max(int(math.Floor(top*scale)), 0), min(int(math.Ceil((top+d.H)*scale)), b.Dy())
	sample := func(u, v float64) float64 {
		u = math.Max(0, math.Min(float64(src.W-1), u))
		v = math.Max(0, math.Min(float64(src.H-1), v))
		ix, iy := min(int(u), src.W-2), min(int(v), src.H-2)
		fx, fy := u-float64(ix), v-float64(iy)
		p := func(x, y int) float64 { return float64(src.Gray[y*src.W+x]) }
		return (p(ix, iy)*(1-fx)+p(ix+1, iy)*fx)*(1-fy) + (p(ix, iy+1)*(1-fx)+p(ix+1, iy+1)*fx)*fy
	}
	for py := y0; py < y1; py++ {
		v := ((float64(py)+0.5)/scale-top)/d.H*float64(src.H) - 0.5
		for px := x0; px < x1; px++ {
			u := ((float64(px)+0.5)/scale-d.X)/d.W*float64(src.W) - 0.5
			val := sample(u, v)
			i := py*dst.Stride + px
			if d.Multiply {
				val = float64(dst.Pix[i]) * val / 255
			}
			dst.Pix[i] = uint8(math.Round(math.Max(0, math.Min(255, val))))
		}
	}
}

// grayF is a float working buffer for image degradations and analysis.
type grayF struct {
	W, H int
	Pix  []float32
}

func toGrayF(img image.Image) *grayF {
	b := img.Bounds()
	g := &grayF{W: b.Dx(), H: b.Dy(), Pix: make([]float32, b.Dx()*b.Dy())}
	if gi, ok := img.(*image.Gray); ok {
		for y := 0; y < g.H; y++ {
			row := gi.Pix[(y)*gi.Stride : y*gi.Stride+g.W]
			for x, v := range row {
				g.Pix[y*g.W+x] = float32(v)
			}
		}
		return g
	}
	for y := 0; y < g.H; y++ {
		for x := 0; x < g.W; x++ {
			r, gg, bb, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			g.Pix[y*g.W+x] = float32(0.299*float64(r)+0.587*float64(gg)+0.114*float64(bb)) / 257
		}
	}
	return g
}

// maxAnalysisPixels bounds the working resolution of uploaded captures; larger
// images are box-downsampled, which keeps memory and time predictable.
const maxAnalysisPixels = 5_000_000

// loadGray converts an uploaded image to luma, box-averaging by an integer
// factor when it exceeds maxAnalysisPixels.
func loadGray(img image.Image) *grayF {
	b := img.Bounds()
	W, H := b.Dx(), b.Dy()
	k := max(1, int(math.Ceil(math.Sqrt(float64(W)*float64(H)/maxAnalysisPixels))))
	var luma func(x, y int) float32
	switch m := img.(type) {
	case *image.Gray:
		luma = func(x, y int) float32 { return float32(m.Pix[y*m.Stride+x]) }
	case *image.YCbCr:
		luma = func(x, y int) float32 { return float32(m.Y[y*m.YStride+x]) }
	case *image.NRGBA:
		luma = func(x, y int) float32 {
			p := m.Pix[y*m.Stride+x*4:]
			return 0.299*float32(p[0]) + 0.587*float32(p[1]) + 0.114*float32(p[2])
		}
	case *image.RGBA:
		luma = func(x, y int) float32 {
			p := m.Pix[y*m.Stride+x*4:]
			return 0.299*float32(p[0]) + 0.587*float32(p[1]) + 0.114*float32(p[2])
		}
	default:
		luma = func(x, y int) float32 {
			r, g, bb, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			return float32(0.299*float64(r)+0.587*float64(g)+0.114*float64(bb)) / 257
		}
	}
	g := &grayF{W: W / k, H: H / k}
	g.Pix = make([]float32, g.W*g.H)
	inv := 1 / float32(k*k)
	for y := 0; y < g.H; y++ {
		for x := 0; x < g.W; x++ {
			var acc float32
			for dy := 0; dy < k; dy++ {
				for dx := 0; dx < k; dx++ {
					acc += luma(x*k+dx, y*k+dy)
				}
			}
			g.Pix[y*g.W+x] = acc * inv
		}
	}
	return g
}

func (g *grayF) toImage() *image.Gray {
	img := image.NewGray(image.Rect(0, 0, g.W, g.H))
	for i, v := range g.Pix {
		img.Pix[i] = uint8(math.Max(0, math.Min(255, math.Round(float64(v)))))
	}
	return img
}

func (g *grayF) at(x, y float64) float32 {
	x0, y0 := int(math.Floor(x)), int(math.Floor(y))
	if x0 < 0 || y0 < 0 || x0+1 >= g.W || y0+1 >= g.H {
		return 255
	}
	fx, fy := float32(x-float64(x0)), float32(y-float64(y0))
	p := g.Pix
	i := y0*g.W + x0
	top := p[i]*(1-fx) + p[i+1]*fx
	bot := p[i+g.W]*(1-fx) + p[i+g.W+1]*fx
	return top*(1-fy) + bot*fy
}

func (g *grayF) blur(sigma float64) {
	r := int(math.Ceil(sigma * 3))
	k := make([]float32, 2*r+1)
	var sum float32
	for i := range k {
		d := float64(i - r)
		k[i] = float32(math.Exp(-d * d / (2 * sigma * sigma)))
		sum += k[i]
	}
	for i := range k {
		k[i] /= sum
	}
	tmp := make([]float32, len(g.Pix))
	for y := 0; y < g.H; y++ {
		for x := 0; x < g.W; x++ {
			var acc float32
			for i, kv := range k {
				xx := min(max(x+i-r, 0), g.W-1)
				acc += g.Pix[y*g.W+xx] * kv
			}
			tmp[y*g.W+x] = acc
		}
	}
	for y := 0; y < g.H; y++ {
		for x := 0; x < g.W; x++ {
			var acc float32
			for i, kv := range k {
				yy := min(max(y+i-r, 0), g.H-1)
				acc += tmp[yy*g.W+x] * kv
			}
			g.Pix[y*g.W+x] = acc
		}
	}
}

// printScan simulates printing on an office laser printer and scanning the
// sheet back. Deskewing is assumed to have happened (no rotation).
func printScan(src *image.Gray, seed uint64) *image.Gray {
	g := toGrayF(src)
	// Printer: tones lighter than ~7% coverage deposit no toner.
	for i, v := range g.Pix {
		if v > 237 {
			g.Pix[i] = 255
		}
	}
	g.blur(0.8) // toner spread / dot gain
	// Paper feed: content lands 1.4% smaller and offset on the scanned sheet.
	const s = 0.986
	dx, dy := 9.0, 13.0
	out := &grayF{W: g.W, H: g.H, Pix: make([]float32, len(g.Pix))}
	cx, cy := float64(g.W)/2, float64(g.H)/2
	for y := 0; y < out.H; y++ {
		for x := 0; x < out.W; x++ {
			out.Pix[y*out.W+x] = g.at((float64(x)-cx-dx)/s+cx, (float64(y)-cy-dy)/s+cy)
		}
	}
	out.blur(0.7) // scanner optics
	rng := rand.New(rand.NewPCG(seed, seed^7))
	for i, v := range out.Pix {
		// paper tone ≈ 238, toner black ≈ 28, sensor noise σ ≈ 5
		out.Pix[i] = 28 + v*(238-28)/255 + float32(rng.NormFloat64()*5)
	}
	return out.toImage()
}
