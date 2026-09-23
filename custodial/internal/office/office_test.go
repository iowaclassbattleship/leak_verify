package office

import (
	"archive/zip"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"attribution/common/codec"
	"attribution/custodial/internal/document"
)

// fixtures are real files produced by LibreOffice; set OFFICE_FIXTURES to the
// directory holding memo.docx, deck.pptx and sheet.xlsx.
func fixtures(t *testing.T) []string {
	t.Helper()
	dir := os.Getenv("OFFICE_FIXTURES")
	if dir == "" {
		t.Skip("set OFFICE_FIXTURES to a directory with memo.docx, deck.pptx and sheet.xlsx")
	}
	var out []string
	for _, n := range []string{"memo.docx", "deck.pptx", "sheet.xlsx"} {
		p := filepath.Join(dir, n)
		if _, err := os.Stat(p); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// allLayers turns on every carrier.
var allLayers = Layers{Spacing: true, Text: true, Metadata: true, CustomXML: true, Background: true}

func TestMarkAndDetect(t *testing.T) {
	key := codec.NewKey()
	eng := document.NewEngine(key)
	all := allLayers
	out := os.Getenv("OFFICE_OUT")

	for _, path := range fixtures(t) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		src, err := Parse(data)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		stats := src.StatsWithKey(key)
		t.Logf("%s: %s, %d words, spacing %d slots/%d bits, text %d slots/%d bits",
			filepath.Base(path), src.Kind.Name(), stats.Words, stats.SpacingSlots, stats.SpacingBits, stats.TextSlots, stats.TextBits)

		var issued []Issued
		copies := map[uint16][]byte{}
		for i := 0; i < 4; i++ {
			var ids []uint16
			for _, is := range issued {
				ids = append(ids, is.ID)
			}
			id := key.AllocateID(ids)
			tag := key.Tag(id)
			tile, err := eng.WatermarkTilePNG(key.Encode(id))
			if err != nil {
				t.Fatal(err)
			}
			marked, err := src.Mark(key, key.Encode(id), tag, all, tile)
			if err != nil {
				t.Fatalf("%s: mark: %v", path, err)
			}
			issued = append(issued, Issued{ID: id, Label: fmt.Sprintf("R%d", i), Layers: all, Tag: tag})
			copies[id] = marked
		}
		leaker := issued[2]
		if out != "" {
			os.WriteFile(filepath.Join(out, "marked-"+filepath.Base(path)), copies[leaker.ID], 0o644)
		}

		// The text must survive marking unchanged.
		if before, after := src.Text(), mustParse(t, copies[leaker.ID]).Text(); before != after {
			t.Errorf("%s: marking changed the readable text", filepath.Base(path))
		}

		// A carrier can only identify a recipient when the file gives it enough
		// slots to carry the codeword several times over.
		spacingOK, textOK := "OK", "OK"
		if stats.SpacingBits < codec.CodeLen {
			spacingOK = "?"
			t.Logf("  (spacing reaches only %d of %d bits)", stats.SpacingBits, codec.CodeLen)
		}
		if stats.TextBits < codec.CodeLen {
			textOK = "?"
			t.Logf("  (invisible characters reach only %d of %d bits)", stats.TextBits, codec.CodeLen)
		}
		want := map[string]map[string]string{
			"none":      {"spacing": spacingOK, "text": textOK, "metadata": "OK", "customxml": "OK", "background": "OK", "combined": "OK"},
			"resave":    {"spacing": spacingOK, "text": textOK, "metadata": "OK", "customxml": "OK", "background": "OK", "combined": "OK"},
			"edit":      {"spacing": "-", "text": "-", "metadata": "OK", "customxml": "OK", "background": "OK", "combined": "OK"},
			"inspect":   {"spacing": spacingOK, "text": textOK, "metadata": "-", "customxml": "-", "background": "OK", "combined": "OK"},
			"cleanup":   {"spacing": spacingOK, "text": "-", "customxml": "OK", "background": "OK", "combined": "OK"},
			"strip":     {"spacing": spacingOK, "text": textOK, "metadata": "OK", "customxml": "OK", "background": "-", "combined": "OK"},
			"copypaste": {"spacing": "-", "text": textOK, "metadata": "-", "customxml": "-", "background": "-", "combined": textOK},
		}
		for _, a := range Attacks {
			art, err := Attack(copies[leaker.ID], a.ID, "copy")
			if err != nil {
				t.Fatalf("%s/%s: %v", path, a.ID, err)
			}
			if out != "" {
				os.WriteFile(filepath.Join(out, a.ID+"-"+art.Name+filepath.Ext(path)), art.Data, 0o644)
			}
			var rep Report
			if art.Kind == "txt" {
				rep = DetectText(key, string(art.Data), issued)
			} else if rep, err = Detect(key, art.Data, issued, eng.ReadWatermarkTile); err != nil {
				t.Fatalf("%s/%s: detect: %v", path, a.ID, err)
			}
			got := map[string]string{}
			for _, r := range append(rep.Results, rep.Verdict) {
				switch {
				case r.Status == "attributed" && r.Recipient == leaker.Label:
					got[r.Technique] = "OK"
				case r.Status == "attributed":
					got[r.Technique] = "WRONG"
					t.Errorf("%s/%s: %s named the wrong recipient", filepath.Base(path), a.ID, r.Technique)
				default:
					got[r.Technique] = "-"
				}
			}
			t.Logf("  %-10s spacing=%-5s text=%-5s bg=%-5s xml=%-5s meta=%-5s combined=%s",
				a.ID, got["spacing"], got["text"], got["background"], got["customxml"], got["metadata"], got["combined"])
			for tech, w := range want[a.ID] {
				if w == "OK" && got[tech] != "OK" {
					for _, r := range rep.Results {
						if r.Technique == tech {
							t.Errorf("%s/%s: expected %s to identify the recipient: %s", filepath.Base(path), a.ID, tech, r.Detail)
						}
					}
				}
				if w == "-" && got[tech] == "OK" {
					t.Errorf("%s/%s: expected %s not to identify anyone", filepath.Base(path), a.ID, tech)
				}
			}
		}

		// An unmarked file must never be attributed.
		rep, err := Detect(key, data, issued, eng.ReadWatermarkTile)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range append(rep.Results, rep.Verdict) {
			if r.Status == "attributed" {
				t.Errorf("%s: unmarked file attributed by %s", filepath.Base(path), r.Technique)
			}
		}
	}
}

// briefingText is varied prose, the way a real document reads. A file that
// repeats one sentence reaches only a few of the 32 codeword bits.
func briefingText() []string {
	return []string{
		"Project Halcyon board briefing, classified strictly confidential and issued to named recipients only.",
		"The steering committee has spent four months in exclusive discussions with Meridian Logistics regarding a possible acquisition.",
		"Meridian operates forty-two warehouses across Switzerland, southern Germany and northern Italy, serving pharmaceutical and grocery customers.",
		"Their route network overlaps with ours on roughly a third of volume, which creates obvious consolidation opportunities.",
		"The indicative offer values the target at four hundred twelve million francs on a cash free, debt free basis.",
		"That corresponds to about eight times normalised operating profit, below recent comparable transactions among regional carriers.",
		"Financing would combine existing reserves with a new term facility from two banks already approached by treasury.",
		"Competition clearance in Germany may take longer than anticipated, and any remedy requirement could delay completion.",
		"Retention packages for the operations team have been authorised in principle but remain unsigned pending legal review.",
		"Due diligence found no material environmental liabilities, though three leases require landlord consent before transfer.",
		"Management recommends proceeding, subject to the conditions described in appendix two of this paper.",
		"Directors should forward questions to the company secretary rather than discussing them outside scheduled meetings.",
	}
}

func mustParse(t *testing.T, data []byte) *File {
	t.Helper()
	f, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// TestCopiesDiffer guards against every recipient getting the same bytes.
func TestCopiesDiffer(t *testing.T) {
	key := codec.NewKey()
	for _, path := range fixtures(t) {
		data, _ := os.ReadFile(path)
		src, err := Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		eng := document.NewEngine(key)
		ta, _ := eng.WatermarkTilePNG(key.Encode(1))
		tb, _ := eng.WatermarkTilePNG(key.Encode(40000))
		a, _ := src.Mark(key, key.Encode(1), key.Tag(1), allLayers, ta)
		b, _ := src.Mark(key, key.Encode(40000), key.Tag(40000), allLayers, tb)
		if string(a) == string(b) {
			t.Errorf("%s: two recipients got identical files", filepath.Base(path))
		}
		for _, marked := range [][]byte{a, b} {
			f, err := Parse(marked)
			if err != nil {
				t.Fatalf("%s: marked file no longer parses: %v", filepath.Base(path), err)
			}
			if f.Text() != src.Text() {
				t.Errorf("%s: marking changed the readable text", filepath.Base(path))
			}
		}
	}
}

// A minimal Word document, so the package is covered without external files.
const miniDocx = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>%s</w:body></w:document>`

func buildDocx(t *testing.T, paragraphs []string) []byte {
	t.Helper()
	var body strings.Builder
	for _, p := range paragraphs {
		body.WriteString(`<w:p><w:r><w:rPr><w:sz w:val="22"/></w:rPr><w:t xml:space="preserve">` + p + `</w:t></w:r></w:p>`)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range map[string]string{
		"[Content_Types].xml": `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`,
		"_rels/.rels":         `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`,
		"word/document.xml":   fmt.Sprintf(miniDocx, body.String()),
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(content))
	}
	zw.Close()
	return buf.Bytes()
}

func TestBuiltInDocument(t *testing.T) {
	key := codec.NewKey()
	paras := briefingText()
	src, err := Parse(buildDocx(t, paras))
	if err != nil {
		t.Fatal(err)
	}
	eng := document.NewEngine(key)
	all := allLayers
	var issued []Issued
	copies := map[uint16][]byte{}
	for i := 0; i < 5; i++ {
		id := key.AllocateID(nil)
		tag := key.Tag(id)
		tile, err := eng.WatermarkTilePNG(key.Encode(id))
		if err != nil {
			t.Fatal(err)
		}
		marked, err := src.Mark(key, key.Encode(id), tag, all, tile)
		if err != nil {
			t.Fatal(err)
		}
		issued = append(issued, Issued{ID: id, Label: fmt.Sprintf("R%d", i), Layers: all, Tag: tag})
		copies[id] = marked
	}
	leaker := issued[3]
	for _, a := range Attacks {
		art, err := Attack(copies[leaker.ID], a.ID, "copy")
		if err != nil {
			t.Fatal(err)
		}
		rep := Report{}
		if art.Kind == "txt" {
			rep = DetectText(key, string(art.Data), issued)
		} else if rep, err = Detect(key, art.Data, issued, eng.ReadWatermarkTile); err != nil {
			t.Fatal(err)
		}
		if rep.Verdict.Status != "attributed" && a.ID != "cleanup" {
			t.Errorf("%s: expected the file to be traced, got %s (%s)", a.ID, rep.Verdict.Status, rep.Verdict.Detail)
		}
		if rep.Verdict.Status == "attributed" && rep.Verdict.Recipient != leaker.Label {
			t.Errorf("%s: traced to the wrong recipient %q", a.ID, rep.Verdict.Recipient)
		}
	}
	// The unmarked original is never traced.
	rep, err := Detect(key, buildDocx(t, paras), issued, eng.ReadWatermarkTile)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Verdict.Status == "attributed" {
		t.Errorf("unmarked document traced to %s", rep.Verdict.Recipient)
	}
}

// TestBackgroundSurvivesRendering marks each fixture, renders it the way a
// recipient would (export to PDF, then a screen capture of the page) and reads
// the watermark back. It needs LibreOffice and poppler, and skips without them.
func TestBackgroundSurvivesRendering(t *testing.T) {
	soffice, err := exec.LookPath("soffice")
	if err != nil {
		t.Skip("needs LibreOffice")
	}
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		t.Skip("needs poppler")
	}
	key := codec.NewKey()
	eng := document.NewEngine(key)
	dir := t.TempDir()

	for _, path := range fixtures(t) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		src, err := Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		id := key.AllocateID(nil)
		issued := []Issued{{ID: id, Label: "R", Tag: "MK.test", Layers: Layers{Background: true}}}
		tile, err := eng.WatermarkTilePNG(key.Encode(id))
		if err != nil {
			t.Fatal(err)
		}
		marked, err := src.Mark(key, key.Encode(id), "MK.test", Layers{Background: true}, tile)
		if err != nil {
			t.Fatal(err)
		}
		name := filepath.Base(path)
		if err := os.WriteFile(filepath.Join(dir, name), marked, 0o644); err != nil {
			t.Fatal(err)
		}

		// Export to PDF, which is also the check that the file still opens.
		cmd := exec.Command(soffice, "--headless", "--convert-to", "pdf", "--outdir", dir, filepath.Join(dir, name))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: convert: %v: %s", name, err, out)
		}
		pdf := filepath.Join(dir, strings.TrimSuffix(name, filepath.Ext(name))+".pdf")

		// A screen capture of the first page, at a typical screen resolution.
		if out, err := exec.Command("pdftoppm", "-r", "96", "-png", "-f", "1", "-l", "1", pdf, pdf+"-shot").CombinedOutput(); err != nil {
			t.Fatalf("%s: render: %v: %s", name, err, out)
		}
		shots, _ := filepath.Glob(pdf + "-shot*.png")
		if len(shots) == 0 {
			t.Fatalf("%s: no page image", name)
		}
		for _, p := range append([]string{pdf}, shots...) {
			raw, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			v, note, ok := eng.ReadWatermarkCapture(raw)
			rep := DetectCapture(key, "capture", v, note, ok, issued)
			t.Logf("%-12s %-22s %s", name, filepath.Base(p), rep.Verdict.Detail)
			if rep.Verdict.Status != "attributed" || rep.Verdict.Recipient != "R" {
				t.Errorf("%s: %s not traced: %s (%s)", name, filepath.Base(p), rep.Verdict.Status, rep.Verdict.Detail)
			}
		}
	}
}
