package tabular

import (
	"sort"
	"strings"
	"testing"

	"attribution/common/codec"
)

// issueWith issues copies with every measure the QA had on.
func issueWith(t *testing.T, master *Table, tech Techniques, names ...string) *Registry {
	t.Helper()
	sc, _ := DetectSchema(master)
	reg := &Registry{Key: codec.NewKey(), Master: master, Schema: sc, Copies: map[uint16]*Table{}}
	for _, n := range names {
		var ids []uint16
		for _, is := range reg.Issued {
			ids = append(ids, is.MarkID)
		}
		id := reg.Key.AllocateID(ids)
		cp, is := MarkCopy(reg.Key, master, sc, id, tech)
		is.Recipient = n
		reg.Issued = append(reg.Issued, is)
		reg.Copies[id] = cp
	}
	return reg
}

// The QA's collusion case: the union of two recipients' copies, with the
// pseudonym columns and the dummy column dropped, sorted, and the numbers
// normalised. The headline must name both.
func TestMergedCopiesHeadline(t *testing.T) {
	master := Generate(2000, 11)
	tech := Techniques{Canary: true, LowBit: true, Dummy: true, Allocate: true, Redact: true, Params: Params{CanaryRate: 100}}
	reg := issueWith(t, master, tech, "Acme Analytics AG", "Beta Research GmbH")
	a, b := reg.Copies[reg.Issued[0].MarkID], reg.Copies[reg.Issued[1].MarkID]

	merged := &Table{Columns: a.Columns}
	merged.Rows = append(append(merged.Rows, a.Rows[:1000]...), b.Rows[1000:]...)
	drop := append([]string{reg.Schema.Dummy}, reg.Schema.Redact...)
	merged = Apply(merged, reg.Schema, Attack{DropColumns: drop})
	sort.Slice(merged.Rows, func(i, j int) bool { return merged.Rows[i][0] < merged.Rows[j][0] })
	for _, row := range merged.Rows {
		for i, v := range row {
			if n, ok := commaNumber(strings.Replace(v, ".", ",", 1)); ok {
				row[i] = strings.TrimRight(strings.TrimRight(n, "0"), ".") // 12.50 -> 12.5
			}
		}
	}
	rep := reg.Detect(merged)
	v := rep.Verdict
	if v.Status != "merged" {
		for _, r := range rep.Results {
			t.Logf("%-20s %-12s %s", r.Name, r.Status, r.Detail)
		}
		t.Fatalf("verdict %s: %s", v.Status, v.Detail)
	}
	got := map[string]bool{}
	for _, c := range v.Candidates {
		got[c.Recipient] = true
	}
	if !got["Acme Analytics AG"] || !got["Beta Research GmbH"] {
		t.Errorf("candidates %+v", v.Candidates)
	}
}

// The unmodified source is not a "possible mark", and nothing claims the data
// was changed.
func TestSourceIsNotAMark(t *testing.T) {
	master := Generate(1000, 12)
	reg := issueWith(t, master, Techniques{Canary: true, LowBit: true, Dummy: true, Noise: true, Format: true}, "Acme")
	rep := reg.Detect(master)
	if !rep.MatchesSource || rep.Verdict.Status != "absent" {
		t.Fatalf("source: matches %v, verdict %s (%s)", rep.MatchesSource, rep.Verdict.Status, rep.Verdict.Detail)
	}
	for _, r := range append(rep.Results, rep.Verdict) {
		if strings.Contains(r.Detail, "was changed") || strings.Contains(r.Detail, "were changed") {
			t.Errorf("%s says the data changed: %s", r.Name, r.Detail)
		}
	}
	// A sample of the source is still the source.
	half := &Table{Columns: master.Columns, Rows: master.Rows[:400]}
	if rep := reg.Detect(half); !rep.MatchesSource || rep.Verdict.Status != "absent" {
		t.Errorf("sampled source: %s", rep.Verdict.Status)
	}
}
