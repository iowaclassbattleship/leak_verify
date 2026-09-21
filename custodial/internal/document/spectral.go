package document

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"image"
	"image/png"
	"math"
	"math/cmplx"
	"math/rand/v2"
	"sort"

	"attribution/common/codec"
)

// Frequency-domain layer: a spread-spectrum watermark (Cox et al.).
//
// A keyed plan assigns ~330 mid-frequency basis functions cos(2π(u·x+v·y)/T+ψ)
// of a T×T tile: 74 "pilots" with fixed keyed signs for synchronisation, the
// rest split evenly across the 32 codeword bits, whose sign each encodes.
// Their sum is a faint noise-like texture (σ ≈ 1.5 grey levels) placed as a
// repeated background image with the Multiply blend mode. Because every bit
// is spread over the whole tile and the tile repeats, a detector can fold any
// sufficiently large piece of a screenshot back into one tile, recover scale
// and offset from the pilots, and read the bits by correlation.

const (
	SpecTilePx = 128  // tile resolution in image pixels
	SpecTilePt = 64.0 // tile size on the page (≈144 dpi)

	// The band sits high enough that the texture is hard to see (features of
	// roughly 1 mm on the page, where the eye is far less sensitive than at the
	// 2-5 mm of a lower band) but low enough to survive a rescaled JPEG
	// screenshot. Amplitude is a fraction of one grey level per component.
	specBase    = 253.0 // mean grey of the tile; Multiply darkens paper by <1%
	specStd     = 0.9   // texture standard deviation in grey levels
	specRMin    = 8.0   // band, in cycles per tile
	specRMax    = 16.0
	specCoefs   = 384 // basis functions used, keyed subset of the band
	specPilots  = 96  // of those, reserved for synchronisation
	specImgName = "Wm"
	// specZMin is the pilot correlation (in noise standard deviations) needed
	// to accept synchronisation; the offset search alone reaches ≈4 on noise.
	specZMin = 6.0
)

type specCoef struct {
	u, v  int
	psi   float64
	sign  float64
	pilot bool
	bit   int
}

type specPlan struct {
	coefs  []specCoef
	decoys [][2]int // unused frequencies of the band, used to estimate noise
	amp    float64
	perBit int
	maxUV  int // largest frequency index in use
}

func newSpecPlan(key codec.Key) *specPlan {
	s := key.Sum("spectral-plan")
	rng := rand.New(rand.NewPCG(binary.BigEndian.Uint64(s[:8]), binary.BigEndian.Uint64(s[8:16])))
	p := &specPlan{}
	var band [][2]int
	for v := 0; v <= int(specRMax); v++ {
		for u := -int(specRMax); u <= int(specRMax); u++ {
			if v == 0 && u <= 0 {
				continue // conjugate half-plane duplicates
			}
			if r := math.Hypot(float64(u), float64(v)); r >= specRMin && r <= specRMax {
				band = append(band, [2]int{u, v})
			}
		}
	}
	rng.Shuffle(len(band), func(i, j int) { band[i], band[j] = band[j], band[i] })
	used := min(len(band), specCoefs/2)
	for _, f := range band[:used] {
		p.coefs = append(p.coefs, specCoef{u: f[0], v: f[1], psi: 0}, specCoef{u: f[0], v: f[1], psi: math.Pi / 2})
	}
	// Unused frequencies of the same band are the noise reference: sharing the
	// band keeps the comparison fair whatever the capture resolution.
	p.decoys = band[used:]
	rng.Shuffle(len(p.coefs), func(i, j int) { p.coefs[i], p.coefs[j] = p.coefs[j], p.coefs[i] })
	for i := range p.coefs {
		c := &p.coefs[i]
		c.sign = 1
		if rng.IntN(2) == 0 {
			c.sign = -1
		}
		if i < specPilots {
			c.pilot = true
		} else {
			c.bit = (i - specPilots) % codec.CodeLen
		}
	}
	p.perBit = (len(p.coefs) - specPilots) / codec.CodeLen
	p.amp = specStd / math.Sqrt(float64(len(p.coefs))/2)
	for _, c := range p.coefs {
		p.maxUV = max(p.maxUV, max(abs2(c.u), c.v))
	}
	for _, d := range p.decoys {
		p.maxUV = max(p.maxUV, max(abs2(d[0]), d[1]))
	}
	return p
}

func abs2(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func phasor(turns float64) complex128 {
	return cmplx.Exp(complex(0, 2*math.Pi*turns))
}

// tile renders the watermark tile for one codeword.
func (p *specPlan) tile(code codec.Codeword) *PDFImage {
	T := SpecTilePx
	ex := make([][]complex128, 2*p.maxUV+1) // ex[u+maxUV][x] = e^{i2πux/T}
	for u := -p.maxUV; u <= p.maxUV; u++ {
		ex[u+p.maxUV] = make([]complex128, T)
		for x := 0; x < T; x++ {
			ex[u+p.maxUV][x] = phasor(float64(u*x) / float64(T))
		}
	}
	w := make([]float64, T*T)
	for _, c := range p.coefs {
		m := p.amp * c.sign
		if !c.pilot && code[c.bit] == 0 {
			m = -m
		}
		rot := cmplx.Exp(complex(0, c.psi)) * complex(m, 0)
		for y := 0; y < T; y++ {
			ey := ex[c.v+p.maxUV][y] * rot
			row := w[y*T : (y+1)*T]
			for x := range row {
				row[x] += real(ex[c.u+p.maxUV][x] * ey)
			}
		}
	}
	img := &PDFImage{Name: specImgName, W: T, H: T, Gray: make([]byte, T*T)}
	for i, v := range w {
		img.Gray[i] = uint8(math.Max(0, math.Min(255, math.Round(specBase+v))))
	}
	return img
}

// spectrum measures C(u,v) = Σ grid·e^{-i2π(u·sx+v·sy)/T} for every planned
// and decoy frequency. Grid cell g sits at tile coordinate (g+off)·step.
// Results are normalised so a basis function of amplitude a projects to ≈a.
func (p *specPlan) spectrum(grid []float64, G int, step, off float64) map[[2]int]complex128 {
	T := float64(SpecTilePx)
	pos := make([]float64, G)
	for g := range pos {
		pos[g] = (float64(g) + off) * step
	}
	rows := map[int][]complex128{} // u -> Σ_x grid[y][x]·e^{-i2πu·sx/T}, per y
	for u := -p.maxUV; u <= p.maxUV; u++ {
		ph := make([]complex128, G)
		for x := range ph {
			ph[x] = phasor(-float64(u) * pos[x] / T)
		}
		r := make([]complex128, G)
		for y := 0; y < G; y++ {
			var acc complex128
			line := grid[y*G : (y+1)*G]
			for x, val := range line {
				if val != 0 {
					acc += ph[x] * complex(val, 0)
				}
			}
			r[y] = acc
		}
		rows[u] = r
	}
	out := map[[2]int]complex128{}
	norm := complex(2/float64(G*G), 0)
	measure := func(u, v int) {
		key := [2]int{u, v}
		if _, done := out[key]; done {
			return
		}
		var acc complex128
		for y, rv := range rows[u] {
			acc += rv * phasor(-float64(v)*pos[y]/T)
		}
		out[key] = acc * norm
	}
	for _, c := range p.coefs {
		measure(c.u, c.v)
	}
	for _, d := range p.decoys {
		measure(d[0], d[1])
	}
	return out
}

// project returns the correlation of the measured spectrum with basis
// function (u,v,ψ) shifted by (ox,oy) tile pixels.
func project(C map[[2]int]complex128, u, v int, psi, ox, oy float64) float64 {
	beta := psi - 2*math.Pi*(float64(u)*ox+float64(v)*oy)/SpecTilePx
	return real(cmplx.Exp(complex(0, beta)) * cmplx.Conj(C[[2]int{u, v}]))
}

type specSync struct {
	ox, oy, z, sigma float64
}

// synchronise finds the tile offset maximising pilot correlation (or scores
// only offset 0 when fixed) and normalises by the decoy noise level.
func (p *specPlan) synchronise(C map[[2]int]complex128, fixed bool) specSync {
	T := SpecTilePx
	type pilot struct {
		u, v int
		a    complex128 // sign·e^{iψ}·conj(C)
	}
	var pilots []pilot
	for _, c := range p.coefs {
		if c.pilot {
			pilots = append(pilots, pilot{c.u, c.v, complex(c.sign, 0) * cmplx.Exp(complex(0, c.psi)) * cmplx.Conj(C[[2]int{c.u, c.v}])})
		}
	}
	score := func(ox, oy float64) float64 {
		s := 0.0
		for _, pl := range pilots {
			s += real(pl.a * phasor(-(float64(pl.u)*ox+float64(pl.v)*oy)/float64(T)))
		}
		return s
	}
	best := specSync{}
	bestScore := math.Inf(-1)
	if fixed {
		bestScore = score(0, 0)
	} else {
		table := make([][]complex128, 2*p.maxUV+1) // table[u+maxUV][o]
		for u := -p.maxUV; u <= p.maxUV; u++ {
			table[u+p.maxUV] = make([]complex128, T)
			for o := 0; o < T; o++ {
				table[u+p.maxUV][o] = phasor(-float64(u*o) / float64(T))
			}
		}
		for oy := 0; oy < T; oy++ {
			for ox := 0; ox < T; ox++ {
				s := 0.0
				for _, pl := range pilots {
					s += real(pl.a * table[pl.u+p.maxUV][ox] * table[pl.v+p.maxUV][oy])
				}
				if s > bestScore {
					best.ox, best.oy, bestScore = float64(ox), float64(oy), s
				}
			}
		}
		cx, cy := best.ox, best.oy
		for dy := -1.0; dy <= 1.0; dy += 0.125 {
			for dx := -1.0; dx <= 1.0; dx += 0.125 {
				if s := score(cx+dx, cy+dy); s > bestScore {
					best.ox, best.oy, bestScore = cx+dx, cy+dy, s
				}
			}
		}
	}
	var noise []float64
	for _, d := range p.decoys {
		for _, psi := range []float64{0, math.Pi / 2} {
			noise = append(noise, project(C, d[0], d[1], psi, best.ox, best.oy))
		}
	}
	best.sigma = rms(noise)
	if best.sigma > 0 {
		best.z = bestScore / (best.sigma * math.Sqrt(float64(len(pilots))))
	}
	return best
}

func rms(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := 0.0
	for _, x := range xs {
		s += x * x
	}
	return math.Sqrt(s / float64(len(xs)))
}

// bits reads the codeword at a synchronised offset. Bits whose correlation is
// within one noise deviation of zero are left unobserved.
func (p *specPlan) bits(C map[[2]int]complex128, sy specSync) (codec.Votes, float64) {
	score := make([]float64, codec.CodeLen)
	signal := 0.0
	n := 0
	for _, c := range p.coefs {
		pr := c.sign * project(C, c.u, c.v, c.psi, sy.ox, sy.oy)
		if c.pilot {
			signal += pr
			n++
			continue
		}
		score[c.bit] += pr
	}
	var v codec.Votes
	limit := sy.sigma * math.Sqrt(float64(p.perBit))
	for i, s := range score {
		if math.Abs(s) < limit {
			continue
		}
		bit := uint8(0)
		if s > 0 {
			bit = 1
		}
		v.Add(i, bit, 1)
	}
	// Pilot correlation is the per-component amplitude; scale it to the
	// texture's standard deviation in grey levels.
	return v, signal / float64(max(n, 1)) * math.Sqrt(float64(len(p.coefs))/2)
}

// spectralFromPDF correlates the tile image stored in the PDF directly.
func (e *Engine) spectralFromPDF(f *PDFFile) (codec.Votes, string, bool) {
	var v codec.Votes
	for _, img := range f.Images() {
		if img.W != SpecTilePx || img.H != SpecTilePx {
			continue
		}
		grid := make([]float64, len(img.Gray))
		mean := 0.0
		for _, b := range img.Gray {
			mean += float64(b)
		}
		mean /= float64(len(img.Gray))
		for i, b := range img.Gray {
			grid[i] = float64(b) - mean
		}
		C := e.spec.spectrum(grid, SpecTilePx, 1, 0)
		sy := e.spec.synchronise(C, true)
		if sy.z < specZMin {
			continue
		}
		v, amp := e.spec.bits(C, sy)
		return v, fmt.Sprintf("Watermark image found in the page resources. Sync correlation %.0f sigma, texture %.1f grey levels.", sy.z, amp), true
	}
	return v, "No watermark image among the page images.", false
}

type specResult struct {
	votes  codec.Votes
	sync   specSync
	period float64
	C      map[[2]int]complex128
}

// spectralFromImage recovers the watermark from a raster capture of any scale
// or crop (rotation is not handled).
func (e *Engine) spectralFromImage(g *grayF) (codec.Votes, string, bool) {
	var v codec.Votes
	R, mask := specResidual(g)

	var cands []float64
	if ratio := float64(g.H) / float64(g.W); abs(ratio-PageH/PageW) < 0.03*PageH/PageW {
		cands = append(cands, float64(g.W)/PageW*SpecTilePt) // full-page capture
	}
	try := func(P float64) specResult {
		grid, G := foldResidual(R, mask, g.W, g.H, P, foldOversample)
		C := e.spec.spectrum(grid, G, SpecTilePx/float64(G), 0.5)
		return specResult{sync: e.spec.synchronise(C, false), period: P, C: C}
	}
	search := func(center, span float64, steps int) specResult {
		var best specResult
		for j := -steps; j <= steps; j++ {
			r := try(center * (1 + span*float64(j)/float64(steps)))
			if r.sync.z > best.sync.z {
				best = r
			}
		}
		return best
	}
	// The period must be found to a fraction of a percent: an error of one
	// part in a thousand accumulates across the tiles of a page and smears the
	// high-frequency components the bits live in. Search coarse, then refine.
	refine := func(r specResult) specResult {
		for _, span := range []float64{0.002, 0.0004} {
			if next := search(r.period, span, 5); next.sync.z > r.sync.z {
				r = next
			}
		}
		return r
	}
	var best specResult
	for _, c := range cands {
		if r := search(c, 0.012, 12); r.sync.z > best.sync.z {
			best = r
		}
	}
	if best.sync.z < specZMin {
		// Screen the periods that look most like the watermark, then refine.
		for _, d := range e.periodCandidates(R, mask, g.W, g.H) {
			if r := search(d, 0.014, 7); r.sync.z > best.sync.z {
				best = r
			}
		}
	}
	if best.sync.z >= specZMin-2 { // only refine something that looks real
		best = refine(search(best.period, 0.006, 8))
	}
	if best.period == 0 || best.sync.z < specZMin {
		return v, fmt.Sprintf("No watermark pattern found. Best sync correlation %.1f sigma, %.0f needed.", best.sync.z, specZMin), false
	}
	v, amp := e.spec.bits(best.C, best.sync)
	return v, fmt.Sprintf("Locked on to the pattern: tile every %.1f px (%.2f px/pt), sync correlation %.0f sigma, texture %.1f grey levels.",
		best.period, best.period/SpecTilePt, best.sync.z, amp), true
}

// specResidual isolates faint background texture: ink is masked out and
// replaced by paper tone, then a wide blur removes shading and tint blocks.
func specResidual(g *grayF) ([]float32, []bool) {
	paper, _ := g.levels()
	W, H := g.W, g.H
	ink := make([]bool, W*H)
	for i, v := range g.Pix {
		if v < paper-20 {
			ink[i] = true
		}
	}
	mask := make([]bool, W*H)
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			if !ink[y*W+x] {
				continue
			}
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					if xx, yy := x+dx, y+dy; xx >= 0 && yy >= 0 && xx < W && yy < H {
						mask[yy*W+xx] = true
					}
				}
			}
		}
	}
	filled := &grayF{W: W, H: H, Pix: make([]float32, len(g.Pix))}
	for i, v := range g.Pix {
		if mask[i] {
			v = paper
		}
		filled.Pix[i] = v
	}
	smooth := &grayF{W: W, H: H, Pix: append([]float32(nil), filled.Pix...)}
	smooth.blur(5)
	R := make([]float32, len(g.Pix))
	for i := range R {
		if !mask[i] {
			R[i] = float32(math.Max(-8, math.Min(8, float64(filled.Pix[i]-smooth.Pix[i]))))
		}
	}
	return R, mask
}

// foldResidual averages the residual modulo the tile period P into a G×G grid.
// The grid is sampled twice per pixel so that folding does not blur the fine
// components by up to half a pixel.
const foldOversample = 2

func foldResidual(R []float32, mask []bool, W, H int, P float64, oversample int) ([]float64, int) {
	G := int(math.Ceil(P * float64(oversample)))
	scale := float64(G) / P
	sum := make([]float64, G*G)
	cnt := make([]int32, G*G)
	colBin := make([]int, W)
	for x := range colBin {
		colBin[x] = min(int(math.Mod(float64(x), P)*scale), G-1)
	}
	for y := 0; y < H; y++ {
		gy := min(int(math.Mod(float64(y), P)*scale), G-1)
		for x := 0; x < W; x++ {
			i := y*W + x
			if mask[i] {
				continue
			}
			k := gy*G + colBin[x]
			sum[k] += float64(R[i])
			cnt[k]++
		}
	}
	for k := range sum {
		if cnt[k] > 0 {
			sum[k] /= float64(cnt[k])
		}
	}
	return sum, G
}

// bandEnergy is how strongly a folded grid concentrates energy in the
// watermark band, measured against the frequencies just outside it. It does
// not depend on the tile offset, which makes it a cheap way to screen periods.
func (p *specPlan) bandEnergy(C map[[2]int]complex128) float64 {
	sum := func(freqs [][2]int) float64 {
		t := 0.0
		for _, f := range freqs {
			t += real(C[f] * cmplx.Conj(C[f]))
		}
		return t / float64(max(len(freqs), 1))
	}
	var band [][2]int
	seen := map[[2]int]bool{}
	for _, c := range p.coefs {
		if f := [2]int{c.u, c.v}; !seen[f] {
			seen[f] = true
			band = append(band, f)
		}
	}
	noise := sum(p.decoys)
	if noise == 0 {
		return 0
	}
	return sum(band) / noise
}

// periodCandidates sweeps the plausible range of tile periods and returns the
// few that concentrate the most energy in the watermark band. Each period is
// judged on a window of a few tiles: a wider window would smear the fine
// components whenever the trial period is slightly off.
func (e *Engine) periodCandidates(R []float32, mask []bool, W, H int) []float64 {
	type cand struct{ period, score float64 }
	var cands []cand
	for P := 28.0; P <= 200; P *= 1.008 {
		w, h := min(W, int(4.5*P)), min(H, int(4.5*P))
		if float64(min(w, h)) < 2.2*P {
			break // too few repeats to judge this period
		}
		x0, y0 := clearestWindow(mask, W, H, w, h)
		sub := make([]float32, w*h)
		subMask := make([]bool, w*h)
		for y := 0; y < h; y++ {
			copy(sub[y*w:(y+1)*w], R[(y+y0)*W+x0:(y+y0)*W+x0+w])
			copy(subMask[y*w:(y+1)*w], mask[(y+y0)*W+x0:(y+y0)*W+x0+w])
		}
		grid, G := foldResidual(sub, subMask, w, h, P, 1)
		C := e.spec.spectrum(grid, G, SpecTilePx/float64(G), 0.5)
		cands = append(cands, cand{P, e.spec.bandEnergy(C)})
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].score > cands[j].score })
	var out []float64
	for _, c := range cands[:min(len(cands), 4)] {
		out = append(out, c.period)
	}
	return out
}

// clearestWindow places a w by h window where the least of it is covered by
// ink. The centre of a page of prose is as good as anywhere, but the centre of
// a slide is usually where all the content sits.
func clearestWindow(mask []bool, W, H, w, h int) (int, int) {
	// Summed-area table of the mask, so any window costs four lookups.
	sum := make([]int32, (W+1)*(H+1))
	for y := 0; y < H; y++ {
		row, prev, out := mask[y*W:(y+1)*W], sum[y*(W+1):], sum[(y+1)*(W+1):]
		var line int32
		for x := 0; x < W; x++ {
			if row[x] {
				line++
			}
			out[x+1] = prev[x+1] + line
		}
	}
	covered := func(x, y int) int32 {
		return sum[(y+h)*(W+1)+x+w] - sum[y*(W+1)+x+w] - sum[(y+h)*(W+1)+x] + sum[y*(W+1)+x]
	}
	bx, by := (W-w)/2, (H-h)/2
	best := covered(bx, by)
	for _, fy := range []float64{0, 0.5, 1} {
		for _, fx := range []float64{0, 0.5, 1} {
			x, y := int(float64(W-w)*fx), int(float64(H-h)*fy)
			if c := covered(x, y); c < best {
				best, bx, by = c, x, y
			}
		}
	}
	return bx, by
}

// cropShot simulates a partial screenshot shared through a chat app: page 1
// at 110 dpi, cropped to a central region, scaled to 75% and JPEG-compressed.
func cropShot(src *image.Gray) *image.Gray {
	g := toGrayF(src)
	x0, x1 := int(0.12*float64(g.W)), int(0.80*float64(g.W))
	y0, y1 := int(0.25*float64(g.H)), int(0.62*float64(g.H))
	g.blur(0.6) // anti-aliasing before downscaling
	const s = 0.75
	out := &grayF{W: int(float64(x1-x0) * s), H: int(float64(y1-y0) * s)}
	out.Pix = make([]float32, out.W*out.H)
	for y := 0; y < out.H; y++ {
		for x := 0; x < out.W; x++ {
			out.Pix[y*out.W+x] = g.at(float64(x0)+(float64(x)+0.5)/s-0.5, float64(y0)+(float64(y)+0.5)/s-0.5)
		}
	}
	return out.toImage()
}

// ---- watermark for other file formats ----

// WatermarkTilePNG renders the watermark tile as a small greyscale PNG, for
// embedding as a tiled background in an Open XML file.
func (e *Engine) WatermarkTilePNG(code codec.Codeword) ([]byte, error) {
	tile := e.spec.tile(code)
	img := image.NewGray(image.Rect(0, 0, tile.W, tile.H))
	copy(img.Pix, tile.Gray)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return withDensity(buf.Bytes(), SpecTilePx/SpecTilePt*72), nil
}

// withDensity writes the image's physical resolution into the PNG, so an
// application that tiles it at natural size lays the tile down at exactly
// SpecTilePt on the page, the same size the PDF pipeline uses.
func withDensity(data []byte, dpi float64) []byte {
	ppm := uint32(dpi/0.0254 + 0.5)
	body := make([]byte, 0, 9)
	body = append(body, 'p', 'H', 'Y', 's')
	body = binary.BigEndian.AppendUint32(body, ppm)
	body = binary.BigEndian.AppendUint32(body, ppm)
	body = append(body, 1) // unit: metres

	chunk := binary.BigEndian.AppendUint32(nil, 9)
	chunk = append(chunk, body...)
	chunk = binary.BigEndian.AppendUint32(chunk, crc32.ChecksumIEEE(body))

	// The chunk goes straight after IHDR, which is always the first chunk.
	const at = 8 + 8 + 13 + 4
	if len(data) < at {
		return data
	}
	out := make([]byte, 0, len(data)+len(chunk))
	out = append(out, data[:at]...)
	out = append(out, chunk...)
	return append(out, data[at:]...)
}

// ReadWatermarkCapture reads the watermark from a rendering of a marked file:
// a PDF export, a screenshot or a photo of the screen.
func (e *Engine) ReadWatermarkCapture(data []byte) (codec.Votes, string, bool) {
	var v codec.Votes
	if bytes.HasPrefix(bytes.TrimLeft(data, " \r\n\t"), []byte("%PDF")) {
		f, err := ParsePDF(data)
		if err != nil {
			return v, "the PDF could not be read: " + err.Error(), false
		}
		if v, note, ok := e.spectralFromPDF(f); ok {
			return v, note, ok
		}
		return v, "no watermark image among the page images, and a PDF page cannot be searched without rendering it", false
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return v, "not a readable image", false
	}
	return e.spectralFromImage(loadGray(img))
}

// ReadWatermarkTile reads the watermark from an image that is a whole number
// of tiles, such as one lifted straight out of a file. Captures of a rendered
// page go through the image detector instead, which searches for the period.
func (e *Engine) ReadWatermarkTile(data []byte) (codec.Votes, string, bool) {
	var v codec.Votes
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return v, "not a readable image", false
	}
	g := loadGray(img)
	if g.W%SpecTilePx != 0 || g.H%SpecTilePx != 0 || g.W == 0 {
		return v, fmt.Sprintf("image is %dx%d, not a whole number of %d px tiles", g.W, g.H, SpecTilePx), false
	}
	grid := make([]float64, SpecTilePx*SpecTilePx)
	counts := make([]float64, SpecTilePx*SpecTilePx)
	for y := 0; y < g.H; y++ {
		for x := 0; x < g.W; x++ {
			k := (y%SpecTilePx)*SpecTilePx + x%SpecTilePx
			grid[k] += float64(g.Pix[y*g.W+x])
			counts[k]++
		}
	}
	mean := 0.0
	for i := range grid {
		grid[i] /= counts[i]
		mean += grid[i]
	}
	mean /= float64(len(grid))
	for i := range grid {
		grid[i] -= mean
	}
	C := e.spec.spectrum(grid, SpecTilePx, 1, 0)
	sy := e.spec.synchronise(C, true)
	if sy.z < specZMin {
		return v, fmt.Sprintf("no watermark pattern in the image (sync correlation %.1f sigma)", sy.z), false
	}
	v, amp := e.spec.bits(C, sy)
	return v, fmt.Sprintf("Watermark image found. Sync correlation %.0f sigma, texture %.1f grey levels.", sy.z, amp), true
}
