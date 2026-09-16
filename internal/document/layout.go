package document

import (
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"

	"custodial/internal/codec"
)

const (
	PageW     = 595.0 // A4
	PageH     = 842.0
	Margin    = 72.0
	TitleSize = 16.0
	BodySize  = 11.0
	Leading   = 16.0
	ParaGap   = 6.0

	// ShiftPt is the vertical line displacement of the layout layer. Line-shift
	// coding (Brassil et al.) is the layout technique most robust to print+scan.
	ShiftPt = 0.45
	// CellPt / TintGray define the object layer: a page-wide grid of cells, the
	// "1" cells filled with a 2% grey tint drawn behind the text.
	CellPt   = 14.0
	TintGray = 0.985

	maxWords = 6000
)

type LineGeom struct {
	Start, End int // token range
	X, BaseY   float64
	Size       float64
	Body       int // body-line counter, -1 for title lines (never shifted)
}

type Master struct {
	Title     string       `json:"title"`
	Source    string       `json:"source"`
	Warnings  []string     `json:"warnings"`
	Words     int          `json:"words"`
	Pages     int          `json:"pages"`
	BodyLines int          `json:"bodyLines"`
	Tiles     int          `json:"tilesPerPage"`
	CellCount int          `json:"cellsPerPage"`
	Tokens    []string     `json:"-"`
	Geometry  [][]LineGeom `json:"-"`
	PDF       []byte       `json:"-"`
}

type Engine struct {
	Key   codec.Key
	Font  *Font
	cells [][]int // [cy][cx] -> codeword bit index
	spec  *specPlan
}

func NewEngine(key codec.Key) *Engine {
	e := &Engine{Key: key, Font: LoadFont(), spec: newSpecPlan(key)}
	cols, rows := int(PageW)/int(CellPt), int(PageH)/int(CellPt)
	e.cells = make([][]int, rows)
	for cy := range e.cells {
		e.cells[cy] = make([]int, cols)
		for cx := range e.cells[cy] {
			e.cells[cy][cx] = int(key.Sum("cell", strconv.Itoa(cx), strconv.Itoa(cy))[0]) % codec.CodeLen
		}
	}
	return e
}

// NewMaster typesets the document once. Every recipient copy reuses this
// geometry; only the marks differ.
func (e *Engine) NewMaster(doc Doc, source string) *Master {
	m := &Master{Title: doc.Title, Source: source}
	if strings.TrimSpace(m.Title) == "" {
		m.Title = "Untitled document"
	}
	textW := PageW - 2*Margin
	var page []LineGeom
	y := PageH - Margin - TitleSize
	body := 0

	emit := func(tokens []string, size float64, isTitle bool) {
		start := len(m.Tokens)
		m.Tokens = append(m.Tokens, tokens...)
		lineStart, lineW := start, 0.0
		space := e.Font.Width(" ", size)
		place := func(end int) {
			if !isTitle && y < Margin {
				m.Geometry = append(m.Geometry, page)
				page = nil
				y = PageH - Margin - size
			}
			g := LineGeom{Start: lineStart, End: end, X: Margin, BaseY: y, Size: size, Body: -1}
			if !isTitle {
				g.Body = body
				body++
			}
			page = append(page, g)
			if isTitle {
				y -= size * 1.3
			} else {
				y -= Leading
			}
		}
		for i := start; i < len(m.Tokens); i++ {
			w := e.Font.Width(m.Tokens[i], size)
			if i > lineStart && lineW+space+w > textW {
				place(i)
				lineStart, lineW = i, w
				continue
			}
			if i > lineStart {
				lineW += space
			}
			lineW += w
		}
		if lineStart < len(m.Tokens) {
			place(len(m.Tokens))
		}
	}

	emit(strings.Fields(Normalize(m.Title)), TitleSize, true)
	y -= 10
	words := 0
	for _, p := range doc.Paragraphs {
		toks := strings.Fields(Normalize(p))
		if words+len(toks) > maxWords {
			m.Warnings = append(m.Warnings, fmt.Sprintf("Only the first %d words are used.", maxWords))
			break
		}
		words += len(toks)
		emit(toks, BodySize, false)
		y -= ParaGap
	}
	m.Geometry = append(m.Geometry, page)
	m.Words = len(m.Tokens)
	m.Pages = len(m.Geometry)
	m.BodyLines = body
	m.Tiles = int(math.Ceil(PageW/SpecTilePt)) * int(math.Ceil(PageH/SpecTilePt))
	m.CellCount = len(e.cells) * len(e.cells[0])
	m.PDF = e.Font.WritePDF(e.pageContents(m, nil, Layers{}), nil, [][2]string{{"Title", m.Title}, {"Producer", "Leak Attribution Demo (unmarked master)"}}, e.Key.Sum("master-id")[:16])
	if m.BodyLines < codec.CodeLen {
		m.Warnings = append(m.Warnings, fmt.Sprintf("Short document: %d lines of text cannot carry the full tag in the layout layer.", m.BodyLines))
	}
	return m
}

func pdfString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `(`, `\(`, `)`, `\)`)
	return "(" + r.Replace(s) + ")"
}

// pageContents renders content streams. code == nil produces the unmarked master.
// Drawing order: tint cells, watermark tiles (Multiply), text.
func (e *Engine) pageContents(m *Master, code *codec.Codeword, layers Layers) [][]byte {
	var pages [][]byte
	for _, lines := range m.Geometry {
		var b strings.Builder
		if code != nil && layers.Object {
			fmt.Fprintf(&b, "q %.3f g\n", TintGray)
			for cy, row := range e.cells {
				for cx, idx := range row {
					if code[idx] == 1 {
						fmt.Fprintf(&b, "%.0f %.0f %.0f %.0f re\n", float64(cx)*CellPt, float64(cy)*CellPt, CellPt, CellPt)
					}
				}
			}
			b.WriteString("f Q\n")
		}
		if code != nil && layers.Spectral {
			for y := 0.0; y < PageH; y += SpecTilePt {
				for x := 0.0; x < PageW; x += SpecTilePt {
					fmt.Fprintf(&b, "q /GSm gs %.0f 0 0 %.0f %.0f %.0f cm /%s Do Q\n", SpecTilePt, SpecTilePt, x, y, specImgName)
				}
			}
		}
		b.WriteString("BT 0 g\n")
		size := 0.0
		for _, l := range lines {
			if l.Size != size {
				size = l.Size
				fmt.Fprintf(&b, "/F1 %.1f Tf\n", size)
			}
			y := l.BaseY
			if code != nil && layers.Layout && l.Body >= 0 {
				if code[l.Body%codec.CodeLen] == 1 {
					y += ShiftPt
				} else {
					y -= ShiftPt
				}
			}
			fmt.Fprintf(&b, "1 0 0 1 %.2f %.2f Tm %s Tj\n", l.X, y, pdfString(strings.Join(m.Tokens[l.Start:l.End], " ")))
		}
		b.WriteString("ET\n")
		pages = append(pages, []byte(b.String()))
	}
	return pages
}

// metaTag is the metadata-layer value: the mark ID plus a keyed check.
func (e *Engine) metaTag(id uint16) string {
	return fmt.Sprintf("%s.%s", codec.FormatID(id), hex.EncodeToString(e.Key.Sum("meta", strconv.Itoa(int(id)))[:3]))
}

type Layers struct {
	Layout   bool `json:"layout"`
	Spectral bool `json:"spectral"`
	Object   bool `json:"object"`
	Metadata bool `json:"metadata"`
}

// IssueCopy produces one recipient's PDF.
func (e *Engine) IssueCopy(m *Master, id uint16, layers Layers) []byte {
	code := e.Key.Encode(id)
	var images []*PDFImage
	if layers.Spectral {
		images = append(images, e.spec.tile(code))
	}
	info := [][2]string{{"Title", m.Title}, {"Producer", "Leak Attribution Demo"}}
	if layers.Metadata {
		info = append(info, [2]string{"LAPRef", e.metaTag(id)})
	}
	return e.Font.WritePDF(e.pageContents(m, &code, layers), images, info, e.Key.Sum("file-id", strconv.Itoa(int(id)))[:16])
}
