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
	RoundCoords        int      `json:"roundCoords"` // decimals kept on lat/lon; <0 = untouched
	RoundRisk          int      `json:"roundRisk"`   // decimals kept on risk_score; <0 = untouched
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
		parts = append(parts, "sample "+strconv.FormatFloat(a.SamplePct, 'g', -1, 64)+"% of rows")
	}
	if a.RoundCoords >= 0 {
		parts = append(parts, "round lat/lon to "+strconv.Itoa(a.RoundCoords)+" dp")
	}
	if a.RoundRisk >= 0 {
		parts = append(parts, "round risk_score to "+strconv.Itoa(a.RoundRisk)+" dp")
	}
	if a.TruncateTimestamps {
		parts = append(parts, "truncate timestamps to seconds")
	}
	if a.Shuffle {
		parts = append(parts, "shuffle rows")
	}
	if len(parts) == 0 {
		return "unchanged copy"
	}
	return strings.Join(parts, "; ")
}

func roundString(v string, dec int) string {
	x, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return v
	}
	p := math.Pow10(dec)
	return strconv.FormatFloat(math.Round(x*p)/p, 'f', dec, 64)
}

func Apply(src *Table, a Attack) *Table {
	rng := rand.New(rand.NewPCG(a.Seed, a.Seed+1))
	t := src.Clone()

	if a.SamplePct > 0 && a.SamplePct < 100 {
		keep := int(math.Round(float64(len(t.Rows)) * a.SamplePct / 100))
		keep = max(keep, 1)
		perm := rng.Perm(len(t.Rows))[:keep]
		slices.Sort(perm) // a sample keeps source order unless also shuffled
		rows := make([][]string, 0, keep)
		for _, i := range perm {
			rows = append(rows, t.Rows[i])
		}
		t.Rows = rows
	}

	for _, row := range t.Rows {
		for ci, col := range t.Columns {
			switch {
			case (col == "latitude" || col == "longitude") && a.RoundCoords >= 0:
				row[ci] = roundString(row[ci], a.RoundCoords)
			case col == "risk_score" && a.RoundRisk >= 0:
				row[ci] = roundString(row[ci], a.RoundRisk)
			case col == "opened_at" && a.TruncateTimestamps && len(row[ci]) > 19:
				row[ci] = row[ci][:19]
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

// Battery is the fixed set of attacks used for the robustness matrix.
func Battery() []struct {
	Name   string
	Attack Attack
} {
	none := Attack{RoundCoords: -1, RoundRisk: -1}
	with := func(f func(*Attack)) Attack { a := none; f(&a); return a }
	tolerantCols := []string{"opened_at", "latitude", "longitude", "risk_score"}
	return []struct {
		Name   string
		Attack Attack
	}{
		{"Unchanged copy", none},
		{"Shuffle rows", with(func(a *Attack) { a.Shuffle = true })},
		{"Sample 10% of rows", with(func(a *Attack) { a.SamplePct = 10 })},
		{"Sample 1% of rows", with(func(a *Attack) { a.SamplePct = 1 })},
		{"Drop " + DummyColumn, with(func(a *Attack) { a.DropColumns = []string{DummyColumn} })},
		{"Drop all tolerant columns", with(func(a *Attack) { a.DropColumns = tolerantCols })},
		{"Drop account_id + email", with(func(a *Attack) { a.DropColumns = []string{"account_id", "email"} })},
		{"Round values (4dp, 2dp, whole seconds)", with(func(a *Attack) { a.RoundCoords, a.RoundRisk, a.TruncateTimestamps = 4, 2, true })},
		{"Round + drop " + DummyColumn, with(func(a *Attack) {
			a.RoundCoords, a.RoundRisk, a.TruncateTimestamps = 4, 2, true
			a.DropColumns = []string{DummyColumn}
		})},
		{"Sample 10% + drop " + DummyColumn, with(func(a *Attack) { a.SamplePct = 10; a.DropColumns = []string{DummyColumn} })},
		{"Sample 10% + round + drop " + DummyColumn, with(func(a *Attack) {
			a.SamplePct, a.RoundCoords, a.RoundRisk, a.TruncateTimestamps = 10, 4, 2, true
			a.DropColumns = []string{DummyColumn}
		})},
	}
}
