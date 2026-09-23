package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"attribution/custodial/internal/document"
)

// The fixtures are the same 40 paragraphs written by three producers that
// between them cover the common font set-ups: ReportLab with a standard Type1
// font (WinAnsi, ASCII85 over Flate), ReportLab with an embedded TrueType
// subset (ToUnicode), and Chromium print-to-PDF (Type0, Identity-H,
// ToUnicode, a flipped CTM).
func TestExtractCommonProducers(t *testing.T) {
	body, err := os.ReadFile("testdata/body.txt")
	if err != nil {
		t.Fatal(err)
	}
	want := document.DocFromText("Title\n\n" + string(body)).Paragraphs
	for _, name := range []string{"reportlab_type1.pdf", "reportlab_ttf.pdf", "chromium.pdf"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", name))
			if err != nil {
				t.Fatal(err)
			}
			doc, _, _, err := extractDocument(data, name)
			if err != nil {
				t.Fatalf("extraction failed: %v", err)
			}
			if doc.Title != "Quarterly Board Memo" {
				t.Errorf("title %q", doc.Title)
			}
			if len(doc.Paragraphs) != len(want) {
				t.Fatalf("%d paragraphs, want %d", len(doc.Paragraphs), len(want))
			}
			for i := range want {
				if doc.Paragraphs[i] != want[i] {
					t.Fatalf("paragraph %d:\n got  %q\n want %q", i, doc.Paragraphs[i], want[i])
				}
			}
		})
	}
}

// A file that cannot be read is refused. It must never be replaced by the
// sample document, or a user would tag and send the wrong file.
func TestExtractRefusesRatherThanSubstituting(t *testing.T) {
	cases := map[string][]byte{
		"broken.pdf":  []byte("%PDF-1.4\n1 0 obj << /Type /Catalog >> endobj\n%%EOF"),
		"renamed.pdf": []byte("just some text, not a PDF"),
		"empty.txt":   []byte("\n\n"),
		"binary.bin":  {0xff, 0xfe, 0x00, 0x81, 0x92},
	}
	for name, data := range cases {
		doc, source, _, err := extractDocument(data, name)
		if err == nil {
			t.Errorf("%s: accepted (source %q)", name, source)
			continue
		}
		if !strings.Contains(err.Error(), name) {
			t.Errorf("%s: error does not name the file: %v", name, err)
		}
		if doc.Title != "" || len(doc.Paragraphs) != 0 {
			t.Errorf("%s: returned content alongside the error", name)
		}
	}
}
