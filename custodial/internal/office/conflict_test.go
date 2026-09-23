package office

import (
	"regexp"
	"strings"
	"testing"

	"attribution/common/codec"
	"attribution/custodial/internal/document"
)

// issueTwo marks the built-in briefing for recipients A and B with every
// carrier on, and returns both copies with the issuance log.
func issueTwo(t *testing.T, key codec.Key) (a, b []byte, issued []Issued) {
	t.Helper()
	src, err := Parse(buildDocx(t, briefingText()))
	if err != nil {
		t.Fatal(err)
	}
	eng := document.NewEngine(key)
	var copies [][]byte
	for _, label := range []string{"A", "B"} {
		var ids []uint16
		for _, is := range issued {
			ids = append(ids, is.ID)
		}
		id := key.AllocateID(ids)
		tile, err := eng.WatermarkTilePNG(key.Encode(id))
		if err != nil {
			t.Fatal(err)
		}
		marked, err := src.Mark(key, key.Encode(id), key.Tag(id), allLayers, tile)
		if err != nil {
			t.Fatal(err)
		}
		issued = append(issued, Issued{ID: id, Label: label, Layers: allLayers, Tag: key.Tag(id)})
		copies = append(copies, marked)
	}
	return copies[0], copies[1], issued
}

// retag replaces the stored tag in both the custom XML part and the custom
// document property, the way someone editing the package by hand would.
func retag(t *testing.T, data []byte, from, to string) *File {
	t.Helper()
	f := mustParse(t, data)
	for _, p := range []string{customXMLItem, customPart} {
		if !strings.Contains(string(f.parts[p]), from) {
			t.Fatalf("%s does not hold %s", p, from)
		}
		f.set(p, []byte(strings.ReplaceAll(string(f.parts[p]), from, to)))
	}
	return f
}

func detectFile(t *testing.T, key codec.Key, f *File, issued []Issued) Report {
	t.Helper()
	data, err := f.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	rep, err := Detect(key, data, issued, document.NewEngine(key).ReadWatermarkTile)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func candidateMarks(v Result) []string {
	var out []string
	for _, c := range v.Candidates {
		out = append(out, c.Recipient)
	}
	return out
}

// A's copy with B's tag planted: the bit carriers say A, the tags say B. That
// is a conflict, not an attribution to A that happens to list B's layers.
func TestPlantedTagIsAConflict(t *testing.T) {
	key := codec.NewKey()
	a, _, issued := issueTwo(t, key)
	rep := detectFile(t, key, retag(t, a, issued[0].Tag, issued[1].Tag), issued)
	v := rep.Verdict
	if v.Status != "conflict" {
		t.Fatalf("verdict %s (%s), want conflict", v.Status, v.Detail)
	}
	if got := candidateMarks(v); len(got) != 2 || got[0] != "A" || got[1] != "B" {
		t.Errorf("candidates %v, want [A B] with the bit carriers first", got)
	}
	if v.Candidates[0].Strength != "attributed" || v.Candidates[1].Strength != "stored tag" {
		t.Errorf("strengths %s / %s", v.Candidates[0].Strength, v.Candidates[1].Strength)
	}
}

// With the watermark stripped and the spacing zeroed as well, only the
// invisible characters point at A, too few to attribute alone, while both
// tags say B. The tags must not win quietly.
func TestPlantedTagAgainstWeakBitsIsAConflict(t *testing.T) {
	key := codec.NewKey()
	a, _, issued := issueTwo(t, key)
	f := retag(t, a, issued[0].Tag, issued[1].Tag)
	f.dropBackground()
	zero := regexp.MustCompile(`(<w:spacing w:val=")-?\d+(")`)
	for _, p := range f.contentParts() {
		f.set(p, zero.ReplaceAll(f.parts[p], []byte("${1}0${2}")))
	}
	rep := detectFile(t, key, f, issued)
	for _, r := range rep.Results {
		t.Logf("%-22s %-12s %s", r.Name, r.Status, r.Detail)
	}
	if rep.Verdict.Status != "conflict" {
		t.Fatalf("verdict %s %s (%s), want conflict", rep.Verdict.Status, rep.Verdict.Recipient, rep.Verdict.Detail)
	}
}

// A tag whose check value was edited is tampering, and says so.
func TestForgedCheckValueIsInvalid(t *testing.T) {
	key := codec.NewKey()
	a, _, issued := issueTwo(t, key)
	forged := codec.FormatID(issued[1].ID) + ".000000"
	rep := detectFile(t, key, retag(t, a, issued[0].Tag, forged), issued)
	for _, r := range rep.Results {
		if r.Technique == "metadata" || r.Technique == "customxml" {
			if r.Status != "invalid" || !strings.Contains(r.Detail, "B") {
				t.Errorf("%s: %s (%s), want invalid naming the claimed recipient", r.Name, r.Status, r.Detail)
			}
		}
	}
	v := rep.Verdict
	if v.Status != "attributed" || v.Recipient != "A" {
		t.Errorf("verdict %s %s, want A from the bit carriers", v.Status, v.Recipient)
	}
	if len(v.Warnings) == 0 {
		t.Error("no tampering warning on the verdict")
	}
	for _, l := range v.ReadFrom {
		if l == "Metadata" || l == "Custom XML part" {
			t.Errorf("verdict reads from %s, which failed its check", l)
		}
	}
}

// An untouched copy agrees with itself: no conflict, and every carrier is
// listed as agreeing.
func TestCleanCopyReadsFromAgreeingLayers(t *testing.T) {
	key := codec.NewKey()
	a, _, issued := issueTwo(t, key)
	rep := detectFile(t, key, mustParse(t, a), issued)
	v := rep.Verdict
	if v.Status != "attributed" || v.Recipient != "A" || len(v.Warnings) > 0 {
		t.Fatalf("verdict %s %s %v", v.Status, v.Recipient, v.Warnings)
	}
	if len(v.ReadFrom) < 4 {
		t.Errorf("reads from %v", v.ReadFrom)
	}
}
