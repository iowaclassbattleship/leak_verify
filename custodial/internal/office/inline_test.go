package office

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"attribution/common/codec"
	"attribution/custodial/internal/document"
)

// openpyxl and pandas write every string inline in the worksheet
// (t="inlineStr"), with no shared string table. Those workbooks must read
// and mark like the ones Excel writes.
func TestInlineStringWorkbooks(t *testing.T) {
	for _, name := range []string{"openpyxl.xlsx", "pandas.xlsx"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", name))
			if err != nil {
				t.Fatal(err)
			}
			src := mustParse(t, data)
			if n := src.Stats().Words; n < 3000 {
				t.Fatalf("read %d words", n)
			}
			key := codec.NewKey()
			eng := document.NewEngine(key)
			var issued []Issued
			var copies [][]byte
			for _, label := range []string{"A", "B", "C"} {
				var ids []uint16
				for _, is := range issued {
					ids = append(ids, is.ID)
				}
				id := key.AllocateID(ids)
				tile, _ := eng.WatermarkTilePNG(key.Encode(id))
				marked, err := src.Mark(key, key.Encode(id), key.Tag(id), allLayers, tile)
				if err != nil {
					t.Fatal(err)
				}
				issued = append(issued, Issued{ID: id, Label: label, Layers: allLayers, Tag: key.Tag(id)})
				copies = append(copies, marked)
			}
			leak := mustParse(t, copies[1])
			if leak.Text() != src.Text() {
				t.Error("marking changed the readable text")
			}
			if name == "openpyxl.xlsx" && !strings.Contains(string(leak.parts["xl/worksheets/sheet1.xml"]), "<f>SUM(D2:D201)</f>") {
				t.Error("the formula did not survive marking")
			}
			rep, err := Detect(key, copies[1], issued, eng.ReadWatermarkTile)
			if err != nil {
				t.Fatal(err)
			}
			if rep.Verdict.Status != "attributed" || rep.Verdict.Recipient != "B" {
				t.Errorf("verdict %s %s: %s", rep.Verdict.Status, rep.Verdict.Recipient, rep.Verdict.Detail)
			}
			// The inline strings carry the invisible characters, which is the
			// carrier that survives copying the cells out.
			text := DetectText(key, leak.rawText(), issued)
			for _, r := range text.Results {
				if r.Technique == "text" && (r.Status != "attributed" || r.Recipient != "B") {
					t.Errorf("invisible characters in inline strings: %s %s (%s)", r.Status, r.Recipient, r.Detail)
				}
			}
		})
	}
}
