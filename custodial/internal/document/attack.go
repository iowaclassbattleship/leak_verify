package document

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"image/jpeg"
	"image/png"
	"strings"
)

type Artifact struct {
	Kind        string `json:"kind"` // pdf | png | jpeg | txt
	Name        string `json:"name"`
	Description string `json:"description"`
	Data        []byte `json:"-"`
}

type AttackInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

var Attacks = []AttackInfo{
	{"none", "Forward the file", "The recipient's PDF leaks unchanged."},
	{"resave", "Re-save / print to PDF", "Re-written by another tool: new object layout, recompressed streams, custom metadata and document ID dropped."},
	{"strip", "Re-save without backgrounds", "Re-written with all image objects removed, as an editor's \"remove background\" or an ink-saving export would."},
	{"copypaste", "Copy-paste text", "All text selected and pasted into an email or chat. Only the words survive."},
	{"screenshot", "Screenshot", "Full-page screen capture of page 1 at 96 dpi, saved as PNG."},
	{"cropshot", "Cropped screenshot via chat", "Part of page 1 captured at 110 dpi, cropped to about a quarter of the page, scaled to 75% and JPEG-compressed (q70) by a messaging app."},
	{"printscan", "Print + scan", "Page 1 printed on a laser printer and scanned at 150 dpi: highlight clipping, toner spread, 1.4% scale and offset, sensor noise, JPEG q75."},
}

func (e *Engine) Attack(pdf []byte, kind string) (*Artifact, error) {
	if kind == "none" {
		return &Artifact{Kind: "pdf", Name: "leaked.pdf", Data: pdf}, nil
	}
	f, err := ParsePDF(pdf)
	if err != nil {
		return nil, err
	}
	if len(f.Pages) == 0 {
		return nil, fmt.Errorf("no pages found")
	}
	newID := func() []byte {
		id := make([]byte, 16)
		rand.Read(id)
		return id
	}
	info := [][2]string{{"Producer", "Generic PDF Printer 11.2"}}
	switch kind {
	case "resave":
		return &Artifact{Kind: "pdf", Name: "resaved.pdf", Data: e.Font.WritePDF(f.Pages, f.Images(), info, newID())}, nil
	case "strip":
		var pages [][]byte
		for _, p := range f.Pages {
			var keep []string
			for _, line := range strings.Split(string(p), "\n") {
				if !strings.Contains(line, " Do") {
					keep = append(keep, line)
				}
			}
			pages = append(pages, []byte(strings.Join(keep, "\n")))
		}
		return &Artifact{Kind: "pdf", Name: "no-backgrounds.pdf", Data: e.Font.WritePDF(pages, nil, info, newID())}, nil
	case "copypaste":
		var lines []string
		for i := range f.Pages {
			for _, l := range f.PageContent(i).Lines(i) {
				lines = append(lines, l.Text)
			}
		}
		return &Artifact{Kind: "txt", Name: "pasted.txt", Data: []byte(strings.Join(lines, "\n") + "\n")}, nil
	case "screenshot":
		var b bytes.Buffer
		png.Encode(&b, e.Font.Render(f.PageContent(0), 96.0/72))
		return &Artifact{Kind: "png", Name: "screenshot.png", Data: b.Bytes()}, nil
	case "cropshot":
		var b bytes.Buffer
		jpeg.Encode(&b, cropShot(e.Font.Render(f.PageContent(0), 110.0/72)), &jpeg.Options{Quality: 70})
		return &Artifact{Kind: "jpeg", Name: "chat-screenshot.jpg", Data: b.Bytes()}, nil
	case "printscan":
		var b bytes.Buffer
		jpeg.Encode(&b, printScan(e.Font.Render(f.PageContent(0), 150.0/72), 42), &jpeg.Options{Quality: 75})
		return &Artifact{Kind: "jpeg", Name: "scan.jpg", Data: b.Bytes()}, nil
	}
	return nil, fmt.Errorf("unknown attack %q", kind)
}
