package document

import (
	"bytes"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	"custodial/internal/codec"
)

type Result struct {
	Technique  string          `json:"technique"`
	Name       string          `json:"name"`
	Survives   string          `json:"survives"`
	Status     string          `json:"status"` // attributed | inconclusive | absent | n/a
	MarkID     string          `json:"markId,omitempty"`
	Recipient  string          `json:"recipient,omitempty"`
	Confidence string          `json:"confidence,omitempty"`
	Detail     string          `json:"detail"`
	Decision   *codec.Decision `json:"decision,omitempty"`
}

type Report struct {
	Input   string   `json:"input"`
	Results []Result `json:"results"`
	Verdict Result   `json:"verdict"`
}

// Issued is the detector's view of one issuance-log entry.
type Issued struct {
	ID     uint16
	Label  string
	Layers Layers
}

var layerInfo = map[string][2]string{
	"layout":   {"Layout", "Survives re-save, screenshots and print+scan. Lost when the text is copied or re-typeset."},
	"spectral": {"Frequency domain", "Survives re-save, screenshots, cropping, rescaling and JPEG. Lost on printing and when background images are removed."},
	"object":   {"Object layer", "Survives re-save and full screenshots. Lost on printing and when the text is copied."},
	"metadata": {"Metadata", "Survives only a byte-for-byte copy. Stripped by any re-save."},
}

func newResult(tech string) Result {
	return Result{Technique: tech, Name: layerInfo[tech][0], Survives: layerInfo[tech][1]}
}

func candidates(issued []Issued, pred func(Layers) bool) []codec.Candidate {
	var c []codec.Candidate
	for _, is := range issued {
		if pred(is.Layers) {
			c = append(c, codec.Candidate{ID: is.ID, Label: is.Label})
		}
	}
	return c
}

func (e *Engine) decide(res *Result, v codec.Votes, cands []codec.Candidate, extraFactor float64) {
	d := e.Key.Decode(v, cands)
	if extraFactor > 1 && d.Status != "absent" {
		d.FalseProb = math.Min(1, d.FalseProb*extraFactor)
		if d.FalseProb > codec.MaxFalseProb && d.Status == "attributed" {
			d.Status, d.MarkID, d.Recipient = "inconclusive", "", ""
		}
		d.Settle()
	}
	res.Decision = &d
	res.Status = d.Status
	if d.Status == "attributed" {
		res.MarkID, res.Recipient = d.MarkID, d.Recipient
	}
	if d.Votes > 0 {
		res.Confidence = codec.FormatProb(d.FalseProb)
	}
}

func (e *Engine) Detect(m *Master, issued []Issued, data []byte) (Report, error) {
	switch {
	case bytes.HasPrefix(bytes.TrimLeft(data, " \r\n\t"), []byte("%PDF")):
		return e.detectPDF(m, issued, data)
	case bytes.HasPrefix(data, []byte("\x89PNG")) || bytes.HasPrefix(data, []byte("\xff\xd8")):
		img, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			return Report{}, fmt.Errorf("cannot decode image: %w", err)
		}
		return e.detectImage(m, issued, img), nil
	case utf8.Valid(data):
		return e.detectText(m, issued, string(data)), nil
	}
	return Report{}, fmt.Errorf("unsupported file: expected PDF, PNG, JPEG or UTF-8 text")
}

// plural renders a count with the matching noun, e.g. "1 page" or "3 pages".
func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// bitSummary reports how much of the codeword a layer recovered.
func bitSummary(d *codec.Decision) string {
	return fmt.Sprintf("Recovered %d of %d bits, %s.", d.BitsObserved, codec.CodeLen, plural(d.BitErrors, "error", "errors"))
}

func notApplicable(tech, why string) Result {
	r := newResult(tech)
	r.Status = "n/a"
	r.Detail = why
	return r
}

func absent(tech, why string) Result {
	r := newResult(tech)
	r.Status = "absent"
	r.Detail = why
	return r
}

func (e *Engine) detectPDF(m *Master, issued []Issued, data []byte) (Report, error) {
	f, err := ParsePDF(data)
	if err != nil {
		return Report{}, err
	}
	pcs := make([]PageContent, len(f.Pages))
	for i := range f.Pages {
		pcs[i] = f.PageContent(i)
	}
	rep := Report{Input: fmt.Sprintf("PDF, %s", plural(len(pcs), "page", "pages"))}

	// Metadata
	meta := newResult("metadata")
	if tag, ok := f.Info["LAPRef"]; ok {
		meta.Status = "inconclusive"
		meta.Detail = fmt.Sprintf("Document properties carry the tag %q, but its check value does not verify.", tag)
		for _, is := range issued {
			if e.MetaTag(is.ID) == tag {
				meta.Status, meta.MarkID, meta.Recipient = "attributed", codec.FormatID(is.ID), is.Label
				meta.Confidence = "Check value verifies"
				meta.Detail = "Valid tag in the document properties. This layer is easily stripped or copied into another file, so it is weak on its own."
			}
		}
	} else {
		meta.Status = "absent"
		meta.Detail = "No tag in the document properties."
		if len(f.Info) > 0 {
			var keys []string
			for k := range f.Info {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			meta.Detail += " Present: " + strings.Join(keys, ", ") + "."
		}
	}

	// Layout: compare each text line's baseline with the master geometry.
	layout := newResult("layout")
	var lv codec.Votes
	matchedPages, lineVotes := 0, 0
	for i, pc := range pcs {
		if i >= len(m.Geometry) || len(pc.Runs) != len(m.Geometry[i]) {
			continue
		}
		matchedPages++
		for j, l := range m.Geometry[i] {
			d := pc.Runs[j].Y - l.BaseY
			if l.Body < 0 || abs(d) < ShiftPt/3 || abs(d) > ShiftPt*3 {
				continue
			}
			bit := uint8(0)
			if d > 0 {
				bit = 1
			}
			lv.Add(l.Body, bit, 1)
			lineVotes++
		}
	}
	e.decide(&layout, lv, candidates(issued, func(l Layers) bool { return l.Layout }), 1)
	switch {
	case matchedPages == 0:
		layout.Status = "absent"
		layout.Detail = "Page text does not line up with the issued layout."
	case lineVotes == 0:
		layout.Detail = fmt.Sprintf("%s aligned with the source layout, but no line is shifted.", plural(matchedPages, "page", "pages"))
	default:
		layout.Detail = fmt.Sprintf("Read %d shifted lines on %s. %s", lineVotes, plural(matchedPages, "page", "pages"), bitSummary(layout.Decision))
	}

	// Object layer: tinted cells in the vector content.
	object := newResult("object")
	var ov codec.Votes
	tintedPages := 0
	for _, pc := range pcs {
		tinted := map[[2]int]bool{}
		for _, r := range pc.Rects {
			if r.Gray < 0.995 && abs(r.W-CellPt) < 0.5 && abs(r.H-CellPt) < 0.5 {
				tinted[[2]int{int(math.Round(r.X / CellPt)), int(math.Round(r.Y / CellPt))}] = true
			}
		}
		if len(tinted) == 0 {
			continue
		}
		tintedPages++
		for cy, row := range e.cells {
			for cx, idx := range row {
				bit := uint8(0)
				if tinted[[2]int{cx, cy}] {
					bit = 1
				}
				ov.Add(idx, bit, 1)
			}
		}
	}
	e.decide(&object, ov, candidates(issued, func(l Layers) bool { return l.Object }), 1)
	if tintedPages == 0 {
		object.Status = "absent"
		object.Detail = "No tint grid in the page content."
	} else {
		object.Detail = fmt.Sprintf("Tint grid found on %s. %s", plural(tintedPages, "page", "pages"), bitSummary(object.Decision))
	}

	spectral := newResult("spectral")
	sv, note, ok := e.spectralFromPDF(f)
	if !ok {
		spectral.Status = "absent"
		spectral.Detail = note
		sv = codec.Votes{}
	} else {
		e.decide(&spectral, sv, candidates(issued, func(l Layers) bool { return l.Spectral }), 1)
		spectral.Detail = fmt.Sprintf("%s %s", note, bitSummary(spectral.Decision))
	}

	rep.Results = []Result{layout, spectral, object, meta}
	rep.Verdict = e.combine(issued, rep.Results, map[string]codec.Votes{"layout": lv, "spectral": sv, "object": ov})
	return rep, nil
}

func (e *Engine) detectText(m *Master, issued []Issued, text string) Report {
	rep := Report{Input: fmt.Sprintf("Plain text, %s", plural(len(strings.Fields(text)), "word", "words"))}
	rep.Results = []Result{
		absent("layout", "Plain text has no page layout."),
		absent("spectral", "Plain text has no background image."),
		absent("object", "Plain text has no graphics."),
		absent("metadata", "Plain text has no document properties."),
	}
	rep.Verdict = e.combine(issued, rep.Results, nil)
	return rep
}

// ---- raster analysis ----

func percentile(hist *[256]int, total int, p float64) float32 {
	target, acc := int(float64(total)*p), 0
	for v, n := range hist {
		acc += n
		if acc > target {
			return float32(v)
		}
	}
	return 255
}

func (g *grayF) levels() (paper, ink float32) {
	var hist [256]int
	for _, v := range g.Pix {
		hist[int(math.Max(0, math.Min(255, float64(v))))]++
	}
	return percentile(&hist, len(g.Pix), 0.90), percentile(&hist, len(g.Pix), 0.004)
}

// rowProfile sums per-row ink coverage, ignoring faint tones (tint, noise).
func (g *grayF) rowProfile() []float64 {
	paper, ink := g.levels()
	span := math.Max(float64(paper-ink), 40)
	const floor = 0.12
	p := make([]float64, g.H)
	for y := 0; y < g.H; y++ {
		s := 0.0
		for _, v := range g.Pix[y*g.W : (y+1)*g.W] {
			c := (float64(paper-v)/span - floor) / (1 - floor)
			if c > 0 {
				s += math.Min(c, 1)
			}
		}
		p[y] = s
	}
	return p
}

func inkExtent(p []float64) (first, last int) {
	peak := 0.0
	for _, v := range p {
		peak = math.Max(peak, v)
	}
	first, last = -1, -1
	for y, v := range p {
		if v > peak*0.03 {
			if first < 0 {
				first = y
			}
			last = y
		}
	}
	return
}

func (m *Master) pageContent(p int) PageContent {
	var pc PageContent
	for _, l := range m.Geometry[p] {
		pc.Runs = append(pc.Runs, TextRun{X: l.X, Y: l.BaseY, Size: l.Size, Text: strings.Join(m.Tokens[l.Start:l.End], " ")})
	}
	return pc
}

type layoutFit struct {
	votes     codec.Votes
	located   int
	voted     int
	meanShift float64
	page      int
	decision  codec.Decision
}

// layoutFromImage measures every text baseline on a raster page, fits the
// master's baseline positions (absorbing scan scale and offset), and reads
// each body line's residual displacement as one bit.
func (e *Engine) layoutFromImage(g *grayF, prof []float64, m *Master, p int) layoutFit {
	fit := layoutFit{page: p}
	s0 := float64(g.W) / PageW
	mprof := toGrayF(e.Font.Render(m.pageContent(p), s0)).rowProfile()
	firstM, lastM := inkExtent(mprof)
	first, last := inkExtent(prof)
	if firstM < 0 || first < 0 || lastM == firstM {
		return fit
	}
	a := float64(last-first) / float64(lastM-firstM)
	window := 0.33 * Leading * s0 * a

	type obs struct {
		t, y float64
		body int
	}
	var pts []obs
	for _, l := range m.Geometry[p] {
		t := PageH - l.BaseY
		pred := float64(first) + (t*s0-float64(firstM))*a
		lo, hi := int(pred-window), int(pred+window)
		if lo < 1 || hi >= len(prof)-2 {
			continue
		}
		best, bestD := -1, 0.0
		for y := lo; y <= hi; y++ {
			if d := prof[y+1] - prof[y]; d < bestD {
				best, bestD = y, d
			}
		}
		if best < 0 {
			continue
		}
		dm, d0, dp := prof[best]-prof[best-1], bestD, prof[best+2]-prof[best+1]
		off := 0.0
		if den := dm - 2*d0 + dp; den != 0 {
			off = math.Max(-0.5, math.Min(0.5, 0.5*(dm-dp)/den))
		}
		pts = append(pts, obs{t, float64(best) + 0.5 + off, l.Body})
	}
	fit.located = len(pts)
	if len(pts) < 4 {
		return fit
	}
	use := make([]bool, len(pts))
	for i := range use {
		use[i] = true
	}
	var alpha, beta float64
	for iter := 0; iter < 3; iter++ {
		var n, st, sy, stt, sty float64
		for i, o := range pts {
			if use[i] {
				n++
				st += o.t
				sy += o.y
				stt += o.t * o.t
				sty += o.t * o.y
			}
		}
		if n < 3 || n*stt-st*st == 0 {
			return fit
		}
		alpha = (n*sty - st*sy) / (n*stt - st*st)
		beta = (sy - alpha*st) / n
		for i, o := range pts {
			use[i] = abs(o.y-(alpha*o.t+beta))/alpha < 3*ShiftPt
		}
	}
	sum := 0.0
	for i, o := range pts {
		if !use[i] || o.body < 0 {
			continue
		}
		r := (o.y - (alpha*o.t + beta)) / alpha // points; negative = moved up
		if abs(r) < 0.15*ShiftPt {
			continue
		}
		bit := uint8(0)
		if r < 0 {
			bit = 1
		}
		fit.votes.Add(o.body, bit, 1)
		fit.voted++
		sum += abs(r)
	}
	if fit.voted > 0 {
		fit.meanShift = sum / float64(fit.voted)
	}
	return fit
}

// objectFromImage averages the paper tone inside every grid cell of a
// full-page capture and thresholds the two tint levels.
func (e *Engine) objectFromImage(g *grayF) (codec.Votes, string, bool) {
	var v codec.Votes
	if ratio := float64(g.H) / float64(g.W); abs(ratio-PageH/PageW) > 0.03*PageH/PageW {
		return v, "Not a full page. This layer needs the whole page in view.", false
	}
	s := float64(g.W) / PageW
	paper, _ := g.levels()
	type cell struct {
		idx  int
		mean float64
	}
	var cells []cell
	for cy, row := range e.cells {
		for cx, idx := range row {
			x0, x1 := int((float64(cx)+0.25)*CellPt*s), int((float64(cx)+0.75)*CellPt*s)
			y0, y1 := int((PageH-(float64(cy)+0.75)*CellPt)*s), int((PageH-(float64(cy)+0.25)*CellPt)*s)
			if x0 < 0 || y0 < 0 || x1 > g.W || y1 > g.H || x1 <= x0 || y1 <= y0 {
				continue
			}
			sum, n := 0.0, 0
			for y := y0; y < y1; y++ {
				for x := x0; x < x1; x++ {
					if px := g.Pix[y*g.W+x]; px >= paper-12 {
						sum += float64(px)
						n++
					}
				}
			}
			if n*10 >= (x1-x0)*(y1-y0)*3 {
				cells = append(cells, cell{idx, sum / float64(n)})
			}
		}
	}
	if len(cells) < 100 {
		return v, "Too few readable cells.", false
	}
	means := make([]float64, len(cells))
	for i, c := range cells {
		means[i] = c.mean
	}
	sort.Float64s(means)
	lo, hi := means[len(means)/10], means[len(means)*9/10]
	if hi-lo < 2 {
		return v, fmt.Sprintf("Cell tones vary by only %.1f grey levels, below the noise floor.", hi-lo), false
	}
	thr := (lo + hi) / 2
	for _, c := range cells {
		bit := uint8(0)
		if c.mean < thr {
			bit = 1
		}
		v.Add(c.idx, bit, 1)
	}
	return v, fmt.Sprintf("Read %d cells at tone levels %.1f and %.1f.", len(cells), lo, hi), true
}

func (e *Engine) detectImage(m *Master, issued []Issued, img image.Image) Report {
	g := loadGray(img)
	b := img.Bounds()
	rep := Report{Input: fmt.Sprintf("Image, %d x %d px", b.Dx(), b.Dy())}
	if g.W != b.Dx() {
		rep.Input += fmt.Sprintf(", analysed at %d x %d", g.W, g.H)
	}

	layout := newResult("layout")
	prof := g.rowProfile()
	var best *layoutFit
	cands := candidates(issued, func(l Layers) bool { return l.Layout })
	for p := range m.Geometry {
		fit := e.layoutFromImage(g, prof, m, p)
		fit.decision = e.Key.Decode(fit.votes, cands)
		if best == nil || fit.decision.FalseProb < best.decision.FalseProb ||
			(fit.decision.FalseProb == best.decision.FalseProb && fit.voted > best.voted) {
			f := fit
			best = &f
		}
	}
	var lv codec.Votes
	if best != nil {
		lv = best.votes
	}
	// Page identity is unknown, so every page was a hypothesis: correct for that.
	e.decide(&layout, lv, cands, float64(len(m.Geometry)))
	switch {
	case best == nil || best.located < 4:
		layout.Status = "absent"
		layout.Detail = "No text lines match the issued layout."
	case best.voted == 0:
		layout.Status = "absent"
		layout.Detail = fmt.Sprintf("Found %d text lines, none shifted from the source layout.", best.located)
	default:
		layout.Detail = fmt.Sprintf("Matched page %d. Measured %d lines, mean shift %.2f pt against %.2f pt embedded. %s",
			best.page+1, best.voted, best.meanShift, ShiftPt, bitSummary(layout.Decision))
	}

	object := newResult("object")
	ov, note, ok := e.objectFromImage(g)
	if !ok {
		object.Status = "absent"
		object.Detail = note
		ov = codec.Votes{}
	} else {
		e.decide(&object, ov, candidates(issued, func(l Layers) bool { return l.Object }), 1)
		object.Detail = fmt.Sprintf("%s %s", note, bitSummary(object.Decision))
	}

	spectral := newResult("spectral")
	sv, snote, sok := e.spectralFromImage(g)
	if !sok {
		spectral.Status = "absent"
		spectral.Detail = snote
		sv = codec.Votes{}
	} else {
		e.decide(&spectral, sv, candidates(issued, func(l Layers) bool { return l.Spectral }), 1)
		spectral.Detail = fmt.Sprintf("%s %s", snote, bitSummary(spectral.Decision))
	}

	rep.Results = []Result{
		layout,
		spectral,
		object,
		absent("metadata", "Images have no document properties."),
	}
	rep.Verdict = e.combine(issued, rep.Results, map[string]codec.Votes{"layout": lv, "spectral": sv, "object": ov})
	return rep
}

// combine pools the codeword evidence of every layer that yielded a signal.
// Each layer is normalised to equal weight so the dense tint grid cannot
// drown the sparser layers.
func (e *Engine) combine(issued []Issued, results []Result, votes map[string]codec.Votes) Result {
	v := Result{Technique: "combined", Name: "Combined verdict"}
	var pooled codec.Votes
	var used []string
	for _, r := range results {
		lv, ok := votes[r.Technique]
		if !ok || r.Status == "absent" || r.Status == "n/a" || lv.Total() == 0 {
			continue
		}
		w := codec.CodeLen / lv.Total()
		for i := range lv.One {
			pooled.One[i] += lv.One[i] * w
			pooled.Zero[i] += lv.Zero[i] * w
		}
		used = append(used, r.Name)
	}
	var attributedBy []string
	who := map[string]bool{}
	for _, r := range results {
		if r.Status == "attributed" {
			attributedBy = append(attributedBy, r.Name+": "+r.Recipient)
			who[r.Recipient] = true
		}
	}
	if len(used) > 0 {
		e.decide(&v, pooled, candidates(issued, func(Layers) bool { return true }), 1)
		v.Detail = "Evidence pooled from " + strings.Join(used, ", ") + "."
	}
	if v.Status != "attributed" {
		for _, r := range results {
			if r.Technique == "metadata" && r.Status == "attributed" {
				v.Status, v.MarkID, v.Recipient = "attributed", r.MarkID, r.Recipient
				v.Confidence = "Weak evidence"
				v.Detail = "Only the metadata tag identifies this file. It is easily copied or forged, so confirm it another way."
			}
		}
	}
	if v.Status == "" {
		v.Status = "absent"
		v.Detail = "No layer produced a signal."
	}
	if len(who) > 1 {
		v.Detail += " Layers disagree (" + strings.Join(attributedBy, "; ") + "), which can mean merged copies or a forged tag."
	}
	return v
}
