// Package office marks Open XML documents (.docx, .pptx, .xlsx) per recipient
// and reads the marks back.
//
// An Open XML file is a zip of XML parts, so marking edits those parts and
// rewrites the archive. Three carriers are used:
//
//   - Spacing: character spacing of ±0.05 pt on individual words (Word,
//     PowerPoint) or the last digit of row heights and column widths (Excel).
//     Invisible on screen and on paper, and kept by editors that preserve
//     formatting.
//   - Text: a zero-width character after selected words. It travels with the
//     text itself, so it survives copy and paste.
//   - Metadata: a custom document property. Trivially inspected and removed.
package office

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"custodial/internal/codec"
)

type Kind string

const (
	Docx Kind = "docx"
	Pptx Kind = "pptx"
	Xlsx Kind = "xlsx"
)

// Name is the human label of a file kind.
func (k Kind) Name() string {
	switch k {
	case Docx:
		return "Word document"
	case Pptx:
		return "PowerPoint presentation"
	case Xlsx:
		return "Excel workbook"
	}
	return string(k)
}

// File is an Open XML package held in memory.
type File struct {
	Kind  Kind
	names []string // entry order of the original archive
	parts map[string][]byte
}

// IsOfficeFile reports whether data looks like an Open XML package.
func IsOfficeFile(data []byte) bool {
	return len(data) > 4 && data[0] == 'P' && data[1] == 'K' && (data[2] == 3 || data[2] == 5 || data[2] == 7)
}

func Parse(data []byte) (*File, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("not a readable Open XML file: %w", err)
	}
	f := &File{parts: map[string][]byte{}}
	for _, e := range zr.File {
		rc, err := e.Open()
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, err
		}
		f.names = append(f.names, e.Name)
		f.parts[e.Name] = b
	}
	switch {
	case f.parts["word/document.xml"] != nil:
		f.Kind = Docx
	case f.has("ppt/slides/"):
		f.Kind = Pptx
	case f.parts["xl/workbook.xml"] != nil:
		f.Kind = Xlsx
	default:
		return nil, fmt.Errorf("unsupported Open XML file: expected a .docx, .pptx or .xlsx")
	}
	return f, nil
}

func (f *File) has(prefix string) bool {
	for _, n := range f.names {
		if strings.HasPrefix(n, prefix) {
			return true
		}
	}
	return false
}

func (f *File) clone() *File {
	c := &File{Kind: f.Kind, names: append([]string(nil), f.names...), parts: make(map[string][]byte, len(f.parts))}
	for k, v := range f.parts {
		c.parts[k] = append([]byte(nil), v...)
	}
	return c
}

func (f *File) set(name string, data []byte) {
	if _, ok := f.parts[name]; !ok {
		f.names = append(f.names, name)
	}
	f.parts[name] = data
}

func (f *File) remove(name string) {
	if _, ok := f.parts[name]; !ok {
		return
	}
	delete(f.parts, name)
	for i, n := range f.names {
		if n == name {
			f.names = append(f.names[:i], f.names[i+1:]...)
			break
		}
	}
}

// Bytes writes the package back out as a zip archive.
func (f *File) Bytes() ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range f.names {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(f.parts[name]); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// contentParts lists the XML parts that hold text, in a stable order.
func (f *File) contentParts() []string {
	var out []string
	for _, n := range f.names {
		switch {
		case f.Kind == Docx && n == "word/document.xml",
			f.Kind == Pptx && strings.HasPrefix(n, "ppt/slides/slide") && strings.HasSuffix(n, ".xml"),
			f.Kind == Xlsx && n == "xl/sharedStrings.xml":
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// sheetParts lists the worksheet parts that carry row heights and column widths.
func (f *File) sheetParts() []string {
	var out []string
	for _, n := range f.names {
		if strings.HasPrefix(n, "xl/worksheets/sheet") && strings.HasSuffix(n, ".xml") {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

var (
	reTextEl = regexp.MustCompile(`(?s)<(w:t|a:t|t)(\s[^>]*)?>(.*?)</(?:w:t|a:t|t)>`)
	reTag    = regexp.MustCompile(`(?s)<[^>]+>`)
)

var reParaEnd = regexp.MustCompile(`</(?:w:p|a:p|si)>`)

// Text returns the readable text of the document, without the invisible marks.
// Runs are joined without separators, since marking splits them per word.
func (f *File) Text() string {
	var b strings.Builder
	for _, p := range f.contentParts() {
		for _, para := range reParaEnd.Split(string(f.parts[p]), -1) {
			line := ""
			for _, m := range reTextEl.FindAllStringSubmatch(para, -1) {
				line += m[3]
			}
			if strings.TrimSpace(line) != "" {
				b.WriteString(line)
				b.WriteByte('\n')
			}
		}
	}
	return strings.NewReplacer(zwMark, "", "&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", "\"", "&apos;", "'").Replace(b.String())
}

// Stats describes how much room the file offers each carrier. The bit counts
// say how many of the 32 codeword bits the carrier can actually reach: a file
// that repeats the same few phrases has many slots but few distinct bits.
type Stats struct {
	Words        int `json:"words"`
	SpacingSlots int `json:"spacingSlots"`
	SpacingBits  int `json:"spacingBits"`
	TextSlots    int `json:"textSlots"`
	TextBits     int `json:"textBits"`
	Parts        int `json:"parts"`
}

// StatsWithKey adds the counts that depend on the marking key.
func (f *File) StatsWithKey(key codec.Key) Stats {
	s := f.Stats()
	s.TextSlots = f.clone().applyText(key, nil)
	spacing, text := map[int]bool{}, map[int]bool{}
	if f.Kind == Xlsx {
		for _, p := range f.sheetParts() {
			for _, tag := range reXlsxRow.FindAll(f.parts[p], -1) {
				if idx, _, ok := slotFor(key, "", measureToken(p, "row", tag)); ok {
					spacing[idx] = true
				}
			}
			for _, tag := range reXlsxCol.FindAll(f.parts[p], -1) {
				if idx, _, ok := slotFor(key, "", measureToken(p, "col", tag)); ok {
					spacing[idx] = true
				}
			}
		}
	}
	for _, p := range f.contentParts() {
		prev := ""
		for _, m := range reTextEl.FindAllSubmatch(f.parts[p], -1) {
			for _, w := range splitWords(string(m[3])) {
				if f.Kind != Xlsx {
					if idx, _, ok := slotFor(key, prev, w); ok {
						spacing[idx] = true
					}
				}
				if idx, _, ok := textSlot(key, prev, w); ok {
					text[idx] = true
				}
				if core(w) != "" {
					prev = w
				}
			}
		}
	}
	s.SpacingBits, s.TextBits = len(spacing), len(text)
	return s
}

func (f *File) Stats() Stats {
	s := Stats{Parts: len(f.names), Words: len(strings.Fields(f.Text()))}
	marked := f.clone()
	s.SpacingSlots = marked.applySpacing(nil)
	s.TextSlots = 0
	return s
}
