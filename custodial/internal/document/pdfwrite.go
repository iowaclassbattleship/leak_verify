package document

import (
	"bytes"
	"compress/zlib"
	"encoding/hex"
	"fmt"
	"strings"
)

func deflate(b []byte) []byte {
	var out bytes.Buffer
	w := zlib.NewWriter(&out)
	w.Write(b)
	w.Close()
	return out.Bytes()
}

// PDFImage is an 8-bit greyscale image XObject.
type PDFImage struct {
	Name string
	W, H int
	Gray []byte
}

// WritePDF emits a minimal PDF 1.4 file: one embedded TrueType font, optional
// greyscale images shared by all pages, one Flate-compressed content stream
// per A4 page, and an Info dictionary. Pages get a /GSm graphics state that
// selects the Multiply blend mode.
func (f *Font) WritePDF(pages [][]byte, images []*PDFImage, info [][2]string, fileID []byte) []byte {
	var b bytes.Buffer
	var offsets []int
	obj := func(body string) {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", len(offsets), body)
	}
	stream := func(dict string, data []byte) {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n<< %s /Length %d /Filter /FlateDecode >>\nstream\n", len(offsets), dict, len(data))
		b.Write(data)
		b.WriteString("\nendstream\nendobj\n")
	}
	b.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")

	firstPage := 7 + len(images)
	var kids []string
	for i := range pages {
		kids = append(kids, fmt.Sprintf("%d 0 R", firstPage+2*i))
	}
	var widths []string
	for c := 32; c < 127; c++ {
		widths = append(widths, fmt.Sprintf("%.0f", f.Widths[c]))
	}
	obj("<< /Type /Catalog /Pages 2 0 R >>")
	obj(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(pages)))
	obj(fmt.Sprintf("<< /Type /Font /Subtype /TrueType /BaseFont /GoRegular /FirstChar 32 /LastChar 126 /Widths [%s] /FontDescriptor 4 0 R /Encoding /WinAnsiEncoding >>", strings.Join(widths, " ")))
	obj(fmt.Sprintf("<< /Type /FontDescriptor /FontName /GoRegular /Flags 32 /FontBBox [%.0f %.0f %.0f %.0f] /ItalicAngle 0 /Ascent %.0f /Descent %.0f /CapHeight %.0f /StemV 80 /FontFile2 5 0 R >>",
		f.BBox[0], f.BBox[1], f.BBox[2], f.BBox[3], f.Ascent, f.Descent, f.CapHeight))
	stream(fmt.Sprintf("/Length1 %d", len(f.TTF)), deflate(f.TTF))
	var infoParts []string
	for _, kv := range info {
		infoParts = append(infoParts, "/"+kv[0]+" "+pdfString(Normalize(kv[1])))
	}
	obj("<< " + strings.Join(infoParts, " ") + " >>")
	resources := "/Font << /F1 3 0 R >>"
	if len(images) > 0 {
		var xobj []string
		for i, img := range images {
			xobj = append(xobj, fmt.Sprintf("/%s %d 0 R", img.Name, 7+i))
			stream(fmt.Sprintf("/Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceGray /BitsPerComponent 8 /Interpolate true", img.W, img.H), deflate(img.Gray))
		}
		resources += " /XObject << " + strings.Join(xobj, " ") + " >> /ExtGState << /GSm << /BM /Multiply >> >>"
	}
	for i, content := range pages {
		obj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.0f %.0f] /Resources << %s >> /Contents %d 0 R >>", PageW, PageH, resources, firstPage+2*i+1))
		stream("", deflate(content))
	}

	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	id := hex.EncodeToString(fileID)
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R /Info 6 0 R /ID [<%s> <%s>] >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, id, id, xref)
	return b.Bytes()
}
