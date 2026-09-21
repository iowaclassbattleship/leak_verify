package tabular

import (
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
)

// Attack describes what a leaker did to their copy before it surfaced.
type Attack struct {
	DropColumns        []string `json:"dropColumns"`
	SamplePct          float64  `json:"samplePct"`   // share of rows kept, 0 or 100 = all
	RoundDigits        int      `json:"roundDigits"` // decimals removed from tolerant numbers
	TruncateTimestamps bool     `json:"truncateTimestamps"`
	Shuffle            bool     `json:"shuffle"`
	Seed               uint64   `json:"seed"`
}

func (a Attack) Describe() string {
	var parts []string
	if len(a.DropColumns) > 0 {
		parts = append(parts, "drop "+strings.Join(a.DropColumns, ", "))
	}
	if a.SamplePct > 0 && a.SamplePct < 100 {
		parts = append(parts, "keep "+strconv.FormatFloat(a.SamplePct, 'g', -1, 64)+"% of rows")
	}
	if a.RoundDigits > 0 {
		parts = append(parts, "round off "+plural(a.RoundDigits, "decimal", "decimals"))
	}
	if a.TruncateTimestamps {
		parts = append(parts, "truncate timestamps")
	}
	if a.Shuffle {
		parts = append(parts, "shuffle rows")
	}
	if len(parts) == 0 {
		return "unchanged copy"
	}
	return strings.Join(parts, ", ")
}

// Apply transforms a copy the way a leaker might before passing it on.
func Apply(src *Table, sc Schema, a Attack) *Table {
	rng := rand.New(rand.NewPCG(a.Seed, a.Seed+1))
	t := src.Clone()

	if a.SamplePct > 0 && a.SamplePct < 100 {
		keep := max(int(math.Round(float64(len(t.Rows))*a.SamplePct/100)), 1)
		perm := rng.Perm(len(t.Rows))[:keep]
		slices.Sort(perm) // a sample keeps source order unless also shuffled
		rows := make([][]string, 0, keep)
		for _, i := range perm {
			rows = append(rows, t.Rows[i])
		}
		t.Rows = rows
	}

	for _, f := range sc.Tolerant {
		c := t.Col(f.Name)
		if c < 0 {
			continue
		}
		switch {
		case f.Timestamp && a.TruncateTimestamps:
			for _, row := range t.Rows {
				if m := reTimestamp.FindStringSubmatchIndex(row[c]); m != nil {
					row[c] = row[c][:m[2]-1] + row[c][m[3]:]
				}
			}
		case !f.Timestamp && a.RoundDigits > 0:
			dec := max(f.Decimals-a.RoundDigits, 0)
			for _, row := range t.Rows {
				if x, err := strconv.ParseFloat(row[c], 64); err == nil {
					p := math.Pow10(dec)
					row[c] = strconv.FormatFloat(math.Round(x*p)/p, 'f', dec, 64)
				}
			}
		}
	}

	if len(a.DropColumns) > 0 {
		var keepIdx []int
		var cols []string
		for i, c := range t.Columns {
			if !slices.Contains(a.DropColumns, c) {
				keepIdx = append(keepIdx, i)
				cols = append(cols, c)
			}
		}
		for ri, row := range t.Rows {
			nr := make([]string, len(keepIdx))
			for j, i := range keepIdx {
				nr[j] = row[i]
			}
			t.Rows[ri] = nr
		}
		t.Columns = cols
	}

	if a.Shuffle {
		rng.Shuffle(len(t.Rows), func(i, j int) { t.Rows[i], t.Rows[j] = t.Rows[j], t.Rows[i] })
	}
	return t
}

// BatteryItem is one attack of the robustness battery.
type BatteryItem struct {
	Name   string `json:"name"`
	Attack Attack `json:"attack"`
}

// Battery is the set of attacks used for the robustness matrix, built from the
// schema so it names the columns of whatever table was uploaded.
func Battery(sc Schema) []BatteryItem {
	var tolerantCols []string
	for _, f := range sc.Tolerant {
		tolerantCols = append(tolerantCols, f.Name)
	}
	ident := append([]string(nil), sc.Match...)
	if sc.Key != "" {
		ident = append([]string{sc.Key}, ident...)
	}
	with := func(f func(*Attack)) Attack {
		a := Attack{}
		f(&a)
		return a
	}
	items := []BatteryItem{
		{"Unchanged copy", Attack{}},
		{"Shuffle rows", with(func(a *Attack) { a.Shuffle = true })},
		{"Keep 10% of rows", with(func(a *Attack) { a.SamplePct = 10 })},
		{"Keep 1% of rows", with(func(a *Attack) { a.SamplePct = 1 })},
	}
	if sc.Dummy != "" {
		items = append(items, BatteryItem{"Drop " + sc.Dummy, with(func(a *Attack) { a.DropColumns = []string{sc.Dummy} })})
	}
	if len(tolerantCols) > 0 {
		items = append(items,
			BatteryItem{"Drop tolerant columns", with(func(a *Attack) { a.DropColumns = tolerantCols })},
			BatteryItem{"Round values", with(func(a *Attack) { a.RoundDigits, a.TruncateTimestamps = 2, true })})
	}
	if len(ident) > 0 {
		items = append(items, BatteryItem{"Drop " + strings.Join(ident, " + "), with(func(a *Attack) { a.DropColumns = ident })})
	}
	if sc.Dummy != "" && len(tolerantCols) > 0 {
		items = append(items,
			BatteryItem{"Round + drop " + sc.Dummy, with(func(a *Attack) {
				a.RoundDigits, a.TruncateTimestamps, a.DropColumns = 2, true, []string{sc.Dummy}
			})},
			BatteryItem{"Keep 10% + drop " + sc.Dummy, with(func(a *Attack) {
				a.SamplePct, a.DropColumns = 10, []string{sc.Dummy}
			})},
			BatteryItem{"Keep 10% + round + drop " + sc.Dummy, with(func(a *Attack) {
				a.SamplePct, a.RoundDigits, a.TruncateTimestamps, a.DropColumns = 10, 2, true, []string{sc.Dummy}
			})})
	}
	return items
}
