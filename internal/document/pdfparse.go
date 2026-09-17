package document

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"errors"
	"image/color"
	"image/jpeg"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// This is a deliberately small PDF reader: enough for the files this demo
// writes (and re-saves), plus best-effort text extraction from simple
// uploaded PDFs. It does not handle encryption, object streams for page
// objects, or CID font encodings.

type pdfObject struct {
	dict   []byte
	stream []byte // decoded, nil if absent or undecodable
}

type PDFFile struct {
	Info  map[string]string
	Pages [][]byte // decoded content per page, in page order
	// glyphs maps single-byte codes to text using font /Differences arrays
	// (e.g. TeX fonts put the "fi" ligature at code 2). Best effort: codes
	// that different fonts map differently are left out.
	glyphs       map[byte]string
	pageFonts    []map[string]*fontWidths
	pageImages   []map[string]*PDFImage
	pageMultiply []map[string]bool
}

// Images returns the distinct greyscale images referenced by any page, by name.
func (f *PDFFile) Images() []*PDFImage {
	seen := map[*PDFImage]bool{}
	var out []*PDFImage
	for _, m := range f.pageImages {
		names := make([]string, 0, len(m))
		for n := range m {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			if img := m[n]; !seen[img] {
				seen[img] = true
				out = append(out, img)
			}
		}
	}
	return out
}

// fontWidths holds a simple font's /FirstChar and /Widths (1/1000 em).
type fontWidths struct {
	first  int
	widths []float64
}

func (fw *fontWidths) width(c byte) float64 {
	if fw != nil {
		if i := int(c) - fw.first; i >= 0 && i < len(fw.widths) && fw.widths[i] > 0 {
			return fw.widths[i]
		}
	}
	return 500 // unknown font (e.g. CID): assume half an em
}

// PageContent interprets page i with the file's glyph mapping applied.
func (f *PDFFile) PageContent(i int) PageContent {
	var fonts map[string]*fontWidths
	if i < len(f.pageFonts) {
		fonts = f.pageFonts[i]
	}
	pc := interpret(f.Pages[i], fonts)
	for j, d := range pc.Images {
		if i < len(f.pageImages) {
			pc.Images[j].Img = f.pageImages[i][d.Name]
			pc.Images[j].Multiply = f.pageMultiply[i][d.GS]
		}
	}
	for j, r := range pc.Runs {
		var b strings.Builder
		for k := 0; k < len(r.Text); k++ {
			if g, ok := f.glyphs[r.Text[k]]; ok {
				b.WriteString(g)
			} else {
				b.WriteString(decodeSimpleText(r.Text[k : k+1]))
			}
		}
		pc.Runs[j].Text = b.String()
	}
	return pc
}

var glyphText = map[string]string{
	"fi": "fi", "fl": "fl", "ff": "ff", "ffi": "ffi", "ffl": "ffl", "quoteright": "'", "quoteleft": "'",
	"quotedblleft": "\"", "quotedblright": "\"", "endash": "-", "emdash": "-", "bullet": "-", "minus": "-",
}

func parseDifferences(objs map[int]*pdfObject) map[byte]string {
	out := map[byte]string{}
	conflict := map[byte]bool{}
	for _, o := range objs {
		for _, m := range reDifferences.FindAllSubmatch(o.dict, -1) {
			code := 0
			for _, tok := range reDiffToken.FindAll(m[1], -1) {
				if tok[0] != '/' {
					code, _ = strconv.Atoi(string(tok))
					continue
				}
				if t, ok := glyphText[string(tok[1:])]; ok && code >= 0 && code < 256 && (code < 32 || code > 126) {
					c := byte(code)
					if prev, seen := out[c]; seen && prev != t {
						conflict[c] = true
					}
					out[c] = t
				}
				code++
			}
		}
	}
	for c := range conflict {
		delete(out, c)
	}
	return out
}

var (
	reObj          = regexp.MustCompile(`(\d+)\s+(\d+)\s+obj\b`)
	reStream       = regexp.MustCompile(`stream\r?\n`)
	reLength       = regexp.MustCompile(`/Length\s+(\d+)(\s+\d+\s+R)?`)
	reRef          = regexp.MustCompile(`(\d+)\s+\d+\s+R`)
	reInfo         = regexp.MustCompile(`/Info\s+(\d+)\s+\d+\s+R`)
	reRoot         = regexp.MustCompile(`/Root\s+(\d+)\s+\d+\s+R`)
	rePagesRef     = regexp.MustCompile(`/Pages\s+(\d+)\s+\d+\s+R`)
	reKids         = regexp.MustCompile(`/Kids\s*\[([^\]]*)\]`)
	reContents     = regexp.MustCompile(`/Contents\s*(\[[^\]]*\]|\d+\s+\d+\s+R)`)
	reTypePage     = regexp.MustCompile(`/Type\s*/Page\b`)
	reInfoKV       = regexp.MustCompile(`/(\w+)\s*\(((?:\\.|[^\\)])*)\)`)
	reXObjRef      = regexp.MustCompile(`/XObject\s+(\d+)\s+\d+\s+R`)
	reXObjInline   = regexp.MustCompile(`/XObject\s*<<((?:[^<>]|<[^<]|>[^>])*)>>`)
	reSubtypeImage = regexp.MustCompile(`/Subtype\s*/Image\b`)
	reDeviceGray   = regexp.MustCompile(`/ColorSpace\s*/DeviceGray\b`)
	reBPC8         = regexp.MustCompile(`/BitsPerComponent\s+8\b`)
	reImgWidth     = regexp.MustCompile(`/Width\s+(\d+)`)
	reImgHeight    = regexp.MustCompile(`/Height\s+(\d+)`)
	reMultiplyGS   = regexp.MustCompile(`/([^\s/<>\[\]()]+)\s*<<[^<>]*/BM\s*/Multiply`)
	reResRef       = regexp.MustCompile(`/Resources\s+(\d+)\s+\d+\s+R`)
	reFontRef      = regexp.MustCompile(`/Font\s+(\d+)\s+\d+\s+R`)
	reFontInline   = regexp.MustCompile(`/Font\s*<<((?:[^<>]|<[^<]|>[^>])*)>>`)
	reNameRef      = regexp.MustCompile(`/([^\s/<>\[\]()]+)\s+(\d+)\s+\d+\s+R`)
	reFirstChar    = regexp.MustCompile(`/FirstChar\s+(\d+)`)
	reWidthsInline = regexp.MustCompile(`/Widths\s*\[([^\]]*)\]`)
	reWidthsRef    = regexp.MustCompile(`/Widths\s+(\d+)\s+\d+\s+R`)
	reNumber       = regexp.MustCompile(`-?\d+(?:\.\d+)?`)
	reObjStm       = regexp.MustCompile(`/Type\s*/ObjStm\b`)
	reFirst        = regexp.MustCompile(`/First\s+(\d+)`)
	reN            = regexp.MustCompile(`/N\s+(\d+)`)
	reInt          = regexp.MustCompile(`\d+`)
	reDifferences  = regexp.MustCompile(`/Differences\s*\[([^\]]*)\]`)
	reDiffToken    = regexp.MustCompile(`/[^\s/\[\]]+|\d+`)
)

func ParsePDF(data []byte) (*PDFFile, error) {
	if !bytes.HasPrefix(bytes.TrimLeft(data, " \r\n\t"), []byte("%PDF")) {
		return nil, errors.New("not a PDF file")
	}
	locs := reObj.FindAllSubmatchIndex(data, -1)
	objs := map[int]*pdfObject{}
	var order []int
	type pendingStream struct {
		num, dataStart, end int
		dict                []byte
	}
	var pending []pendingStream
	for i, loc := range locs {
		num, _ := strconv.Atoi(string(data[loc[2]:loc[3]]))
		start, end := loc[1], len(data)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		body := data[start:end]
		endobj := bytes.Index(body, []byte("endobj"))
		sIdx := reStream.FindIndex(body)
		o := &pdfObject{}
		if sIdx != nil && (endobj < 0 || sIdx[0] < endobj) && bytes.Contains(body[:sIdx[0]], []byte("<<")) {
			o.dict = body[:sIdx[0]]
			pending = append(pending, pendingStream{num, start + sIdx[1], end, o.dict})
		} else if endobj >= 0 {
			o.dict = body[:endobj]
		} else {
			o.dict = body
		}
		if _, seen := objs[num]; !seen {
			order = append(order, num)
		}
		objs[num] = o // later objects (incremental updates) win
	}
	for _, p := range pending {
		length := -1
		if m := reLength.FindSubmatch(p.dict); m != nil {
			n, _ := strconv.Atoi(string(m[1]))
			if len(m[2]) == 0 {
				length = n
			} else if ref, ok := objs[n]; ok {
				if v, err := strconv.Atoi(string(bytes.TrimSpace(ref.dict))); err == nil {
					length = v
				}
			}
		}
		var raw []byte
		if length >= 0 && p.dataStart+length <= p.end {
			raw = data[p.dataStart : p.dataStart+length]
		} else if e := bytes.Index(data[p.dataStart:p.end], []byte("endstream")); e >= 0 {
			raw = bytes.TrimRight(data[p.dataStart:p.dataStart+e], "\r\n")
		}
		objs[p.num].stream = decodeStream(p.dict, raw)
	}

	// Objects packed inside compressed object streams (PDF 1.5+).
	for _, n := range append([]int(nil), order...) {
		o := objs[n]
		if o.stream == nil || !reObjStm.Match(o.dict) {
			continue
		}
		first, count := reFirst.FindSubmatch(o.dict), reN.FindSubmatch(o.dict)
		if first == nil || count == nil {
			continue
		}
		fo, _ := strconv.Atoi(string(first[1]))
		cnt, _ := strconv.Atoi(string(count[1]))
		if fo > len(o.stream) {
			continue
		}
		nums := reInt.FindAll(o.stream[:fo], 2*cnt)
		for k := 0; k+1 < len(nums); k += 2 {
			num, _ := strconv.Atoi(string(nums[k]))
			off, _ := strconv.Atoi(string(nums[k+1]))
			end := len(o.stream)
			if k+3 < len(nums) {
				next, _ := strconv.Atoi(string(nums[k+3]))
				end = fo + next
			}
			if _, exists := objs[num]; exists || fo+off > end || end > len(o.stream) {
				continue
			}
			objs[num] = &pdfObject{dict: o.stream[fo+off : end]}
			order = append(order, num)
		}
	}

	f := &PDFFile{Info: map[string]string{}, glyphs: parseDifferences(objs)}
	if ms := reInfo.FindAllSubmatch(data, -1); len(ms) > 0 {
		n, _ := strconv.Atoi(string(ms[len(ms)-1][1]))
		if o, ok := objs[n]; ok {
			for _, kv := range reInfoKV.FindAllSubmatch(o.dict, -1) {
				f.Info[string(kv[1])] = unescapePDF(kv[2])
			}
		}
	}

	contentsOf := func(o *pdfObject) []byte {
		m := reContents.FindSubmatch(o.dict)
		if m == nil {
			return nil
		}
		var out []byte
		for _, r := range reRef.FindAllSubmatch(m[1], -1) {
			n, _ := strconv.Atoi(string(r[1]))
			if c, ok := objs[n]; ok && c.stream != nil {
				out = append(out, c.stream...)
				out = append(out, '\n')
			}
		}
		return out
	}
	var walk func(n, depth int, inherited []byte)
	walk = func(n, depth int, inherited []byte) {
		o, ok := objs[n]
		if !ok || depth > 20 {
			return
		}
		res := inherited
		if m := reResRef.FindSubmatch(o.dict); m != nil {
			if r, ok := objs[atoi(m[1])]; ok {
				res = r.dict
			}
		} else if i := bytes.Index(o.dict, []byte("/Resources")); i >= 0 {
			res = o.dict[i:]
		}
		if kids := reKids.FindSubmatch(o.dict); kids != nil {
			for _, r := range reRef.FindAllSubmatch(kids[1], -1) {
				walk(atoi(r[1]), depth+1, res)
			}
			return
		}
		if reTypePage.Match(o.dict) {
			f.Pages = append(f.Pages, contentsOf(o))
			f.pageFonts = append(f.pageFonts, fontsOf(objs, res))
			f.pageImages = append(f.pageImages, imagesOf(objs, res))
			f.pageMultiply = append(f.pageMultiply, multiplyOf(res))
		}
	}
	if m := reRoot.FindAllSubmatch(data, -1); len(m) > 0 {
		root, _ := strconv.Atoi(string(m[len(m)-1][1]))
		if cat, ok := objs[root]; ok {
			if pm := rePagesRef.FindSubmatch(cat.dict); pm != nil {
				n, _ := strconv.Atoi(string(pm[1]))
				walk(n, 0, nil)
			}
		}
	}
	if len(f.Pages) == 0 {
		// Fallback: page objects in file order, then any stream with text operators.
		sort.Ints(order)
		for _, n := range order {
			if reTypePage.Match(objs[n].dict) && !bytes.Contains(objs[n].dict, []byte("/Kids")) {
				f.Pages = append(f.Pages, contentsOf(objs[n]))
			}
		}
	}
	if len(f.Pages) == 0 {
		for _, n := range order {
			if s := objs[n].stream; s != nil && bytes.Contains(s, []byte("BT")) && bytes.Contains(s, []byte("ET")) {
				f.Pages = append(f.Pages, s)
			}
		}
	}
	return f, nil
}

func atoi(b []byte) int {
	n, _ := strconv.Atoi(string(b))
	return n
}

func fontsOf(objs map[int]*pdfObject, res []byte) map[string]*fontWidths {
	if res == nil {
		return nil
	}
	var fontDict []byte
	if m := reFontRef.FindSubmatch(res); m != nil {
		if o, ok := objs[atoi(m[1])]; ok {
			fontDict = o.dict
		}
	} else if m := reFontInline.FindSubmatch(res); m != nil {
		fontDict = m[1]
	}
	out := map[string]*fontWidths{}
	for _, m := range reNameRef.FindAllSubmatch(fontDict, -1) {
		o, ok := objs[atoi(m[2])]
		if !ok {
			continue
		}
		fw := &fontWidths{}
		if fc := reFirstChar.FindSubmatch(o.dict); fc != nil {
			fw.first = atoi(fc[1])
		}
		var arr []byte
		if w := reWidthsInline.FindSubmatch(o.dict); w != nil {
			arr = w[1]
		} else if w := reWidthsRef.FindSubmatch(o.dict); w != nil {
			if ao, ok := objs[atoi(w[1])]; ok {
				arr = bytes.Trim(bytes.TrimSpace(ao.dict), "[]")
			}
		}
		for _, num := range reNumber.FindAll(arr, -1) {
			v, _ := strconv.ParseFloat(string(num), 64)
			fw.widths = append(fw.widths, v)
		}
		if len(fw.widths) > 0 {
			out[string(m[1])] = fw
		}
	}
	return out
}

// imagesOf resolves the 8-bit DeviceGray image XObjects named in a resource dictionary.
func imagesOf(objs map[int]*pdfObject, res []byte) map[string]*PDFImage {
	out := map[string]*PDFImage{}
	if res == nil {
		return out
	}
	var dict []byte
	if m := reXObjRef.FindSubmatch(res); m != nil {
		if o, ok := objs[atoi(m[1])]; ok {
			dict = o.dict
		}
	} else if m := reXObjInline.FindSubmatch(res); m != nil {
		dict = m[1]
	}
	for _, m := range reNameRef.FindAllSubmatch(dict, -1) {
		o, ok := objs[atoi(m[2])]
		if !ok || o.stream == nil || !reSubtypeImage.Match(o.dict) || !reDeviceGray.Match(o.dict) || !reBPC8.Match(o.dict) {
			continue
		}
		w, h := reImgWidth.FindSubmatch(o.dict), reImgHeight.FindSubmatch(o.dict)
		if w == nil || h == nil || atoi(w[1])*atoi(h[1]) != len(o.stream) {
			continue
		}
		out[string(m[1])] = &PDFImage{Name: string(m[1]), W: atoi(w[1]), H: atoi(h[1]), Gray: o.stream}
	}
	return out
}

// multiplyOf lists the ExtGState names in a resource dictionary that select /BM /Multiply.
func multiplyOf(res []byte) map[string]bool {
	out := map[string]bool{}
	for _, m := range reMultiplyGS.FindAllSubmatch(res, -1) {
		out[string(m[1])] = true
	}
	return out
}

func decodeStream(dict, raw []byte) []byte {
	if raw == nil {
		return nil
	}
	if !bytes.Contains(dict, []byte("/Filter")) {
		return raw
	}
	if bytes.Contains(dict, []byte("/DCTDecode")) && bytes.Count(dict, []byte("Decode")) == 1 {
		return jpegGray(raw) // an image another application re-encoded
	}
	if !bytes.Contains(dict, []byte("/FlateDecode")) || bytes.Count(dict, []byte("Decode")) > 1 {
		return nil // LZW, chained filters: not needed for this demo
	}
	if r, err := zlib.NewReader(bytes.NewReader(raw)); err == nil {
		if out, err := io.ReadAll(r); err == nil || len(out) > 0 {
			return out
		}
	}
	if out, err := io.ReadAll(flate.NewReader(bytes.NewReader(raw))); err == nil || len(out) > 0 {
		return out
	}
	return nil
}

// jpegGray decodes a JPEG image stream to one byte per pixel, so a watermark
// tile that an application re-encoded can still be read.
func jpegGray(raw []byte) []byte {
	img, err := jpeg.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil
	}
	b := img.Bounds()
	out := make([]byte, b.Dx()*b.Dy())
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			g, _, _, _ := color.GrayModel.Convert(img.At(b.Min.X+x, b.Min.Y+y)).RGBA()
			out[y*b.Dx()+x] = byte(g >> 8)
		}
	}
	return out
}

func unescapePDF(s []byte) string {
	var out []byte
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			out = append(out, s[i])
			continue
		}
		i++
		switch c := s[i]; c {
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		case 'b':
			out = append(out, '\b')
		case 'f':
			out = append(out, '\f')
		case '\r', '\n':
			// line continuation
		default:
			if c >= '0' && c <= '7' {
				v, n := 0, 0
				for n < 3 && i < len(s) && s[i] >= '0' && s[i] <= '7' {
					v = v*8 + int(s[i]-'0')
					i++
					n++
				}
				i--
				out = append(out, byte(v))
			} else {
				out = append(out, c)
			}
		}
	}
	return string(out)
}

// ---- content stream interpretation ----

type TextRun struct {
	X, Y, Size float64
	EndX       float64 // estimated end of the run (exact when font widths are known)
	Text       string
}

type Rect struct {
	X, Y, W, H, Gray float64
}

// ImageDraw places an image XObject on an axis-aligned rectangle (in points).
type ImageDraw struct {
	Name       string
	X, Y, W, H float64
	GS         string // graphics state active when drawn
	Img        *PDFImage
	Multiply   bool
}

type PageContent struct {
	Runs   []TextRun
	Rects  []Rect
	Images []ImageDraw
}

type token struct {
	kind byte // 'n' number, 's' string, '/' name, 'k' keyword, '[' ']'
	num  float64
	str  string
}

func lexContent(b []byte) []token {
	var toks []token
	isDelim := func(c byte) bool { return bytes.IndexByte([]byte("()<>[]{}/%"), c) >= 0 }
	isSpace := func(c byte) bool { return c == ' ' || c == '\n' || c == '\r' || c == '\t' || c == '\f' || c == 0 }
	for i := 0; i < len(b); {
		c := b[i]
		switch {
		case isSpace(c):
			i++
		case c == '%':
			for i < len(b) && b[i] != '\n' && b[i] != '\r' {
				i++
			}
		case c == '(':
			depth, j := 1, i+1
			for j < len(b) && depth > 0 {
				switch b[j] {
				case '\\':
					j++
				case '(':
					depth++
				case ')':
					depth--
				}
				j++
			}
			end := min(j-1, len(b))
			toks = append(toks, token{kind: 's', str: unescapePDF(b[i+1 : max(end, i+1)])})
			i = j
		case c == '<' && i+1 < len(b) && b[i+1] == '<', c == '>' && i+1 < len(b) && b[i+1] == '>':
			i += 2
		case c == '<':
			j := bytes.IndexByte(b[i:], '>')
			if j < 0 {
				return toks
			}
			hexs := bytes.Map(func(r rune) rune {
				if isSpace(byte(r)) {
					return -1
				}
				return r
			}, b[i+1:i+j])
			if len(hexs)%2 == 1 {
				hexs = append(hexs, '0')
			}
			out := make([]byte, len(hexs)/2)
			for k := range out {
				v, _ := strconv.ParseUint(string(hexs[2*k:2*k+2]), 16, 8)
				out[k] = byte(v)
			}
			toks = append(toks, token{kind: 's', str: string(out)})
			i += j + 1
		case c == '[' || c == ']':
			toks = append(toks, token{kind: c})
			i++
		case c == '/':
			j := i + 1
			for j < len(b) && !isSpace(b[j]) && !isDelim(b[j]) {
				j++
			}
			toks = append(toks, token{kind: '/', str: string(b[i+1 : j])})
			i = j
		default:
			j := i
			for j < len(b) && !isSpace(b[j]) && !isDelim(b[j]) {
				j++
			}
			if j == i {
				i++
				continue
			}
			w := string(b[i:j])
			if v, err := strconv.ParseFloat(w, 64); err == nil {
				toks = append(toks, token{kind: 'n', num: v})
			} else {
				toks = append(toks, token{kind: 'k', str: w})
				if w == "ID" { // inline image data: skip to EI
					if e := bytes.Index(b[j:], []byte("EI")); e >= 0 {
						j += e + 2
					} else {
						j = len(b)
					}
				}
			}
			i = j
		}
	}
	return toks
}

// InterpretContent extracts positioned text runs and filled rectangles.
// Transformation matrices (cm) are ignored — sufficient for this demo's PDFs.
func InterpretContent(b []byte) PageContent { return interpret(b, nil) }

func interpret(b []byte, fonts map[string]*fontWidths) PageContent {
	var pc PageContent
	var font *fontWidths
	tc, tw := 0.0, 0.0
	var stack []token
	var inArray bool
	var array []token
	gray, size := 0.0, 0.0
	type gstate struct {
		gray float64
		ctm  [6]float64
		gs   string
	}
	var gstack []gstate
	ctm := [6]float64{1, 0, 0, 1, 0, 0}
	gsName := ""
	var tm, tlm [6]float64
	identity := [6]float64{1, 0, 0, 1, 0, 0}
	tm, tlm = identity, identity
	leading := 0.0
	var pendingRects []Rect
	nums := func(n int) []float64 {
		if len(stack) < n {
			return nil
		}
		out := make([]float64, n)
		for i, t := range stack[len(stack)-n:] {
			out[i] = t.num
		}
		return out
	}
	fontScale := func() float64 {
		if tm[3] < 0 {
			return -tm[3]
		}
		if tm[3] == 0 {
			return 1
		}
		return tm[3]
	}
	addRun := func(x float64, text string) {
		if text != "" {
			pc.Runs = append(pc.Runs, TextRun{X: x, Y: tm[5], Size: size * fontScale(), EndX: tm[4], Text: text})
		}
	}
	// show advances the text matrix glyph by glyph. Producers such as
	// Ghostscript encode word gaps as character spacing or TJ offsets rather
	// than space glyphs, so any gap wider than 0.2 em becomes a space.
	show := func(elems []token) {
		x := tm[4]
		var out []byte
		gap := 0.0
		for _, el := range elems {
			switch el.kind {
			case 's':
				for i := 0; i < len(el.str); i++ {
					c := el.str[i]
					if len(out) > 0 && gap > 0.2*size && c != ' ' && out[len(out)-1] != ' ' {
						out = append(out, ' ')
					}
					gap = tc
					if c == ' ' {
						gap += tw
					}
					tm[4] += (font.width(c)/1000*size + gap) * tm[0]
					out = append(out, c)
				}
			case 'n':
				d := -el.num / 1000 * size
				tm[4] += d * tm[0]
				gap += d
			}
		}
		addRun(x, string(out))
	}
	lastString := func() string {
		if len(stack) > 0 && stack[len(stack)-1].kind == 's' {
			return stack[len(stack)-1].str
		}
		return ""
	}
	for _, t := range lexContent(b) {
		if inArray {
			if t.kind == ']' {
				inArray = false
				stack = append(stack, token{kind: 'a'})
			} else {
				array = append(array, t)
			}
			continue
		}
		if t.kind == '[' {
			inArray, array = true, nil
			continue
		}
		if t.kind != 'k' {
			stack = append(stack, t)
			continue
		}
		switch t.str {
		case "q":
			gstack = append(gstack, gstate{gray, ctm, gsName})
		case "Q":
			if n := len(gstack); n > 0 {
				top := gstack[n-1]
				gray, ctm, gsName, gstack = top.gray, top.ctm, top.gs, gstack[:n-1]
			}
		case "cm":
			if v := nums(6); v != nil {
				ctm = [6]float64{
					v[0]*ctm[0] + v[1]*ctm[2], v[0]*ctm[1] + v[1]*ctm[3],
					v[2]*ctm[0] + v[3]*ctm[2], v[2]*ctm[1] + v[3]*ctm[3],
					v[4]*ctm[0] + v[5]*ctm[2] + ctm[4], v[4]*ctm[1] + v[5]*ctm[3] + ctm[5],
				}
			}
		case "gs":
			if len(stack) > 0 && stack[len(stack)-1].kind == '/' {
				gsName = stack[len(stack)-1].str
			}
		case "Do":
			if len(stack) > 0 && stack[len(stack)-1].kind == '/' {
				pc.Images = append(pc.Images, ImageDraw{Name: stack[len(stack)-1].str, X: ctm[4], Y: ctm[5], W: ctm[0], H: ctm[3], GS: gsName})
			}
		case "g":
			if v := nums(1); v != nil {
				gray = v[0]
			}
		case "rg":
			if v := nums(3); v != nil {
				gray = (v[0] + v[1] + v[2]) / 3
			}
		case "k":
			if v := nums(4); v != nil {
				gray = max(0, 1-min(1, v[3]+(v[0]+v[1]+v[2])/3))
			}
		case "re":
			if v := nums(4); v != nil {
				pendingRects = append(pendingRects, Rect{X: v[0], Y: v[1], W: v[2], H: v[3]})
			}
		case "f", "F", "f*", "B", "B*", "b", "b*":
			for _, r := range pendingRects {
				r.Gray = gray
				pc.Rects = append(pc.Rects, r)
			}
			pendingRects = nil
		case "n", "S", "s", "W", "W*":
			if t.str != "W" && t.str != "W*" {
				pendingRects = nil
			}
		case "BT":
			tm, tlm = identity, identity
		case "Tf":
			if v := nums(1); v != nil {
				size = v[0]
			}
			if len(stack) >= 2 && stack[len(stack)-2].kind == '/' {
				font = fonts[stack[len(stack)-2].str]
			}
		case "Tc":
			if v := nums(1); v != nil {
				tc = v[0]
			}
		case "Tw":
			if v := nums(1); v != nil {
				tw = v[0]
			}
		case "TL":
			if v := nums(1); v != nil {
				leading = v[0]
			}
		case "Tm":
			if v := nums(6); v != nil {
				copy(tm[:], v)
				tlm = tm
			}
		case "Td", "TD":
			if v := nums(2); v != nil {
				tlm[4] += v[0] * tlm[0]
				tlm[5] += v[1] * tlm[3]
				if t.str == "TD" {
					leading = -v[1]
				}
				tm = tlm
			}
		case "T*":
			tlm[5] -= leading * tlm[3]
			tm = tlm
		case "Tj":
			show([]token{{kind: 's', str: lastString()}})
		case "'", "\"":
			tlm[5] -= leading * tlm[3]
			tm = tlm
			show([]token{{kind: 's', str: lastString()}})
		case "TJ":
			show(array)
		}
		stack = stack[:0]
	}
	return pc
}

var winAnsiHigh = map[byte]rune{
	0x80: '€', 0x85: '…', 0x91: '‘', 0x92: '’', 0x93: '“', 0x94: '”', 0x95: '•', 0x96: '–', 0x97: '—',
}

// decodeSimpleText maps single-byte font codes to text: TeX OT1 ligature
// slots, WinAnsi specials, otherwise Latin-1. Our own PDFs are plain ASCII.
func decodeSimpleText(s string) string {
	var b []rune
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 0x0B && c <= 0x0F:
			b = append(b, []rune([]string{"ff", "fi", "fl", "ffi", "ffl"}[c-0x0B])...)
		case winAnsiHigh[c] != 0:
			b = append(b, winAnsiHigh[c])
		case c < 0x20:
			// unmapped control code
		default:
			b = append(b, rune(c))
		}
	}
	return string(b)
}

// Lines groups runs that share a baseline into extracted lines.
func (pc PageContent) Lines(page int) []ExtractedLine {
	var out []ExtractedLine
	for _, r := range pc.Runs {
		if n := len(out); n > 0 && abs(out[n-1].Y-r.Y) < max(1.5, r.Size*0.3) {
			prev := &out[n-1]
			if gap := r.X - prev.endX; gap > 0.2*r.Size && !strings.HasSuffix(prev.Text, " ") && !strings.HasPrefix(r.Text, " ") {
				prev.Text += " "
			}
			prev.Text += r.Text
			prev.endX = r.EndX
			continue
		}
		out = append(out, ExtractedLine{Text: r.Text, Y: r.Y, Size: r.Size, Page: page, endX: r.EndX})
	}
	return out
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
